#!/usr/bin/env bash

# Copyright 2026 The HuaTuo Authors.
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

# Verify iotracing autotracing with deterministic diskstats and the real
# iotracing/BPF subprocess. Public inputs isolate trigger conditions without
# relying on device performance or observing the internal sampling loop.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"
source "${ROOT_DIR}/integration/lib_io.sh"
io_test_isolate_run_directory

CURL_TIMEOUT=(--connect-timeout 2 --max-time 3)

require_commands jq python3

readonly IOTRACING_FIXTURE_ROOT="${HUATUO_BAMAI_TEST_TMPDIR}/iotracing-fixture"
readonly IOTRACING_DISKSTATS="${IOTRACING_FIXTURE_ROOT}/proc/diskstats"
readonly IOTRACING_EVENT="${HUATUO_BAMAI_TEST_TMPDIR}/events/iotracing"
readonly IOTRACING_RESULTS="${HUATUO_BAMAI_TEST_TMPDIR}/iotracing-scenarios"
readonly IOTRACING_DEADLINE=$((SECONDS + 90))
content_dir=""
content_pid=""
content_loop=""
content_device=""
content_results=""
content_inode=0
content_enabled=0
content_xfs_module_added=0
iotracing_completed=0

content_process_stopped() {
	! kill -0 "$1" 2> /dev/null
}

content_loop_detached() {
	! losetup "$1" > /dev/null 2>&1
}

stop_content_workload() {
	local attached loop
	if [[ -n "${content_pid}" ]]; then
		stop_by_pid "${content_pid}" 5
		wait_until 2 0.1 content_process_stopped "${content_pid}" \
			|| {
				log_error "content workload remains: pid=${content_pid}, directory=${content_dir}"
				return 1
			}
		wait "${content_pid}" 2> /dev/null || true
		content_pid=""
	fi
	if [[ -n "${content_dir}" ]]; then
		[[ ${content_dir} == "${io_test_storage_dir}/autotracing."* ]] \
			|| {
				log_error "refusing to remove unexpected content directory: ${content_dir}"
				return 1
			}
		if mountpoint -q "${content_dir}/mnt"; then
			if ! timeout --kill-after=1s 10s umount "${content_dir}/mnt"; then
				log_error "cannot unmount ${content_dir}/mnt; stop its users before reclaiming the content fixture"
				return 1
			fi
		fi
		# Recover ownership even if losetup was interrupted before returning its name.
		if ! attached=$(timeout --kill-after=1s 5s losetup -j "${content_dir}/disk.img" -n -O NAME); then
			log_error "cannot inspect content loop ownership: backing=${content_dir}/disk.img"
			return 1
		fi
		while IFS= read -r loop; do
			[[ -n ${loop} ]] || continue
			if ! timeout --kill-after=1s 10s losetup -d "${loop}" \
				|| ! wait_until 5 0.1 content_loop_detached "${loop}"; then
				log_error "content loop remains: ${loop}, backing=${content_dir}/disk.img; inspect users before losetup -d"
				return 1
			fi
		done <<< "${attached}"
		rm -rf -- "${content_dir}" || return 1
		printf 'content_cleanup=done\n' >> "${HUATUO_BAMAI_TEST_TMPDIR}/iotracing.resources"
		content_loop=""
		content_dir=""
	fi
	io_test_cleanup_disks || return 1
	if ((content_xfs_module_added)); then
		# Module state is global even though the mount belongs to this namespace.
		# rmmod never forces removal and leaves dependencies and busy users alone.
		if ! wait_until 10 0.1 timeout --kill-after=1s 2s bash -c \
			'[[ ! -d /sys/module/xfs ]] || rmmod xfs' > "${HUATUO_BAMAI_TEST_TMPDIR}/xfs-module-cleanup.log" 2>&1; then
			log_error "cannot restore initially absent XFS module; inspect xfs-module-cleanup.log and existing XFS users"
			return 1
		fi
		content_xfs_module_added=0
		printf 'xfs_module_cleanup=done\n' >> "${HUATUO_BAMAI_TEST_TMPDIR}/iotracing.resources"
	fi
}

