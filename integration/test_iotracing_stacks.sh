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
# snapshot. A private delayed device provides the workload; its configured
# delay is not used as an exact expectation for any reported duration.
set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_io.sh"
require_commands blockdev dd dmsetup losetup truncate timeout setsid jq awk grep
bpf_tool_setup iotracing iotracing iotracing-stacks

if grep -qw xfs /proc/filesystems; then
	probe_status=0
	io_test_xfs_available "${TOOL_BIN}" "${TOOL_BPF}" 4095:1048575 "${TOOL_WORK_DIR}/xfs-qualification" || probe_status=$?
	if ((probe_status == 77)); then skip "XFS name-based attachment is unavailable; inspect xfs-qualification"; fi
	((probe_status == 0)) || fatal "CLI startup failed; inspect xfs-qualification/xfs-cli.err"
fi

if ! timeout --kill-after=1s 5s dmsetup targets > "${TOOL_WORK_DIR}/dm-targets.txt" 2>&1; then
	skip "T06 BLOCKED: device-mapper targets cannot be queried"
fi
if ! awk '$1 == "delay" { found = 1 } END { exit !found }' "${TOOL_WORK_DIR}/dm-targets.txt"; then
	skip "T06 BLOCKED: enable the device-mapper delay target before running this case"
fi
if ! cat "/proc/$$/stack" > "${TOOL_WORK_DIR}/proc-stack-check.txt" 2>&1; then
	skip "T06 BLOCKED: task kernel stacks are not readable through procfs"
fi

readonly DURATION=8
readonly THRESHOLD_MS=20
backing="${TOOL_WORK_DIR}/disk.img"
loop_device=""
dm_name="huatuo-iotracing-stacks-$$"
dm_uuid="HUATUO-IOTRACING-STACKS-$(< /proc/sys/kernel/random/uuid)"
dm_attempted=0
tracer_pid=""
workload_pid=""
signal_status=0
resources="${TOOL_WORK_DIR}/resources.txt"

defer_interrupts() {
	trap 'signal_status=130' INT
	trap 'signal_status=143' TERM
}

record_resources() {
	printf 'backing=%s\nloop=%s\ndm=%s\nuuid=%s\ntracer=%s\nworkload=%s\n' \
		"${backing}" "${loop_device}" "${dm_name}" "${dm_uuid}" \
		"${tracer_pid}" "${workload_pid}" > "${resources}"
	trap 'signal_status=130; exit 130' INT
	trap 'signal_status=143; exit 143' TERM
	((signal_status == 0)) || exit "${signal_status}"
}

process_stopped() {
	! kill -0 -- "-$1" 2> /dev/null
}

loop_detached() {
	! losetup "$1" > /dev/null 2>&1
}

