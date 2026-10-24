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
# shows up under per-file attribution. Private filesystems keep unrelated
# host IO out of the bounded capture and provide an excluded-device fixture.
# Real BPF contracts additionally cover accounting, metadata, capacity,
# device filters and complete object loading and attach.
# Path-validation rules are covered by internal/bpf TestValidateName.

iotracing_filesystem_test() {
	source "${ROOT_DIR}/integration/lib.sh"

	require_commands go truncate losetup mount umount mountpoint grep
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

	cleanup() {
		local status=$? index
		trap - EXIT
		[[ -n "${IO_PID}" ]] && stop_and_wait_by_pid "${IO_PID}" 5 || true
		[[ -n "${WORKLOAD_PID}" ]] && stop_and_wait_by_pid "${WORKLOAD_PID}" 5 || true
		for ((index = ${#loop_devices[@]} - 1; index >= 0; index--)); do
			if mountpoint -q "${test_dirs[index]}"; then
				umount "${test_dirs[index]}" || status=1
			fi
			# Busy loop devices are marked autoclear until their last user exits.
			losetup -d "${loop_devices[index]}" || status=1
		done
		exit "${status}"
	}
	trap cleanup EXIT

	iotracing_capture_is_ready() {
		kill -0 "${IO_PID}" 2> /dev/null \
			|| fatal "iotracing exited before capture was ready; see ${TOOL_ERR}"
		grep -Fxq 'iotracing: capture ready' "${TOOL_ERR}"
	}

	run_filesystem_tests() {
		local filesystem=$1 image_size=64M format_flag=-F
		local io_test_dir="${TOOL_WORK_DIR}/${filesystem}-mnt"
		local other_test_dir="${TOOL_WORK_DIR}/${filesystem}-other"
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
		test_dirs+=("${io_test_dir}" "${other_test_dir}")

		# The directory alone does not isolate a global probe on the host root device.
		for test_dir in "${io_test_dir}" "${other_test_dir}"; do
			truncate -s "${image_size}" "${test_dir}.img"
			loop_device=$(losetup --find --show "${test_dir}.img")
			loop_devices+=("${loop_device}")
			"mkfs.${filesystem}" -q "${format_flag}" "${format_options[@]}" "${loop_device}"
			mkdir "${test_dir}"
			mount "${loop_device}" "${test_dir}"
		done
		device=$(cat "/sys/class/block/${loop_devices[first_loop]##*/}/dev")

		# Qualify attach before requiring CLI attribution from this filesystem.
		# A Go skip exits zero, so propagate unavailable XFS coverage explicitly.
		mkdir -p "${TOOL_WORK_DIR}/go-tmp"
		local runtime_log="${TOOL_WORK_DIR}/runtime.json" runtime_status=0
		# A lazily faulted Go test executable must not reside on the captured device.
		GOTMPDIR="${TOOL_WORK_DIR}/go-tmp" TMPDIR="${io_test_dir}" \
			TEST_IOTRACING_OTHER_FS="${other_test_dir}" TEST_IOTRACING_FILE_BPF="${TOOL_BPF}" \
			timeout --kill-after=5s 240s go test -mod=vendor -tags=integration ./cmd/iotracing \
			-run '^TestIOTracing' -count=1 -json -timeout=230s > "${runtime_log}" || runtime_status=$?
		jq -rj 'select(.Action == "output") | .Output' "${runtime_log}" \
			|| fatal "invalid iotracing runtime test results"
		[[ ${runtime_status} -eq 0 ]] || fatal "iotracing runtime tests failed with ${runtime_status}"
		if jq -e -s 'any(.[]; .Action == "skip" and .Test == "TestIOTracingFullObject")' "${runtime_log}" > /dev/null; then
			skip "XFS name-based probe attachment is unavailable; full-object qualification was not completed"
		fi

		log_info "iotracing: filesystem=${filesystem}, duration=${DURATION}s, device=${device}, io_test_dir=${io_test_dir}"
		timeout --kill-after=5s "$((READY_TIMEOUT + DURATION))s" "${TOOL_BIN}" --bpf-path "${TOOL_BPF}" \
			--duration "${DURATION}" \
			--device "${device}" \
			--output json \
			> "${TOOL_OUT}" 2> "${TOOL_ERR}" &
		IO_PID=$!

		# The CLI confirms both event-channel creation and complete probe attachment.
		wait_until "${READY_TIMEOUT}" 1 iotracing_capture_is_ready \
			|| fatal "iotracing capture was not ready within ${READY_TIMEOUT} seconds; see ${TOOL_ERR}"

		# Keep the sustained load within the private filesystem's bounded capacity.
		timeout --kill-after=1s "$((DURATION - 2))" bash -c '
		while :; do
			dd if=/dev/zero of="$1" bs=1M count=16 oflag=dsync conv=notrunc status=none || exit
		done
	' _ "${io_test_dir}/io" > "${TOOL_WORK_DIR}/dd.log" 2>&1 &
		WORKLOAD_PID=$!

		wait "${IO_PID}" || fatal "iotracing exited non-zero"
		IO_PID=""
		workload_status=0
		wait "${WORKLOAD_PID}" || workload_status=$?
		WORKLOAD_PID=""
		[[ ${workload_status} -eq 124 ]] || fatal "dd workload exited with ${workload_status}, expected timeout (124)"
		[[ -s ${TOOL_OUT} ]] || fatal "iotracing produced empty output"

		# Envelope: parses as JSON, both top-level keys exist as arrays.
		jq -e '
		(.process_file_io_stats | type == "array") and
		(.io_schedule_timeout_stacks | type == "array")
	' "${TOOL_OUT}" > /dev/null \
			|| fatal "iotracing JSON schema invalid"

		# Accuracy: a row attributed to dd holds the private file with non-zero
		# write bps. Captured paths are relative to the filesystem root.
		# comm may be "dd" (BPF capture) or "dd if=/dev/zero ..."
		# (cmdline fallback when /proc still has the entry); match the leading
		# token. Preserve the matched rows so a successful run shows which
		# attribution data satisfied the accuracy check.
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

		log_info "matched dd-attributed writes:"
		echo "${MATCHED_IO}" | jq '.'
	}

	run_filesystem_tests "${filesystem}"
}
