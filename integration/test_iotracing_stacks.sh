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

# T06 observes a task blocked in IO through procfs, then checks the real CLI
# snapshot. An owned scsi_debug disk provides delayed reads; its configured
# delay is not used as an exact expectation for any reported duration.
set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_io.sh"
require_commands blockdev dd timeout setsid jq awk grep modprobe modinfo stat udevadm
bpf_tool_setup iotracing iotracing iotracing-stacks

if grep -qw xfs /proc/filesystems; then
	stacks_probe_status=0
	io_test_xfs_available "${TOOL_BIN}" "${TOOL_BPF}" 4095:1048575 "${TOOL_WORK_DIR}/xfs-qualification" || stacks_probe_status=$?
	if ((stacks_probe_status == 77)); then skip "XFS name-based attachment is unavailable; inspect xfs-qualification"; fi
	((stacks_probe_status == 0)) || fatal "CLI startup failed; inspect xfs-qualification/xfs-cli.err"
fi

if ! cat "/proc/$$/stack" > "${TOOL_WORK_DIR}/proc-stack-check.txt" 2>&1; then
	skip "T06 BLOCKED: task kernel stacks are not readable through procfs"
fi

readonly STACKS_DURATION=2
readonly STACKS_THRESHOLD_MS=20
readonly STACKS_DELAY_NS=100000000
readonly STACKS_SCSI_CONTROLS=/sys/bus/pseudo/drivers/scsi_debug
stacks_disk=""
stacks_device=""
stacks_tracer_pid=""
stacks_workload_pid=""
stacks_signal_status=0
stacks_resources="${TOOL_WORK_DIR}/resources.txt"

defer_interrupts() {
	trap 'stacks_signal_status=130' INT
	trap 'stacks_signal_status=143' TERM
}

record_resources() {
	printf 'disk=%s\nscsi_owner=%s\nndelay_ns=%s\ntracer=%s\nworkload=%s\n' \
		"${stacks_device}" "${io_test_scsi_owner}" "${STACKS_DELAY_NS}" \
		"${stacks_tracer_pid}" "${stacks_workload_pid}" > "${stacks_resources}"
	trap 'stacks_signal_status=130; exit 130' INT
	trap 'stacks_signal_status=143; exit 143' TERM
	((stacks_signal_status == 0)) || exit "${stacks_signal_status}"
}

process_stopped() {
	! kill -0 -- "-$1" 2> /dev/null
}

cleanup() {
	local status=$? cleanup_failed=0 pid
	((stacks_signal_status == 0)) || status=${stacks_signal_status}
	trap - EXIT
	trap '' INT TERM
	set +e
	for pid in "${stacks_workload_pid}" "${stacks_tracer_pid}"; do
		[[ -n ${pid} ]] || continue
		kill -TERM -- "-${pid}" 2> /dev/null
		stop_by_pid "${pid}" 5
		kill -KILL -- "-${pid}" 2> /dev/null
		if wait_until 2 0.1 process_stopped "${pid}"; then
			wait "${pid}" 2> /dev/null
		else
			log_error "T06 cleanup: process group ${pid} remains; stop it before releasing ${stacks_device}"
			cleanup_failed=1
		fi
	done
	if ((cleanup_failed == 0)); then
		io_test_cleanup_disks || cleanup_failed=1
	else
		log_error "T06 cleanup: retaining owned SCSI disk ${stacks_device}, owner=${io_test_scsi_owner}; stop recorded processes before recovery"
	fi
	printf 'cleanup_failed=%s\nexecution_status=%s\n' "${cleanup_failed}" "${status}" >> "${stacks_resources}"
	((cleanup_failed == 0)) || status=1
	exit "${status}"
}
trap cleanup EXIT
record_resources

stacks_set_completion_delay() {
	assert_io_test_disk_identity "${stacks_disk}" || fatal "T06 SCSI disk ownership changed while setting its delay"
	printf '%s\n' "${STACKS_DELAY_NS}" 2>> "${TOOL_WORK_DIR}/delay-setting.log" > "${STACKS_SCSI_CONTROLS}/ndelay"
}

