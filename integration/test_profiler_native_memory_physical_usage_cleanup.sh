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

# Exercise the physical usage script's cancellation through real child processes.
# A private bind mount keeps fault injection away from the host's sysctl value.
set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_profiler.sh"

readonly PHYSICAL_CLEANUP_SYSCTL=/proc/sys/vm/compact_unevictable_allowed
readonly PHYSICAL_CLEANUP_SCRIPT="${ROOT_DIR}/integration/test_profiler_native_memory_physical_usage.sh"

require_commands unshare mount setsid gcc python3
require_native_pid_profiling
require_readable "${PHYSICAL_CLEANUP_SYSCTL}"
if ! kprobe_available folio_add_new_anon_rmap && ! kprobe_available page_add_new_anon_rmap; then
	skip "requires an anonymous page allocation kprobe for the physical usage script"
fi
if ! kprobe_available folio_remove_rmap_ptes && ! kprobe_available page_remove_rmap; then
	skip "requires an anonymous page removal kprobe for the physical usage script"
fi

assert_physical_cleanup() (
	local original=$1 mode=$2 signal=$3 expected_status=143
	local workspace="${HUATUO_BAMAI_TEST_TMPDIR}/${original}-${mode}-${signal}"
	local pid="" profiler_pid="" status=0
	local -a signal_reset_launcher
	[[ ${signal} != INT ]] || expected_status=130
	runner_build_signal_reset_launcher signal_reset_launcher
	mkdir -p "${workspace}"
	printf '%s\n' "${original}" > "${workspace}/sysctl"
	cleanup() {
		trap - EXIT
		trap '' INT TERM
		[[ -z ${pid} ]] || stop_by_process_group "${pid}" 1
		[[ -z ${pid} ]] || wait "${pid}" 2> /dev/null || true
	}
	trap cleanup EXIT

	# Background Bash inherits ignored INT; setsid alone cannot restore its traps.
	HUATUO_BAMAI_TEST_TMPDIR="${workspace}" setsid unshare --mount "${signal_reset_launcher[@]}" bash -euc '
		mount --make-rprivate /
		mount --bind "$1/sysctl" "$2"
		exec bash "$3"
	' physical-cleanup "${workspace}" "${PHYSICAL_CLEANUP_SYSCTL}" "${PHYSICAL_CLEANUP_SCRIPT}" \
		> "${workspace}/case.log" 2>&1 &
	pid=$!

	physical_cleanup_profiler_waiting() {
		local child comm
		# The parent logs this wait only after recording PROFILER_PID.
		grep -Fq '[profiler_ready ' "${workspace}/case.log" || return 1
		[[ -r /proc/${pid}/task/${pid}/children ]] || return 1
		for child in $(< "/proc/${pid}/task/${pid}/children"); do
			[[ -r /proc/${child}/comm ]] || continue
			comm=$(< "/proc/${child}/comm")
			if [[ ${comm} == profiler ]]; then
				profiler_pid=${child}
				return 0
			fi
		done
		return 1
	}
	wait_until 15 0.01 physical_cleanup_profiler_waiting \
		|| fatal "physical usage script did not reach profiler wait; see ${workspace}/case.log"
	kill -STOP "${profiler_pid}"
	kill -"${signal}" "${pid}"

	physical_cleanup_stop_pending() {
		local pending
		[[ -r /proc/${profiler_pid}/status ]] || return 1
		pending=$(awk '/^ShdPnd:/ {print $2}' "/proc/${profiler_pid}/status")
		(((16#${pending} & (1 << 14)) != 0))
	}
	# A pending TERM on the stopped profiler proves cleanup reached its child wait.
	wait_until 5 0.01 physical_cleanup_stop_pending \
		|| fatal "cleanup did not try stopping profiler; see ${workspace}/case.log"
	if [[ ${mode} == repeated ]]; then
		kill -TERM "${pid}"
		kill -INT "${pid}" 2> /dev/null || true
		wait_until 10 0.05 process_exited "${pid}" \
			|| fatal "physical usage cleanup did not finish; see ${workspace}/case.log"
		wait "${pid}" || status=$?
		assert_eq "${status}" "${expected_status}" "physical usage exit after repeated ${signal} cancellation"
		assert_eq "$(process_exited "${profiler_pid}" && echo exited || echo running)" exited \
			"physical usage stopped profiler despite repeated cancellation"
	else
		# Match the runner's five-second deadline while the profiler cannot exit.
		stop_by_process_group "${pid}" 5
		wait "${pid}" 2> /dev/null || status=$?
		if [[ ${status} != 137 ]]; then
			assert_eq "${status}" "${expected_status}" "physical usage deadline exit (or 137 after SIGKILL)"
		fi
	fi
	assert_eq "$(< "${workspace}/sysctl")" "${original}" \
		"physical usage restored sysctl after ${mode} cancellation"
	log_info "physical usage cleanup restored sysctl=${original} after ${mode} ${signal} cancellation (exit=${status})"
)

for physical_cleanup_signal in TERM INT; do
	assert_physical_cleanup 1 repeated "${physical_cleanup_signal}"
	assert_physical_cleanup 0 repeated "${physical_cleanup_signal}"
done
assert_physical_cleanup 1 deadline TERM
