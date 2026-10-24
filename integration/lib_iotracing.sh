#!/usr/bin/env bash

# Copyright 2026 The HuaTuo Authors.
#
# Authors:
# Tonghao Zhang <tonghao@bamaicloud.com>
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Verify the iotracing CLI end-to-end: a normal run emits well-formed
# JSON with the expected top-level schema and the dd workload we drive
# shows up under per-file attribution. A private filesystem keeps unrelated
# host IO out of the bounded capture.
# Path-validation rules are covered by internal/bpf TestValidateName.

iotracing_filesystem_test() {
	source "${ROOT_DIR}/integration/lib.sh"
	source "${ROOT_DIR}/integration/lib_io.sh"

	require_commands truncate losetup mount umount mountpoint grep timeout setsid python3 jq
	filesystem=$1
	case ${filesystem} in
	ext4 | xfs) ;;
	*) fatal "unsupported test filesystem: ${filesystem}; expected ext4 or xfs" ;;
	esac
	command -v "mkfs.${filesystem}" > /dev/null 2>&1 \
		|| skip "${filesystem} qualification requires mkfs.${filesystem}"
	if ! grep -qw "${filesystem}" /proc/filesystems; then
		if ! command -v modprobe > /dev/null 2>&1 || ! modprobe -n -q "${filesystem}"; then
			skip "${filesystem} is unavailable in the running kernel"
		fi
	fi
	bpf_tool_setup iotracing
	readonly DURATION=8
	readonly READY_TIMEOUT=60
	test_dirs=()
	loop_devices=()
	IO_PID=""
	WORKLOAD_PID=""

	worker_pids=()
	xfs_module_added=0
	signal_status=0
	resources="${TOOL_WORK_DIR}/resources.txt"

	defer_interrupts() {
		trap 'signal_status=130' INT
		trap 'signal_status=143' TERM
	}

	record_resources() {
		printf 'mounts=%s\nloops=%s\ntracer=%s\nworkload=%s\nworkers=%s\n' \
			"${test_dirs[*]}" "${loop_devices[*]}" "${IO_PID}" "${WORKLOAD_PID}" \
			"${worker_pids[*]}" > "${resources}"
		trap 'signal_status=130; exit 130' INT
		trap 'signal_status=143; exit 143' TERM
		((signal_status == 0)) || exit "${signal_status}"
	}

	loop_detached() {
		! losetup "$1" > /dev/null 2>&1
	}

	process_stopped() {
		! kill -0 -- "-$1" 2> /dev/null
	}

	cleanup() {
		local status=$? index pid attached loop_device test_dir
		((signal_status == 0)) || status=${signal_status}
		trap - EXIT
		trap '' INT TERM
		set +e
		for pid in "${IO_PID}" "${WORKLOAD_PID}" "${worker_pids[@]}"; do
			[[ -n ${pid} ]] || continue
			kill -TERM -- "-${pid}" 2> /dev/null || true
			stop_by_pid "${pid}" 5
			kill -KILL -- "-${pid}" 2> /dev/null || true
			if ! wait_until 2 0.1 process_stopped "${pid}"; then
				log_error "iotracing process remains: pid=${pid}; inspect ${resources} before reusing the fixture"
				status=1
			else
				wait "${pid}" 2> /dev/null
			fi
		done
		for ((index = ${#test_dirs[@]} - 1; index >= 0; index--)); do
			test_dir=${test_dirs[index]}
			if mountpoint -q "${test_dir}"; then
				if ! timeout --kill-after=1s 10s umount "${test_dir}"; then
					log_error "cannot unmount ${test_dir}; stop its remaining users before reclaiming the fixture"
					status=1
				fi
			fi
			[[ -e ${test_dir}.img ]] || continue
			# Recover ownership if losetup was interrupted before returning its name.
			if attached=$(timeout --kill-after=1s 5s losetup -j "${test_dir}.img" -n -O NAME); then
				while IFS= read -r loop_device; do
					[[ -n ${loop_device} ]] || continue
					if mountpoint -q "${test_dir}" \
						|| ! timeout --kill-after=1s 10s losetup -d "${loop_device}" \
						|| ! wait_until 5 1 loop_detached "${loop_device}"; then
						log_error "loop cleanup incomplete: ${loop_device}, backing=${test_dir}.img; inspect users before losetup -d"
						status=1
					fi
				done <<< "${attached}"
			else
				log_error "cannot inspect loop ownership: backing=${test_dir}.img"
				status=1
			fi
		done
		if ((xfs_module_added)); then
			if ! timeout --kill-after=1s 10s bash -c '
				while [[ -d /sys/module/xfs ]]; do
					rmmod xfs && exit 0
					sleep 0.1
				done' > "${TOOL_WORK_DIR}/xfs-module-cleanup.log" 2>&1; then
				log_error "cannot restore initially absent XFS module; inspect xfs-module-cleanup.log and remaining mounts"
				status=1
			fi
		fi
		printf 'cleanup_status=%s\n' "${status}" >> "${resources}"
		exit "${status}"
	}
	trap cleanup EXIT
	record_resources

	assert_iotracing_cli_schema() {
		[[ -s ${TOOL_OUT} ]] || fatal "iotracing produced empty output"

		# Envelope: parses as JSON, both top-level keys exist as arrays.
		jq -e '
			(.process_file_io_stats | type == "array") and
			(.io_schedule_timeout_stacks | type == "array") and
			(has("reason_snapshot") | not) and
			(has("failure_reason") | not)
		' "${TOOL_OUT}" > /dev/null \
			|| fatal "iotracing JSON schema invalid"
	}

	assert_iotracing_cli_writes() {
		MATCHED_IO=$(jq -ce '
			[
				.process_file_io_stats[]
				| select(.comm | test("^dd( |$)")) as $process
				| $process.total_files[]?
				| (.fs_write_bps + .disk_write_bps) as $write_bps
				| select(.path == "/io")
				| select($write_bps > 0)
				| {
					pid: $process.pid,
					comm: $process.comm,
					path: .path,
					fs_write_bps: .fs_write_bps,
					disk_write_bps: .disk_write_bps,
					total_write_bps: $write_bps
				}
			]
			| select(length > 0)
		' "${TOOL_OUT}") \
			|| fatal "no dd-attributed write to ${io_test_dir} found"
	}

	assert_iotracing_cli_device() {
		jq -e --argjson major "${major}" --argjson minor "${minor}" '
			all(.process_file_io_stats[].total_files[]?;
				.major == $major and .minor == $minor)
		' "${TOOL_OUT}" > /dev/null || fatal "iotracing output device does not match --device ${device}"
	}

	assert_iotracing_cli_block_reads() {
		jq -e --argjson reader "${block_reader_pid}" \
			--arg disk "${loop_devices[first_loop]##*/}" \
			--argjson major "${major}" --argjson minor "${minor}" '
			[.process_file_io_stats[] | select(.pid == $reader)] as $readers |
			($readers | length == 1) and
			all($readers[];
				(.total_disk_read_bps | type == "number" and . > 0) and
				any(.total_files[]?;
					.major == $major and .minor == $minor and .dev_name == $disk and
					(.disk_read_bps | type == "number" and . > 0))) and
			all(.process_file_io_stats[].total_files[]?;
				.major == $major and .minor == $minor)
		' "${TOOL_WORK_DIR}/block-read.out" > /dev/null \
			|| fatal "buffered block-device read lacks the workload PID, target device or positive read statistics; inspect block-read.out"
	}

	assert_iotracing_cli_empty() {
		jq -e '
			has("process_file_io_stats") and
			(.process_file_io_stats == [] or .process_file_io_stats == null) and
			(has("reason_snapshot") | not) and (has("failure_reason") | not)
		' "${TOOL_WORK_DIR}/empty.json" > /dev/null \
			|| fatal "nonexistent device did not produce a successful empty snapshot"
	}

	assert_iotracing_cli_output_conflict() {
		assert_invalid_args output-conflict --output json \
			--output-storage "${TOOL_WORK_DIR}/unused.sock" --task-id cli-invalid
		grep -Fq -- '--output and --output-storage are mutually exclusive' \
			"${TOOL_WORK_DIR}/invalid-output-conflict.err" \
			|| fatal "output-conflict: missing the mutually exclusive parameter error"
	}

	assert_iotracing_cli_writers() {
		jq -e --argjson first "${worker_pids[0]}" --argjson second "${worker_pids[1]}" '
			[.process_file_io_stats[] | select(.pid == $first or .pid == $second)] as $workers |
			($workers | map(.pid) | sort) == ([$first, $second] | sort) and
			all($workers[];
				(if .pid == $first then "first" else "second" end) as $prefix |
				([.total_files[] | select(.path | test("^/(first|second)-[012]$")) |
					select(.disk_write_bps > 0 and .is_direct == false) | .path] | sort) ==
				[range(0; 3) | "/" + $prefix + "-" + tostring] and
				any(.total_files[]; .path == "/" + $prefix + "-direct" and
					.fs_write_bps > 0 and .is_direct == true))
		' "${TOOL_WORK_DIR}/top-full.out" > /dev/null \
			|| fatal "top-N fixture lacks synchronous files, Direct IO files or their expected is_direct flags"
	}

	assert_iotracing_cli_order() {
		# Rates are rounded individually; sums may differ by one at a tie.
		jq -e --argjson major "${major}" --argjson minor "${minor}" '
			def descending: . as $a | all(range(1; length); $a[. - 1] + 1 >= $a[.]);
			([.process_file_io_stats[] | .total_disk_read_bps + .total_disk_write_bps] | descending) and
			all(.process_file_io_stats[];
				([.total_files[]? | .disk_read_bps + .disk_write_bps] | descending)) and
			all(.process_file_io_stats[].total_files[]?;
				.major == $major and .minor == $minor)
		' "${TOOL_WORK_DIR}/top-full.out" > /dev/null \
			|| fatal "iotracing process/file order or device filtering is incorrect"
	}

	assert_iotracing_cli_file_limit() {
		jq -e --argjson first "${worker_pids[0]}" --argjson second "${worker_pids[1]}" '
			[.process_file_io_stats[] | select(.pid == $first or .pid == $second)] as $workers |
			($workers | map(.pid) | sort) == ([$first, $second] | sort) and
			all(.process_file_io_stats[]; .total_files | length <= 1) and
			all($workers[];
				(if .pid == $first then "first" else "second" end) as $prefix |
				.total_file_count >= 3 and (.total_files | length == 1) and
				(.total_files[0].path | test("^/" + $prefix + "-[012]$")) and
				.total_files[0].disk_write_bps > 0 and
				.total_disk_write_bps > .total_files[0].disk_write_bps)
		' "${TOOL_WORK_DIR}/top-files.out" > /dev/null \
			|| fatal "iotracing file limit lost known writers, their totals or nonempty file trimming"
	}

	assert_iotracing_cli_process_limit() {
		jq -e '
			(.process_file_io_stats | length == 1) and
			all(.process_file_io_stats[];
				(.total_disk_read_bps + .total_disk_write_bps > 0) and
				(.total_files | length > 0))
		' "${TOOL_WORK_DIR}/top-process.out" > /dev/null \
			|| fatal "iotracing process limit did not retain one nonempty IO process"
	}

	assert_iotracing_cli_text() {
		grep -Eq '/(first|second)-[012]' "${TOOL_WORK_DIR}/text.out" \
			|| fatal "text output does not identify a known workload file"
		grep -Eq "(^|[^0-9])(${worker_pids[0]}|${worker_pids[1]})([^0-9]|$)" \
			"${TOOL_WORK_DIR}/text.out" || fatal "text output does not identify a known workload PID"
		if jq -e . "${TOOL_WORK_DIR}/text.out" > /dev/null 2>&1; then
			fatal "--output text returned JSON"
		fi
	}

	iotracing_capture_is_ready() {
		kill -0 "${IO_PID}" 2> /dev/null \
			|| fatal "iotracing exited before capture was ready; see ${TOOL_ERR}"
		grep -Fxq 'iotracing: capture ready' "${TOOL_ERR}"
	}

	run_filesystem_tests() {
		local filesystem=$1 image_size=64M format_flag=-F
		local io_test_dir="${TOOL_WORK_DIR}/${filesystem}-mnt"
		local first_loop=${#loop_devices[@]} device test_dir loop_device
		local format_options=() usage feature
		if [[ ${filesystem} == xfs ]]; then
			image_size=512M
			format_flag=-f
			# New mkfs defaults need newer kernels; these tests need only baseline XFS.
			usage=$(mkfs.xfs 2>&1 || true)
			for feature in inobtcount bigtime; do
				if [[ ${usage} == *"${feature}="* ]]; then
					format_options+=(-m "${feature}=0")
				fi
			done
			for feature in nrext64 exchange; do
				if [[ ${usage} == *"${feature}="* ]]; then
					format_options+=(-i "${feature}=0")
				fi
			done
			if [[ ${usage} == *"parent="* ]]; then
				format_options+=(-n parent=0)
			fi
		fi
		test_dirs+=("${io_test_dir}")

		# The directory alone does not isolate a global probe on the host root device.
		for test_dir in "${io_test_dir}"; do
			truncate -s "${image_size}" "${test_dir}.img"
			defer_interrupts
			timeout --kill-after=1s 10s losetup --find --show "${test_dir}.img" \
				> "${TOOL_WORK_DIR}/loop-device"
			loop_device=$(< "${TOOL_WORK_DIR}/loop-device")
			loop_devices+=("${loop_device}")
			record_resources
			timeout --kill-after=1s 15s "mkfs.${filesystem}" -q "${format_flag}" "${format_options[@]}" "${loop_device}"
			mkdir "${test_dir}"
			if [[ ${filesystem} == xfs ]] && ! grep -qw xfs /proc/filesystems; then
				require_commands rmmod
				# Mount may autoload global probes even when qualification then skips.
				xfs_module_added=1
			fi
			timeout --kill-after=1s 10s mount "${loop_device}" "${test_dir}"
		done
		device=$(cat "/sys/class/block/${loop_devices[first_loop]##*/}/dev")

		# The CLI attaches XFS probes whenever the host advertises XFS,
		# including captures whose workload filesystem is ext4.
		if grep -qw xfs /proc/filesystems; then
			local probe_status=0
			io_test_xfs_available "${TOOL_BIN}" "${TOOL_BPF}" "${device}" "${TOOL_WORK_DIR}/xfs-qualification" \
				|| probe_status=$?
			if ((probe_status == 77)); then
				skip "XFS name-based attachment is unavailable; inspect xfs-qualification"
			fi
			((probe_status == 0)) || fatal "XFS CLI startup failed; inspect xfs-qualification/xfs-cli.err"
		fi

		log_info "iotracing: filesystem=${filesystem}, duration=${DURATION}s, device=${device}, io_test_dir=${io_test_dir}"
		defer_interrupts
		setsid timeout --kill-after=5s "$((READY_TIMEOUT + DURATION))s" "${TOOL_BIN}" --bpf-path "${TOOL_BPF}" \
			--duration "${DURATION}" \
			--device "${device}" \
			--output json \
			> "${TOOL_OUT}" 2> "${TOOL_ERR}" &
		IO_PID=$!
		record_resources

		# The CLI confirms both event-channel creation and complete probe attachment.
		wait_until "${READY_TIMEOUT}" 1 iotracing_capture_is_ready \
			|| fatal "iotracing capture was not ready within ${READY_TIMEOUT} seconds; see ${TOOL_ERR}"

		# Keep writes active until capture ends without growing the private filesystem.
		defer_interrupts
		setsid timeout --preserve-status --kill-after=1s 30s bash -c '
			while :; do
				dd if=/dev/zero of="$1" bs=1M count=16 oflag=dsync conv=notrunc status=none || exit
			done
		' _ "${io_test_dir}/io" > "${TOOL_WORK_DIR}/dd.log" 2>&1 &
		WORKLOAD_PID=$!
		record_resources

		wait "${IO_PID}" || fatal "iotracing exited non-zero"
		IO_PID=""
		kill -0 "${WORKLOAD_PID}" 2> /dev/null || fatal "dd workload ended before capture completed"
		kill -TERM -- "-${WORKLOAD_PID}" 2> /dev/null || true
		workload_status=0
		wait "${WORKLOAD_PID}" || workload_status=$?
		wait_until 3 0.1 process_stopped "${WORKLOAD_PID}" \
			|| fatal "dd process group remains after cancellation; inspect resources.txt"
		WORKLOAD_PID=""
		record_resources
		# timeout implementations report TERM as either 15 or shell status 143.
		[[ ${workload_status} == 15 || ${workload_status} == 143 ]] \
			|| fatal "dd workload exited with ${workload_status}, expected TERM cancellation"
		assert_iotracing_cli_schema

		# Accuracy: a row attributed to dd holds the private file with non-zero
		# write bps. Captured paths are relative to the filesystem root.
		# comm may be "dd" (BPF capture) or "dd if=/dev/zero ..."
		# (cmdline fallback when /proc still has the entry); match the leading
		# token. Preserve the matched rows so a successful run shows which
		# attribution data satisfied the accuracy check.
		assert_iotracing_cli_writes

		log_info "matched dd-attributed writes:"
		echo "${MATCHED_IO}" | jq '.'

		# The successful matching workload makes this exclusion check non-vacuous.
		major=${device%:*}
		minor=${device#*:}
		assert_iotracing_cli_device

		# Buffered device reads exercise bdevfs attribution independently of files.
		# Evict only a tail page on our private loop so every read reaches block IO.
		local block_reader_pid block_read_status=0
		defer_interrupts
		setsid timeout --preserve-status --kill-after=1s 30s python3 - \
			"${loop_devices[first_loop]}" "${TOOL_WORK_DIR}/block-read.ready" \
			> "${TOOL_WORK_DIR}/block-read.log" 2>&1 <<- 'PY' &
				import os
				from pathlib import Path
				import sys
				import time

				device, ready = sys.argv[1:]
				fd = os.open(device, os.O_RDONLY | os.O_CLOEXEC)
				page_size = os.sysconf("SC_PAGE_SIZE")
				offset = (os.lseek(fd, 0, os.SEEK_END) // page_size - 1) * page_size
				published = False
				try:
				    while True:
				        os.posix_fadvise(fd, offset, page_size, os.POSIX_FADV_DONTNEED)
				        if len(os.pread(fd, page_size, offset)) != page_size:
				            raise OSError("short buffered block-device read")
				        if not published:
				            Path(ready).write_text(str(os.getpid()), encoding="ascii")
				            published = True
				        time.sleep(0.01)
				finally:
				    os.close(fd)
			PY
		WORKLOAD_PID=$!
		record_resources
		wait_until 5 0.1 test -s "${TOOL_WORK_DIR}/block-read.ready" \
			|| fatal "buffered block reader did not complete its first read; inspect block-read.log"
		block_reader_pid=$(< "${TOOL_WORK_DIR}/block-read.ready")
		[[ ${block_reader_pid} =~ ^[1-9][0-9]*$ ]] || fatal "buffered block reader published an invalid PID"
		defer_interrupts
		setsid timeout --kill-after=5s 20s "${TOOL_BIN}" --bpf-path "${TOOL_BPF}" \
			--duration 2 --device "${device}" --output json \
			> "${TOOL_WORK_DIR}/block-read.out" 2> "${TOOL_WORK_DIR}/block-read.err" &
		IO_PID=$!
		record_resources
		wait "${IO_PID}" || fatal "iotracing buffered block-read capture failed"
		IO_PID=""
		kill -0 "${block_reader_pid}" 2> /dev/null || fatal "buffered block reader ended before capture completed"
		kill -TERM -- "-${WORKLOAD_PID}" 2> /dev/null || true
		wait "${WORKLOAD_PID}" || block_read_status=$?
		wait_until 3 0.1 process_stopped "${WORKLOAD_PID}" \
			|| fatal "buffered block-reader process group remains; inspect resources.txt"
		WORKLOAD_PID=""
		record_resources
		[[ ${block_read_status} == 15 || ${block_read_status} == 143 ]] \
			|| fatal "buffered block reader exited with ${block_read_status}, expected TERM cancellation"
		assert_iotracing_cli_block_reads
		log_info "iotracing buffered block-device read retained pid=${block_reader_pid} device=${device}"

		# A valid unused device must be accepted, while malformed input must fail
		# before any capture. The largest valid device number is checked for absence.
		absent_device=4095:1048575
		[[ ! -e /sys/dev/block/${absent_device} ]] \
			|| fatal "empty-result fixture device unexpectedly exists: ${absent_device}"
		timeout --kill-after=5s 15s "${TOOL_BIN}" --bpf-path "${TOOL_BPF}" \
			--duration 1 --device "${absent_device}" --output json \
			> "${TOOL_WORK_DIR}/empty.json" 2> "${TOOL_WORK_DIR}/empty.err" \
			|| fatal "iotracing rejected a valid nonexistent device"
		assert_iotracing_cli_empty

		assert_invalid_args() {
			local name=$1 status=0
			shift
			timeout --kill-after=1s 5s "${TOOL_BIN}" --bpf-path "${TOOL_BPF}" "$@" \
				> "${TOOL_WORK_DIR}/invalid-${name}.out" \
				2> "${TOOL_WORK_DIR}/invalid-${name}.err" || status=$?
			[[ ${status} -eq 1 ]] || fatal "invalid ${name}: exit=${status}, expected 1"
			[[ -s ${TOOL_WORK_DIR}/invalid-${name}.err ]] \
				|| fatal "invalid ${name}: stderr did not explain the rejection"
		}
		assert_invalid_args duration-zero --duration 0
		assert_invalid_args duration-negative --duration -1
		assert_invalid_args threshold-zero --schedule-threshold 0
		assert_invalid_args threshold-negative --schedule-threshold -1
		assert_invalid_args output --output xml
		assert_invalid_args device-format --device 8:0:1
		assert_invalid_args device-major --device 4096:0
		assert_invalid_args device-minor --device 8:1048576
		assert_invalid_args device-count --device \
			'8:0,8:1,8:2,8:3,8:4,8:5,8:6,8:7,8:8,8:9,8:10,8:11,8:12,8:13,8:14,8:15,8:16'
		assert_iotracing_cli_output_conflict

		# Keep process identities stable across captures. Synchronous and Direct IO
		# files distinguish their flags while top-N checks trim real observations.
		cat > "${TOOL_WORK_DIR}/file-workload.py" <<- 'PY'
			import mmap
			import os
			from pathlib import Path
			import signal
			import sys
			import time

			directory, name, ready = sys.argv[1:]
			running = True


			def stop(signum, frame):
			    global running
			    running = False


			signal.signal(signal.SIGTERM, stop)
			signal.signal(signal.SIGINT, stop)
			files = [os.open(f"{directory}/{name}-{i}", os.O_CREAT | os.O_RDWR | os.O_SYNC, 0o600)
			         for i in range(3)]
			direct_fd = os.open(f"{directory}/{name}-direct", os.O_CREAT | os.O_RDWR | os.O_DIRECT, 0o600)
			aligned = mmap.mmap(-1, 4096, flags=mmap.MAP_PRIVATE | mmap.MAP_ANONYMOUS)
			deadline = time.monotonic() + 90
			try:
			    while running and time.monotonic() < deadline:
			        for i, fd in enumerate(files):
			            size = 4096 * (i + 1)
			            if os.pwrite(fd, b"x" * size, 0) != size:
			                raise RuntimeError("short synchronous write")
			        if os.pwrite(direct_fd, aligned, 0) != len(aligned):
			            raise RuntimeError("short Direct IO write")
			        Path(ready).touch()
			finally:
			    aligned.close()
			    os.close(direct_fd)
			    for fd in files:
			        os.close(fd)
		PY
		for name in first second; do
			defer_interrupts
			setsid python3 "${TOOL_WORK_DIR}/file-workload.py" "${io_test_dir}" "${name}" \
				"${TOOL_WORK_DIR}/${name}.ready" > "${TOOL_WORK_DIR}/${name}.log" 2>&1 &
			worker_pids+=("$!")
			record_resources
			wait_until 5 0.1 test -e "${TOOL_WORK_DIR}/${name}.ready" \
				|| fatal "${name} multi-file workload did not complete its first writes"
		done

		capture_cli() {
			local name=$1
			shift
			defer_interrupts
			setsid timeout --kill-after=5s 15s "${TOOL_BIN}" --bpf-path "${TOOL_BPF}" \
				--duration 2 --device "${device},${absent_device}" "$@" \
				> "${TOOL_WORK_DIR}/${name}.out" 2> "${TOOL_WORK_DIR}/${name}.err" &
			IO_PID=$!
			record_resources
			wait "${IO_PID}" || fatal "iotracing ${name} capture failed"
			IO_PID=""
			record_resources
			for pid in "${worker_pids[@]}"; do
				kill -0 "${pid}" 2> /dev/null || fatal "multi-file workload ${pid} stopped during ${name}"
			done
		}

		capture_cli top-full --output json --max-process 100 --max-files-per-process 100
		assert_iotracing_cli_writers

		assert_iotracing_cli_order

		# Kernel workers can also issue IO on the private device. Keep both known
		# writers visible when checking file trimming; their rank may change between
		# capture windows, so the process limit has its own check below.
		capture_cli top-files --output json --max-process 100 --max-files-per-process 1
		assert_iotracing_cli_file_limit

		capture_cli top-process --output json --max-process 1 --max-files-per-process 100
		assert_iotracing_cli_process_limit

		capture_cli text --output text --max-process 100 --max-files-per-process 3
		assert_iotracing_cli_text

		for pid in "${worker_pids[@]}"; do
			stop_and_wait_by_pid "${pid}" 5 || fatal "multi-file workload ${pid} did not stop cleanly"
		done
		worker_pids=()
		record_resources
		log_info "T01-T04 CLI output, filtering, process/file top-N and invalid parameters verified"
	}

	run_filesystem_tests "${filesystem}"
}
