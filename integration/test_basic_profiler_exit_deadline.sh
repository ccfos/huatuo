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

set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"

require_commands mkfifo setsid python3
PROFILER_EXIT_DIR=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/profiler-exit.XXXXXX")
readonly PROFILER_EXIT_DIR

assert_exit_deadline_result() {
	local test_case=$1 actual_status=$2 case_dir=$3 expected_status=0
	local child_pid child_start_time
	[[ ${test_case} != *-stalled ]] || expected_status=1
	assert_eq "${actual_status}" "${expected_status}" "${test_case}; see ${case_dir}/assertion.log" \
		|| fatal "stdout EOF did not enforce the expected process-exit result"
	if [[ ${test_case} == *-stalled ]]; then
		grep -qF 'did not exit before deadline' "${case_dir}/assertion.log" \
			|| fatal "stalled child failed for a reason other than its exit deadline"
	fi
	[[ -s ${case_dir}/child.identity ]] || fatal "child identity was not recorded"
	read -r child_pid child_start_time < "${case_dir}/child.identity"
	process_exited "${child_pid}" "${child_start_time}" || fatal "cleanup left child PID ${child_pid} running"
}

assert_liveness_result() {
	local test_case=$1 actual_status=$2 fixture_status=$3 assertion_log=$4 expected_status=1
	assert_eq "${fixture_status}" 0 "liveness fixture exit" || fatal "liveness fixture failed"
	[[ ${test_case} != alive ]] || expected_status=0
	assert_eq "${actual_status}" "${expected_status}" "${test_case}; see ${assertion_log}" \
		|| fatal "liveness assertion accepted an exited or different process, or rejected a live one"
	if [[ ${test_case} != alive ]]; then
		grep -qF 'exited or its identity changed' "${assertion_log}" \
			|| fatal "liveness assertion failed for an unrelated reason"
	fi
}

for test_case in profiler-exited profiler-stalled collector-exited collector-stalled collector-reused; do
	case_dir="${PROFILER_EXIT_DIR}/${test_case}"
	mkdir "${case_dir}"
	log_info "exit deadline: ${test_case} after stdout EOF"
	status=0
	(
		trap cleanup_piped_processes EXIT
		# The parent records identity before allowing EOF. The fixture timeout
		# bounds the regression even if an assertion loses its exit deadline.
		start_piped_process "${case_dir}/child" bash -c '
			IFS= read -r -t 4 command && [[ $command == close ]] || exit 2
			exec >&-
			if [[ $1 != *-exited ]]; then
				IFS= read -r -t 4 command || :
			fi
			exit 7
		' bash "${test_case}"
		child_start_time=${PROCESS_START_TIME}
		printf '%s %s\n' "${PROCESS_PID}" "${child_start_time}" > "${case_dir}/child.identity"
		printf 'close\n' >&"${PROCESS_INPUT_FD}"
		if [[ ${test_case} == profiler-* ]]; then
			PROFILER_PID=${PROCESS_PID}
			PROFILER_OUTPUT_FD=${PROCESS_OUTPUT_FD}
			PROFILER_OUT="${case_dir}/profiler.out"
			PROFILER_ERR="${case_dir}/child.err"
			PROFILER_STACKS="${case_dir}/stacks.json"
			: > "${PROFILER_OUT}"
			: > "${PROFILER_STACKS}"
			assert_profiler_exit 7 1
		else
			collector_fd=${PROCESS_OUTPUT_FD}
			collector_pids=("${PROCESS_PID}")
			collector_start_times=("${child_start_time}")
			if [[ ${test_case} == collector-reused ]]; then
				# Model the same PID belonging to a different process instance.
				collector_start_times=("$((child_start_time + 1))")
			fi
			assert_collectors_exited 1
			if [[ ${test_case} == collector-reused ]]; then
				! process_exited "${PROCESS_PID}" "${child_start_time}" \
					|| fatal "exit assertion touched the replacement process"
			fi
		fi
	) > "${case_dir}/assertion.log" 2>&1 || status=$?

	assert_exit_deadline_result "${test_case}" "${status}" "${case_dir}"
done

for test_case in alive zombie reused; do
	(
		trap cleanup_piped_processes EXIT
		case_dir="${PROFILER_EXIT_DIR}/liveness-${test_case}"
		mkdir "${case_dir}"
		log_info "process identity: ${test_case}"
		if [[ ${test_case} == zombie ]]; then
			start_piped_process "${case_dir}/parent" python3 -c '
import os
import select
import sys

child_pid = os.fork()
if child_pid == 0:
    os._exit(0)
# Observe exit without reaping; hold the zombie until the Shell finishes checking it.
os.waitid(os.P_PID, child_pid, os.WEXITED | os.WNOWAIT)
print(f"zombie {child_pid}", flush=True)
if select.select([sys.stdin], [], [], 15)[0]:
    sys.stdin.readline()
os.waitpid(child_pid, 0)
print("finished", flush=True)
'
			IFS=' ' read -r -t 5 -u "${PROCESS_OUTPUT_FD}" message target_pid \
				&& [[ ${message} == zombie && ${target_pid} =~ ^[1-9][0-9]*$ ]] \
				|| fatal "zombie fixture did not report its child PID"
			IFS= read -r target_stat < "/proc/${target_pid}/stat" \
				|| fatal "zombie fixture child disappeared before the assertion"
			[[ ${target_stat##*) } == Z\ * ]] || fatal "fixture child is not a zombie"
			target_start_time=$(process_start_time "${target_pid}") || fatal "zombie identity unavailable"
		else
			start_piped_process "${case_dir}/child" bash -c '
				printf "ready\n"
				IFS= read -r -t 15 command && [[ $command == finish ]] || exit 2
				printf "finished\n"
			'
			assert_process_message "${PROCESS_OUTPUT_FD}" "${case_dir}/fixture.out" ready
			target_pid=${PROCESS_PID}
			target_start_time=${PROCESS_START_TIME}
			! process_exited "${target_pid}" "${target_start_time}" || fatal "live fixture exited before assertion"
			if [[ ${test_case} == reused ]]; then
				# A live PID with another start time must not satisfy the original identity.
				target_start_time=$((target_start_time + 1))
			fi
		fi
		status=0
		(assert_process_alive "${target_pid}" "${target_start_time}" "${test_case} target") \
			> "${case_dir}/assertion.log" 2>&1 || status=$?
		# Let the fixture reap its child before checking the assertion result.
		printf 'finish\n' >&"${PROCESS_INPUT_FD}"
		assert_process_message "${PROCESS_OUTPUT_FD}" "${case_dir}/fixture.out" finished
		process_exited "${PROCESS_PID}" "${PROCESS_START_TIME}" \
			|| wait_until 2 0.1 process_exited "${PROCESS_PID}" "${PROCESS_START_TIME}" \
			|| fatal "liveness fixture did not exit after its finish acknowledgment"
		fixture_status=0
		wait "${PROCESS_PID}" || fixture_status=$?
		assert_liveness_result "${test_case}" "${status}" "${fixture_status}" "${case_dir}/assertion.log"
	)
done
