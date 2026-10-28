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

require_commands jq mkfifo setsid
PROFILER_PHASE_DIR=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/profiler-phases.XXXXXX")
readonly PROFILER_PHASE_DIR

assert_sample_phase_result() {
	local test_case=$1 actual_status=$2 assertion_log=$3
	if [[ ${test_case} == buffered-only ]]; then
		assert_eq "${actual_status}" 1 "old buffered samples; see ${assertion_log}" \
			|| fatal "old buffered samples satisfied the post-fault assertion"
		grep -qF 'profiler exited or timed out before betaAfterFaultHotMethod' "${assertion_log}" \
			|| fatal "buffered-only case failed for an unrelated reason"
	else
		assert_eq "${actual_status}" 0 "post-fault samples; see ${assertion_log}" \
			|| fatal "post-fault samples did not satisfy the assertion"
	fi
}

for method in betaHotMethod betaAfterFaultHotMethod; do
	jq -cn --arg method "${method}" '{profile_type: "process_cpu:cpu:nanoseconds:cpu:nanoseconds",
		profile: {string_table: ["", "pid", "4242", $method],
			function: [{id: 1, name: 3}], location: [{id: 1, line: [{function_id: 1}]}],
			sample: [{value: [1], location_id: [1], label: [{key: 1, str: 2}]}]}}
	' > "${PROFILER_PHASE_DIR}/${method}.json"
done

for test_case in buffered-only buffered-then-new; do
	case_dir="${PROFILER_PHASE_DIR}/${test_case}"
	mkdir "${case_dir}"
	log_info "sample phase boundary: ${test_case}"
	status=0
	(
		trap cleanup_piped_processes EXIT
		mkfifo "${case_dir}/buffered.fifo"
		exec {buffered_fd}<> "${case_dir}/buffered.fifo"
		start_piped_process "${case_dir}/output" bash -c '
			cat "$1/betaHotMethod.json"
			printf "buffered\n" > "$2/buffered.fifo"
			IFS= read -r -t 4 command && [[ $command == fault ]] || exit 2
			if [[ $3 == buffered-then-new ]]; then
				cat "$1/betaAfterFaultHotMethod.json"
			fi
		' bash "${PROFILER_PHASE_DIR}" "${case_dir}" "${test_case}"
		# The old window is already in stdout, but the assertion has not read it.
		assert_process_message "${buffered_fd}" "${case_dir}/buffered.out" buffered
		exec {buffered_fd}>&-
		PROFILER_OUTPUT_FD=${PROCESS_OUTPUT_FD}
		PROFILER_OUT="${case_dir}/profiler.out"
		PROFILER_ERR="${case_dir}/output.err"
		PROFILER_STACKS="${case_dir}/stacks.json"
		: > "${PROFILER_OUT}"
		: > "${PROFILER_STACKS}"
		printf 'fault\n' >&"${PROCESS_INPUT_FD}"
		assert_profiler_samples_before_deadline 4242 betaAfterFaultHotMethod
	) > "${case_dir}/assertion.log" 2>&1 || status=$?
	assert_sample_phase_result "${test_case}" "${status}" "${case_dir}/assertion.log"
done