start_content_workload() {
	local filesystem=$1 fixture_tool setup_status=0 signal_status=0 qualification_status=0
	local mkfs_args=()
	content_enabled=0
	content_results=$2
	mkdir -p "${content_results}"
	case ${filesystem} in
	ext4) mkfs_args=(-q -F) ;;
	xfs) mkfs_args=(-f) ;;
	*) fatal "unsupported content filesystem: ${filesystem}" ;;
	esac
	for fixture_tool in truncate losetup "mkfs.${filesystem}" mount umount mountpoint findmnt timeout; do
		if ! command -v "${fixture_tool}" > /dev/null 2>&1; then
			log_info "NOT_RUN A07 file content: private ${filesystem} fixture requires ${fixture_tool}"
			return
		fi
	done
	if ! grep -qw "${filesystem}" /proc/filesystems; then
		if ! command -v modprobe > /dev/null 2>&1 || ! modprobe -n -q "${filesystem}"; then
			log_info "NOT_RUN A07 file content: ${filesystem} is unavailable in the running kernel"
			return
		fi
	fi
	if [[ ${filesystem} == xfs ]]; then
		local format_options=()
		mapfile -t format_options < <(io_test_xfs_format_options)
		mkfs_args+=("${format_options[@]}")
	fi
	if [[ ${filesystem} == xfs && ! -d /sys/module/xfs ]]; then
		if ! command -v rmmod > /dev/null 2>&1; then
			log_info "NOT_RUN A07 file content: restoring XFS module state requires rmmod"
			stop_content_workload || fatal "cannot reclaim incomplete XFS fixture"
			return
		fi
		# Claim restoration before mount can autoload the module or be interrupted.
		content_xfs_module_added=1
		printf 'xfs_module_initially_loaded=0\n' >> "${HUATUO_BAMAI_TEST_TMPDIR}/iotracing.resources"
	fi
	io_test_prepare_storage "${filesystem}"
	# Preserve ownership if an interrupt arrives between creation and assignment.
	trap 'signal_status=130' INT
	trap 'signal_status=143' TERM
	content_dir=$(mktemp -d "${io_test_storage_dir}/autotracing.XXXXXX")
	printf 'content_filesystem=%s\ncontent_directory=%s\ncontent_backing=%s/disk.img\ncontent_mount=%s/mnt\n' \
		"${filesystem}" "${content_dir}" "${content_dir}" "${content_dir}" >> "${HUATUO_BAMAI_TEST_TMPDIR}/iotracing.resources"
	trap 'log_error "iotracing test interrupted by INT; remaining scenarios were not run"; exit 130' INT
	trap 'log_error "iotracing test interrupted by TERM or execution timeout; remaining scenarios were not run"; exit 143' TERM
	((signal_status == 0)) || exit "${signal_status}"
	mkdir "${content_dir}/mnt"
	truncate -s 1G "${content_dir}/disk.img"
	trap 'signal_status=130' INT
	trap 'signal_status=143' TERM
	timeout --kill-after=1s 10s losetup --find --show "${content_dir}/disk.img" \
		> "${content_results}/content-loop-device" \
		2> "${content_results}/content-setup.log" || setup_status=$?
	content_loop=$(< "${content_results}/content-loop-device")
	printf 'content_loop=%s\n' "${content_loop}" >> "${HUATUO_BAMAI_TEST_TMPDIR}/iotracing.resources"
	trap 'log_error "iotracing test interrupted by INT; remaining scenarios were not run"; exit 130' INT
	trap 'log_error "iotracing test interrupted by TERM or execution timeout; remaining scenarios were not run"; exit 143' TERM
	((signal_status == 0)) || exit "${signal_status}"
	if ((setup_status != 0)); then
		cat "${content_results}/content-setup.log" >&2
		log_info "NOT_RUN A07 file content: cannot attach private loop (exit=${setup_status})"
		stop_content_workload || fatal "cannot reclaim incomplete content loop fixture"
		return
	fi
	timeout --kill-after=1s 15s "mkfs.${filesystem}" "${mkfs_args[@]}" "${content_loop}" \
		>> "${content_results}/content-setup.log" 2>&1 \
		|| fatal "cannot format owned content loop ${content_loop}; inspect ${content_results}/content-setup.log"
	if ! timeout --kill-after=1s 10s mount -t "${filesystem}" "${content_loop}" "${content_dir}/mnt" \
		>> "${content_results}/content-setup.log" 2>&1; then
		cat "${content_results}/content-setup.log" >&2
		log_info "NOT_RUN A07 file content: cannot mount private ${filesystem} fixture"
		stop_content_workload || fatal "cannot reclaim unmounted content fixture"
		return
	fi
	findmnt -n -o SOURCE,FSTYPE,OPTIONS --target "${content_dir}/mnt" \
		> "${content_results}/content-filesystem.txt"
	[[ $(findmnt -n -o FSTYPE --target "${content_dir}/mnt") == "${filesystem}" &&
	$(findmnt -n -o SOURCE --target "${content_dir}/mnt") -ef ${content_loop} ]] \
		|| fatal "content mount does not match owned ${filesystem} loop ${content_loop}"
	content_device=$(< "/sys/class/block/${content_loop##*/}/dev")
	log_info "A07: private ${filesystem} loop=${content_loop} device=${content_device} mount=${content_dir}/mnt"
	if grep -qw xfs /proc/filesystems; then
		io_test_xfs_available "${IOTRACING_FIXTURE_ROOT}/bin/iotracing" \
			"${IOTRACING_FIXTURE_ROOT}/bpf/iotracing.o" "${content_device}" "${content_results}" \
			|| qualification_status=$?
		if ((qualification_status == 77)); then
			log_info "NOT_RUN A07 ${filesystem}: known XFS name-based attachment limitation; inspect ${content_results}/xfs-cli.err"
			stop_content_workload || fatal "cannot reclaim unavailable XFS content fixture"
			return
		fi
		((qualification_status == 0)) || fatal "XFS CLI startup failed; inspect ${content_results}/xfs-cli.err"
	fi

	trap 'signal_status=130' INT
	trap 'signal_status=143' TERM
	python3 "${ROOT_DIR}/integration/testdata/test_iotracing_workload.py" content \
		"${content_dir}/mnt/autotracing-content.dat" "${content_results}/content.ready" \
		> "${content_results}/content.log" 2>&1 &
	content_pid=$!
	printf 'content_pid=%s backing=%s\n' "${content_pid}" "${content_dir}/mnt/autotracing-content.dat" \
		>> "${HUATUO_BAMAI_TEST_TMPDIR}/iotracing.resources"
	trap 'log_error "iotracing test interrupted by INT; remaining scenarios were not run"; exit 130' INT
	trap 'log_error "iotracing test interrupted by TERM or execution timeout; remaining scenarios were not run"; exit 143' TERM
	((signal_status == 0)) || exit "${signal_status}"
	wait_until 5 0.1 test -s "${content_results}/content.ready" \
		|| fatal "known file workload did not complete its first synchronous write"
	content_inode=$(jq -er '.' "${content_results}/content.ready")
	content_enabled=1
}

