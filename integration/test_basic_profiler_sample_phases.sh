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
source "${ROOT_DIR}/integration/lib_profiler.sh"

require_commands jq mkfifo setsid python3
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
		trap cleanup_running_jobs EXIT
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

assert_profile_record() (
	local mode=$1 status=0 expected_status=0
	local work="${PROFILER_PHASE_DIR}/record-${mode}"
	mkdir "${work}"
	case ${mode} in
	ordered) cp "${PROFILER_PHASE_DIR}/betaHotMethod.json" "${work}/input" ;;
	reordered) jq -c '{profile:.profile, profile_type:.profile_type}' "${PROFILER_PHASE_DIR}/betaHotMethod.json" > "${work}/input" ;;
	whitespace) printf '  \t%s\n' "$(< "${PROFILER_PHASE_DIR}/betaHotMethod.json")" > "${work}/input" ;;
	logs) printf 'data reading loop started\n{"level":"info","message":"ready"}\n' > "${work}/input" ;;
	invalid)
		printf '{"profile":\n' > "${work}/input"
		expected_status=1
		;;
	wrong_type)
		printf '{"profile_type":"cpu","profile":null}\n' > "${work}/input"
		expected_status=1
		;;
	multiple)
		printf '{} {}\n' > "${work}/input"
		expected_status=1
		;;
	partial)
		printf '{"profile":' > "${work}/input"
		expected_status=1
		;;
	esac
	(
		trap cleanup_running_jobs EXIT
		start_piped_process "${work}/process" cat "${work}/input"
		exec {PROCESS_INPUT_FD}>&-
		PROFILER_PID=${PROCESS_PID} PROFILER_OUTPUT_FD=${PROCESS_OUTPUT_FD}
		PROFILER_OUT="${work}/output" PROFILER_ERR="${work}/process.err" PROFILER_STACKS="${work}/stacks"
		: > "${PROFILER_OUT}"
		: > "${PROFILER_STACKS}"
		assert_profiler_exit 0 2
		[[ -z ${PROFILER_PID} && -z ${PROFILER_OUTPUT_FD} ]] || fatal "profiler state was not released"
		if [[ ${mode} == logs ]]; then
			[[ ! -s ${PROFILER_STACKS} ]] || fatal "log records became profile samples"
		else
			assert_profiler_stack 4242 betaHotMethod
		fi
	) > "${work}/assertion.log" 2>&1 || status=$?
	assert_eq "${status}" "${expected_status}" "${mode}: profile record; see ${work}/assertion.log"
	cmp "${work}/input" "${work}/output" || fatal "${mode}: raw output was not retained"
	if ((expected_status)); then
		grep -Eq '(invalid profiler output|incomplete profiler output record)' "${work}/assertion.log" \
			|| fatal "${mode}: profile failed without a decoding diagnosis"
	fi
)

for record_mode in ordered reordered whitespace logs invalid wrong_type multiple partial; do
	assert_profile_record "${record_mode}"
done

assert_streaming_output() (
	local mode=$1 status=0 expected_status=0 expected_error=""
	local work="${PROFILER_PHASE_DIR}/stream-${mode}"
	case ${mode} in
	exit_failure)
		expected_status=1
		expected_error='Java streaming profiler exited 7'
		;;
	malformed_tail)
		expected_status=1
		expected_error='expected multiple non-empty Java windows'
		;;
	missing_window)
		expected_status=1
		expected_error='two Java windows not published during collection'
		;;
	esac
	(assert_java_profiler_streams "${work}" python3 -c '
import json
import signal
import sys
import time

mode = sys.argv[1]
def finish(*_):
    print("{" if mode == "malformed_tail" else json.dumps({"final": True}), flush=True)
    sys.exit(7 if mode == "exit_failure" else 0)

signal.signal(signal.SIGINT, finish)
for _ in range(1 if mode == "missing_window" else 2):
    print(json.dumps({"profile": {"sample": [1]}}), flush=True)
while True:
    time.sleep(0.01)
' "${mode}") > "${work}.log" 2>&1 || status=$?
	assert_eq "${status}" "${expected_status}" "${mode}: streaming; see ${work}.log"
	if ((expected_status)); then
		grep -Fq "${expected_error}" "${work}.log" || fatal "${mode}: streaming failed for an unrelated reason"
	else
		jq -se 'last.final == true' "${work}/windows.json" > /dev/null || fatal "streaming lost its final record"
	fi
)

for stream_mode in complete exit_failure malformed_tail missing_window; do
	assert_streaming_output "${stream_mode}"
done