# The delay control affects the whole module, so use only a newly created,
# exclusively locked scsi_debug instance. Unloading it releases the control too.
io_test_prepare_scsi_disks 1
((io_test_scsi_owned && ${#io_test_disks[@]} == 1)) \
	|| skip "T06 BLOCKED: an owned scsi_debug disk is required; existing disks will not be changed"
stacks_disk=${io_test_disks[0]}
stacks_device="/dev/${stacks_disk}"
record_resources
# Discovery commands can remain briefly after udev settles.
timeout --kill-after=1s 10s udevadm settle --timeout=8 || fatal "T06 udev did not settle before setting the SCSI delay"
assert_io_test_disk_identity "${stacks_disk}" || fatal "T06 SCSI disk ownership changed before setting its delay"
[[ -w ${STACKS_SCSI_CONTROLS}/ndelay ]] || skip "T06 BLOCKED: scsi_debug ndelay control is unavailable"
wait_until 2 0.1 stacks_set_completion_delay || fatal "T06 cannot set scsi_debug completion delay; inspect delay-setting.log"
[[ $(< "${STACKS_SCSI_CONTROLS}/ndelay") == "${STACKS_DELAY_NS}" ]] \
	|| fatal "T06 scsi_debug completion delay differs from ${STACKS_DELAY_NS} ns"
# Prevent readahead from serving subsequent reads without a delayed request.
timeout --kill-after=1s 5s blockdev --setra 0 "${stacks_device}" \
	|| fatal "T06 cannot disable readahead on its owned SCSI disk"

iotracing_task_blocked_in_io() {
	local pid=$1 evidence=$2
	cat "/proc/${pid}/status" > "${evidence}.status" 2> "${evidence}.error" || return 1
	grep -q '^State:.*D' "${evidence}.status" || return 1
	cat "/proc/${pid}/stack" > "${evidence}.stack" 2>> "${evidence}.error" || return 1
	# This PID performs only reads from the delayed device. D state
	# confirms a blocked workload; stack symbols vary with kernel inlining.
	[[ -s ${evidence}.stack ]]
}

assert_iotracing_stacks() {
	local name=$1 maximum=$2 target_pid=$3 started=$4 finished=$5
	local output="${TOOL_WORK_DIR}/${name}.json"
	# Public durations have microsecond precision, including values close to
	# the threshold; the delayed target is checked strictly above it below.
	jq -e --argjson maximum "${maximum}" --argjson threshold "${STACKS_THRESHOLD_MS}" '
		(.io_schedule_timeout_stacks | type == "array" and length > 0 and length <= $maximum) and
		all(.io_schedule_timeout_stacks[];
			(.pid | type == "number" and . > 0 and floor == .) and
			(.tid | type == "number" and . > 0 and floor == .) and
			(.cpu | type == "number" and . >= 0 and floor == .) and
			(.comm | type == "string" and length > 0) and
			(.schedule_latency_us | type == "number" and . >= ($threshold * 1000)) and
			(.stack | type == "array" and length > 0) and
			all(.stack[]; type == "string" and length > 0))
	' "${output}" > /dev/null || fatal "T06 ${name}: stack fields, threshold or nonempty entry limit violated"

	# A small global ring may retain another task. Attribution is checked in
	# the larger capture; saturation makes missing target attribution ambiguous.
	if ((maximum > 1)); then
		if ! jq -e --argjson pid "${target_pid}" \
			'any(.io_schedule_timeout_stacks[]; .pid == $pid and .tid == $pid)' "${output}" > /dev/null; then
			if jq -e --argjson maximum "${maximum}" \
				'.io_schedule_timeout_stacks | length == $maximum' "${output}" > /dev/null; then
				skip "T06 BLOCKED: global stack buffer filled without target ${target_pid}; unrelated tasks may have displaced it"
			fi
			fatal "T06 confirmed IO-blocked task ${target_pid} is missing from the unsaturated snapshot"
		fi
		jq -e --argjson pid "${target_pid}" --argjson threshold "${STACKS_THRESHOLD_MS}" \
			--argjson started "${started}" --argjson finished "${finished}" '
			all(.io_schedule_timeout_stacks[] | select(.pid == $pid);
				.tid == $pid and (.comm | test("^dd( |$)")) and
				.schedule_latency_us > ($threshold * 1000) and
				.schedule_latency_us <= (($finished - $started + 1) * 1000000))
		' "${output}" > /dev/null || fatal "T06 target task identity or microsecond duration is inconsistent"
	fi
	log_info "T06 ${name}: confirmed blocked read task=${target_pid}, max-stack=${maximum}"
}

assert_iotracing_stacks_workload_exit() {
	local name=$1 status=$2
	[[ ${status} == 0 || ${status} == 15 || ${status} == 143 ]] \
		|| fatal "T06 ${name}: sustained read workload exit=${status}, expected 0, 15 or 143"
}

assert_iotracing_stacks_capture_completed() {
	local name=$1 status=0
	wait "${stacks_tracer_pid}" || status=$?
	((status == 0)) || fatal "T06 ${name}: iotracing capture exit=${status}, expected 0; inspect ${name}.err"
}

capture_stacks() {
	local name=$1 maximum=$2 target_pid started finished observed=0 workload_status=0
	local output="${TOOL_WORK_DIR}/${name}.json"
	# Clear only the private device cache before each independent capture.
	assert_io_test_disk_identity "${stacks_disk}" || fatal "T06 SCSI disk ownership changed before capture"
	timeout --kill-after=1s 5s blockdev --flushbufs "${stacks_device}" \
		|| fatal "T06 cannot clear its delayed device cache"
	read -r started _ < /proc/uptime
	defer_interrupts
	# Keep one reader alive for the complete capture, including slow attachment.
	# Its private delayed device cannot reach EOF within this bounded run.
	setsid timeout --preserve-status --kill-after=2s 30s bash -c '
		printf "%s\n" "$$" > "$1"
		exec dd if="$2" of=/dev/null bs=4096 status=none
	' _ "${TOOL_WORK_DIR}/${name}.pid" "${stacks_device}" \
		> "${TOOL_WORK_DIR}/${name}.dd.log" 2>&1 &
	stacks_workload_pid=$!
	record_resources
	wait_until 2 0.1 test -s "${TOOL_WORK_DIR}/${name}.pid" \
		|| fatal "T06 workload did not publish its task identity"
	target_pid=$(< "${TOOL_WORK_DIR}/${name}.pid")
	printf 'task=%s\nstarted_uptime=%s\n' "${target_pid}" "${started}" \
		> "${TOOL_WORK_DIR}/${name}.evidence"

	defer_interrupts
	setsid timeout --kill-after=5s 20s "${TOOL_BIN}" \
		--bpf-path "${TOOL_BPF}" --duration "${STACKS_DURATION}" \
		--schedule-threshold "${STACKS_THRESHOLD_MS}" --max-stack "${maximum}" --output json \
		> "${output}" 2> "${TOOL_WORK_DIR}/${name}.err" &
	stacks_tracer_pid=$!
	record_resources
	if wait_until 4 0.1 iotracing_task_blocked_in_io "${target_pid}" "${TOOL_WORK_DIR}/${name}"; then
		observed=1
	fi
	assert_iotracing_stacks_capture_completed "${name}"
	stacks_tracer_pid=""
	kill -0 "${target_pid}" 2> /dev/null || fatal "T06 reader stopped before capture completed"
	kill -TERM -- "-${stacks_workload_pid}" 2> /dev/null || true
	wait "${stacks_workload_pid}" || workload_status=$?
	stacks_workload_pid=""
	read -r finished _ < /proc/uptime
	printf 'finished_uptime=%s\nworkload_status=%s\nblocked_read_observed=%s\n' \
		"${finished}" "${workload_status}" "${observed}" >> "${TOOL_WORK_DIR}/${name}.evidence"
	record_resources
	assert_iotracing_stacks_workload_exit "${name}" "${workload_status}"
	((observed)) || skip "T06 BLOCKED: procfs did not confirm an IO-blocked reader during capture"

	assert_iotracing_stacks "${name}" "${maximum}" "${target_pid}" "${started}" "${finished}"
}

capture_stacks attribution 8
capture_stacks limited 1