record_content_progress() {
	# Observe from another process so the journal cannot add IO to the writer.
	python3 - "${content_pid}" "${content_inode}" "${content_results}/content-progress.jsonl" << 'PY'
import json
from pathlib import Path
import sys
import time

pid, inode, output = sys.argv[1:]
values = {}
for line in Path("/proc/{}/io".format(pid)).read_text().splitlines():
    name, value = line.split(":", 1)
    values[name] = int(value)
with Path(output).open("a") as journal:
    journal.write(json.dumps({"pid": int(pid), "inode": int(inode),
                              "wall_time": time.time(), "monotonic": time.monotonic(),
                              "io": values}) + "\n")
PY
}

# Keep failed startup bounded while leaving time to reclaim the fixture.
WAIT_HUATUO_BAMAI_TIMEOUT=20

stop_iotracing_case() {
	local executable pid status=0
	huatuo_bamai_stop || status=1
	for executable in /proc/[0-9]*/exe; do
		[[ "${executable}" -ef "${IOTRACING_FIXTURE_ROOT}/bin/iotracing" ]] || continue
		pid=${executable%/exe}
		pid=${pid##*/}
		stop_by_pid "${pid}" 2
		if ! wait_until 2 0.1 test ! "${executable}" -ef "${IOTRACING_FIXTURE_ROOT}/bin/iotracing"; then
			log_error "iotracing cleanup failed: pid=${pid}; stop this test-owned process before reusing the environment"
			return 1
		fi
	done
	return "${status}"
}

cleanup_iotracing() {
	local status=$?
	trap - EXIT
	trap '' INT TERM
	stop_iotracing_case || status=1
	stop_content_workload || status=1
	io_test_cleanup_disks || status=1
	exit "${status}"
}

check_execution_budget() {
	((SECONDS < IOTRACING_DEADLINE)) && return 0
	log_error "iotracing execution budget exhausted; remaining scenarios were not run; entering cleanup"
	exit 124
}

trap cleanup_iotracing EXIT
trap 'log_error "iotracing test interrupted by INT; remaining scenarios were not run"; exit 130' INT
trap 'log_error "iotracing test interrupted by TERM or execution timeout; remaining scenarios were not run"; exit 143' TERM

mkdir -p \
	"${IOTRACING_FIXTURE_ROOT}/bin" \
	"${IOTRACING_FIXTURE_ROOT}/bpf" \
	"${IOTRACING_FIXTURE_ROOT}/proc" \
	"${IOTRACING_FIXTURE_ROOT}/sys/dev/block/8:0" \
	"${IOTRACING_FIXTURE_ROOT}/dev"

cp "${HUATUO_BAMAI_BIN}" "${IOTRACING_FIXTURE_ROOT}/bin/huatuo-bamai"
cp "${ROOT_DIR}/_output/bin/iotracing" "${IOTRACING_FIXTURE_ROOT}/bin/iotracing"
cp "${ROOT_DIR}/_output/bpf/iotracing.o" "${IOTRACING_FIXTURE_ROOT}/bpf/iotracing.o"

# Counters advance together; atomic replacement prevents a transient empty
# diskstats read from breaking the two consecutive valid intervals.
write_diskstats() {
	local tick=$1
	printf '%s 0 %s %s 0 %s %s %s 0 %s %s 0 %s %s 0 0 0 0 0 0 0\n' \
		"${device_major}" "${device}" \
		"$((100 + tick * 100))" "$((100 + tick * read_sectors))" "$((100 + tick * read_ticks))" \
		"$((100 + tick * 100))" "$((100 + tick * write_sectors))" "$((100 + tick * write_ticks))" \
		"$((100 + tick * busy_ticks))" "$((100 + tick * busy_ticks))" \
		> "${IOTRACING_DISKSTATS}.next"
	mv "${IOTRACING_DISKSTATS}.next" "${IOTRACING_DISKSTATS}"
}

iotracing_event_count() {
	[[ -s "${IOTRACING_EVENT}" ]] || {
		echo 0
		return
	}
	# Local storage is a stream of JSON objects, not necessarily JSON lines.
	jq -s 'length' "${IOTRACING_EVENT}" 2> /dev/null || echo 0
}

assert_iotracing_single_capture() {
	local executable active=0
	for executable in /proc/[0-9]*/exe; do
		[[ "${executable}" -ef "${IOTRACING_FIXTURE_ROOT}/bin/iotracing" ]] || continue
		active=$((active + 1))
	done
	((active <= 1)) || fatal "${scenario}: observed ${active} concurrent iotracing subprocesses"
}

write_config_bamai_iotracing_case() {
	write_config_bamai_iotracing_autotracing | sed -e "s/RunTracingToolTimeout = 1$/RunTracingToolTimeout = ${capture_duration}/" \
		-e "s/MaxProcDump = 1$/MaxProcDump = ${max_process}/" \
		-e "s/MaxFilesPerProcDump = 1$/MaxFilesPerProcDump = ${max_files}/"
}

assert_iotracing_events() {
	[[ -s "${IOTRACING_EVENT}" ]] || fatal "${scenario}: no diagnostic within ${budget}s"
	# Busy time advances by 200 ms per roughly one-second update. The broad
	# band allows input-update jitter.
	jq -e -s --arg reason "${expected_reason}" --arg device "${device}" \
		--argjson major "${device_major}" --argjson count "${expected_count}" \
		--argjson maximum "${max_process}" --argjson files "${max_files}" \
		--argjson duration "${capture_duration}" '
		def epoch: sub("\\.[0-9]+Z$"; "Z") | fromdateiso8601;
		length >= $count
		and all(.[];
			.tracer_name == "iotracing"
			and .tracer_type == "autotracing"
			and (.tracer_id | type == "string" and length > 0)
			and .tracer_data.reason_snapshot.type == $reason
			and .tracer_data.reason_snapshot.device == $device
			and .tracer_data.reason_snapshot.major_num == $major
			and .tracer_data.reason_snapshot.minor_num == 0
			and ((.uploaded_timestamp | epoch) - (.started_timestamp | epoch)) >= ($duration - 1)
			and (.tracer_data.failure_reason // "") == ""
			and (.tracer_data.process_file_io_stats | type == "array")
			and (.tracer_data.process_file_io_stats | length) <= $maximum
			and all(.tracer_data.process_file_io_stats[]; (.total_files | length) <= $files)
			and (.tracer_data.io_schedule_timeout_stacks | type == "array" or . == null)
			and (.tracer_data.reason_snapshot.iostatus |
				.io_util >= 10 and .io_util <= 30
				and .read_await == 2000 and .write_await == 2000))
	' "${IOTRACING_EVENT}" > /dev/null \
		|| fatal "${scenario}: unexpected diagnostic type, target, snapshot, or output limits"
}

assert_iotracing_file_content() {
	jq -e -s --argjson pid "${content_pid}" --argjson inode "${content_inode}" \
		--argjson major "${content_device%:*}" --argjson minor "${content_device#*:}" '
		all(.[];
			any(.tracer_data.process_file_io_stats[]; .pid == $pid
				and .total_fs_write_bps > 0 and .total_disk_write_bps > 0
				and any(.total_files[];
					.inode == $inode and .major == $major and .minor == $minor
					and (.path | endswith("/autotracing-content.dat"))
					and .disk_write_bps > 0 and .is_direct == false)))
	' "${IOTRACING_EVENT}" > /dev/null \
		|| fatal "automatic diagnostic lacks the synchronous file, its IO or correct is_direct flag"
}

run_iotracing_case() {
	local scenario=$1 device=vda device_major=8 expected_reason=ioutil expected_count=1
	local read_sectors=8388608 write_sectors=8388608 read_ticks=200000 write_ticks=200000 busy_ticks=200
	local budget=25 tick=0 pid count=0
	local capture_duration=1 max_process=100 max_files=100
	local result_dir="${IOTRACING_RESULTS}/${scenario}"

	start_content_workload "${scenario##*_}" "${result_dir}"
	((content_enabled)) || return 0
	mkdir -p "${result_dir}"
	rm -f "${IOTRACING_EVENT}"
	write_diskstats 0
	check_execution_budget
	log_info "${scenario}: expected=${expected_reason} events=${expected_count} device=${device} budget=${budget}s"
	io_test_start_bamai \
		write_config_bamai_iotracing_case \
		--region dev \
		--procfs-prefix "${IOTRACING_FIXTURE_ROOT}" \
		--disable-kubelet \
		--disable-tracing io_health,irqtracing,tcp_retransmit \
		--log-debug
	pid=$(< "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")
	iotracing_case_event_ready() {
		check_execution_budget
		kill -0 "${pid}" 2> /dev/null || fatal "${scenario}: daemon exited before observation completed"
		assert_iotracing_single_capture
		kill -0 "${content_pid}" 2> /dev/null || fatal "known file workload exited before automatic capture completed"
		record_content_progress
		tick=$((tick + 1))
		write_diskstats "${tick}"
		count=$(iotracing_event_count)
		((count >= expected_count))
	}
	wait_until "${budget}" 1 iotracing_case_event_ready > "${result_dir}/event-wait.log" \
		|| fatal "${scenario}: event wait timed out; expected=${expected_count} actual=${count}; inspect ${result_dir}/event-wait.log"
	stop_iotracing_case || fatal "${scenario}: cleanup failed"
	assert_iotracing_events
	cp "${IOTRACING_EVENT}" "${result_dir}/events.json"
	assert_iotracing_file_content

	log_info "A07 ${scenario##*_}: automatic capture saved the known writing process and file"
	stop_content_workload || fatal "cannot reclaim automatic-capture content workload"
	cp "${IOTRACING_DISKSTATS}" "${result_dir}/diskstats"
	cp "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" "${result_dir}/bamai.conf"
	cp "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${result_dir}/huatuo.log"
	iotracing_completed=$((iotracing_completed + 1))
	log_info "PASS: ${scenario}"
}

# Running a copied daemon makes its executable and BPF directories test-owned.
HUATUO_BAMAI_BIN="${IOTRACING_FIXTURE_ROOT}/bin/huatuo-bamai"
for scenario in ioutil_ext4 ioutil_xfs; do
	run_iotracing_case "${scenario}"
done
((iotracing_completed > 0)) || skip "private ext4 and XFS fixtures could not be established; inspect setup logs"
