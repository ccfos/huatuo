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

require_commands mkfifo setsid
PROFILER_EXIT_DIR=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/profiler-exit.XXXXXX")
readonly PROFILER_EXIT_DIR

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
		child_start_time=$(process_start_time "${PROCESS_PID}") || fatal "child identity unavailable"
		printf '%s\n' "${PROCESS_PID}" > "${case_dir}/child.pid"
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

	expected_status=0
	[[ ${test_case} != *-stalled ]] || expected_status=1
	assert_eq "${status}" "${expected_status}" "${test_case}; see ${case_dir}/assertion.log" \
		|| fatal "stdout EOF did not enforce the expected process-exit result"
	if [[ ${test_case} == *-stalled ]]; then
		grep -qF 'did not exit before deadline' "${case_dir}/assertion.log" \
			|| fatal "stalled child failed for a reason other than its exit deadline"
	fi
	[[ -s ${case_dir}/child.pid ]] || fatal "child PID was not recorded"
	child_pid=$(< "${case_dir}/child.pid")
	process_exited "${child_pid}" || fatal "cleanup left child PID ${child_pid} running"
done