cleanup() {
	local status=$? cleanup_failed=0 pid uuid attached names
	((signal_status == 0)) || status=${signal_status}
	trap - EXIT
	trap '' INT TERM
	set +e
	for pid in "${workload_pid}" "${tracer_pid}"; do
		[[ -n ${pid} ]] || continue
		kill -TERM -- "-${pid}" 2> /dev/null
		stop_by_pid "${pid}" 5
		kill -KILL -- "-${pid}" 2> /dev/null
		if wait_until 2 0.1 process_stopped "${pid}"; then
			wait "${pid}" 2> /dev/null
		else
			log_error "T06 cleanup: process group ${pid} remains; stop it before removing ${dm_name}"
			cleanup_failed=1
		fi
	done
	if ((dm_attempted)); then
		if uuid=$(timeout --kill-after=1s 5s dmsetup info -c --noheadings -o uuid "${dm_name}" 2> /dev/null); then
			if [[ ${uuid//[[:space:]]/} != "${dm_uuid}" ]]; then
				log_error "T06 cleanup: ${dm_name} ownership changed; expected UUID ${dm_uuid}, refusing removal"
				cleanup_failed=1
			elif ! timeout --kill-after=1s 30s bash -c 'while ! dmsetup remove "$1"; do sleep 1; done' _ "${dm_name}"; then
				log_error "T06 cleanup: ${dm_name} UUID ${dm_uuid} remains; stop its users, then dmsetup remove ${dm_name}"
				cleanup_failed=1
			fi
		elif ! names=$(timeout --kill-after=1s 5s dmsetup info -c --noheadings -o name) \
			|| awk -v name="${dm_name}" '$1 == name { found = 1 } END { exit !found }' <<< "${names}"; then
			log_error "T06 cleanup: cannot verify ${dm_name}; inspect its UUID and users before removal"
			cleanup_failed=1
		fi
	fi
	# Recover the loop association even if creation was interrupted before
	# losetup returned its name. Only this test's backing file is eligible.
	if attached=$(timeout --kill-after=1s 5s losetup -j "${backing}" -n -O NAME); then
		while IFS= read -r loop_device; do
			[[ -n ${loop_device} ]] || continue
			if ((cleanup_failed)) \
				|| ! timeout --kill-after=1s 5s losetup -d "${loop_device}" \
				|| ! wait_until 5 1 loop_detached "${loop_device}"; then
				log_error "T06 cleanup: retaining ${loop_device}, backing=${backing}; remove owned DM users before losetup -d"
				cleanup_failed=1
			fi
		done <<< "${attached}"
	else
		log_error "T06 cleanup: loop association state is unknown for ${backing}"
		cleanup_failed=1
	fi
	printf 'cleanup_failed=%s\nexecution_status=%s\n' "${cleanup_failed}" "${status}" >> "${resources}"
	((cleanup_failed == 0)) || status=1
	exit "${status}"
}
trap cleanup EXIT
record_resources

truncate -s 64M "${backing}"
defer_interrupts
timeout --kill-after=1s 10s losetup --read-only --find --show "${backing}" \
	> "${TOOL_WORK_DIR}/loop-device" || fatal "T06 cannot create its read-only loop device"
loop_device=$(< "${TOOL_WORK_DIR}/loop-device")
record_resources
# Record the UUID before creation, including its possible udev wait timeout.
dm_attempted=1
defer_interrupts
timeout --kill-after=1s 10s dmsetup create "${dm_name}" --uuid "${dm_uuid}" \
	--readonly --table "0 131072 delay ${loop_device} 0 100" \
	|| fatal "T06 cannot create its delayed device"
record_resources
# udev probes the delayed device before publishing its mapper link. Its reads
# incur the same delay as the workload and may outlive a short creation poll.
wait_until 30 1 test -b "/dev/mapper/${dm_name}" \
	|| fatal "T06 delayed device node did not appear"
timeout --kill-after=1s 5s dmsetup table "${dm_name}" > "${TOOL_WORK_DIR}/dm-table.txt"
# Buffered reads also work when direct IO is rejected by the delayed target.
# Disable readahead only on this private device so each read can block in IO.
timeout --kill-after=1s 5s blockdev --setra 0 "/dev/mapper/${dm_name}" \
	|| fatal "T06 cannot disable readahead on its delayed device"

assert_task_blocked_in_io() {
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
	jq -e --argjson maximum "${maximum}" --argjson threshold "${THRESHOLD_MS}" '
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
		jq -e --argjson pid "${target_pid}" --argjson threshold "${THRESHOLD_MS}" \
			--argjson started "${started}" --argjson finished "${finished}" '
			all(.io_schedule_timeout_stacks[] | select(.pid == $pid);
				.tid == $pid and (.comm | test("^dd( |$)")) and
				.schedule_latency_us > ($threshold * 1000) and
				.schedule_latency_us <= (($finished - $started + 1) * 1000000))
		' "${output}" > /dev/null || fatal "T06 target task identity or microsecond duration is inconsistent"
	fi
	log_info "T06 ${name}: confirmed blocked read task=${target_pid}, max-stack=${maximum}"
}

capture_stacks() {
	local name=$1 maximum=$2 target_pid started finished observed=0 workload_status=0
	local output="${TOOL_WORK_DIR}/${name}.json"
	# Clear only the private device cache before each independent capture.
	timeout --kill-after=1s 5s blockdev --flushbufs "/dev/mapper/${dm_name}" \
		|| fatal "T06 cannot clear its delayed device cache"
	read -r started _ < /proc/uptime
	defer_interrupts
	# Keep one reader alive for the complete capture, including slow attachment.
	# Its private delayed device cannot reach EOF within this bounded run.
	setsid timeout --preserve-status --kill-after=2s 30s bash -c '
		printf "%s\n" "$$" > "$1"
		exec dd if="$2" of=/dev/null bs=4096 status=none
	' _ "${TOOL_WORK_DIR}/${name}.pid" "/dev/mapper/${dm_name}" \
		> "${TOOL_WORK_DIR}/${name}.dd.log" 2>&1 &
	workload_pid=$!
	record_resources
	wait_until 2 0.1 test -s "${TOOL_WORK_DIR}/${name}.pid" \
		|| fatal "T06 workload did not publish its task identity"
	target_pid=$(< "${TOOL_WORK_DIR}/${name}.pid")
	printf 'task=%s\nstarted_uptime=%s\n' "${target_pid}" "${started}" \
		> "${TOOL_WORK_DIR}/${name}.evidence"

	defer_interrupts
	setsid timeout --kill-after=5s 20s "${TOOL_BIN}" \
		--bpf-path "${TOOL_BPF}" --duration "${DURATION}" \
		--schedule-threshold "${THRESHOLD_MS}" --max-stack "${maximum}" --output json \
		> "${output}" 2> "${TOOL_WORK_DIR}/${name}.err" &
	tracer_pid=$!
	record_resources
	if wait_until 4 0.1 assert_task_blocked_in_io "${target_pid}" "${TOOL_WORK_DIR}/${name}"; then
		observed=1
	fi
	wait "${tracer_pid}" || fatal "T06 iotracing ${name} capture failed or timed out"
	tracer_pid=""
	kill -0 "${target_pid}" 2> /dev/null || fatal "T06 reader stopped before capture completed"
	kill -TERM -- "-${workload_pid}" 2> /dev/null || true
	wait "${workload_pid}" || workload_status=$?
	workload_pid=""
	read -r finished _ < /proc/uptime
	printf 'finished_uptime=%s\nworkload_status=%s\nblocked_read_observed=%s\n' \
		"${finished}" "${workload_status}" "${observed}" >> "${TOOL_WORK_DIR}/${name}.evidence"
	record_resources
	[[ ${workload_status} == 0 || ${workload_status} == 15 || ${workload_status} == 143 ]] \
		|| fatal "T06 sustained read workload failed or timed out: status=${workload_status}"
	((observed)) || skip "T06 BLOCKED: procfs did not confirm an IO-blocked reader during capture"

	assert_iotracing_stacks "${name}" "${maximum}" "${target_pid}" "${started}" "${finished}"
}

capture_stacks attribution 64
capture_stacks limited 1
