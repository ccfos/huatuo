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

# Pipe acknowledgments and published windows coordinate failure injection.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"

require_commands jq mkfifo setsid timeout java javac "${PROFILER_TOOL_DIR}/bin/asprof"
require_readable "${PROFILER_TOOL_DIR}/lib/libasyncProfiler.so"

JAVA_EXCEPTIONS_DIR=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/java-exceptions.XXXXXX")
readonly JAVA_EXCEPTIONS_DIR
timeout 30 javac -d "${JAVA_EXCEPTIONS_DIR}" "${ROOT_DIR}/integration/testdata/TestProfilerJavaMultiPID.java"

assert_java_capture_finished() {
	jq -se --arg alpha "$1" --arg beta "$2" '
		length > 0 and all(.[];
			if .pid == $alpha then all(.names[];
				(contains("betaHotMethod") or contains("betaAfterFaultHotMethod")) | not)
			elif .pid == $beta then all(.names[]; contains("alphaHotMethod") | not)
			else false end)
	' "${PROFILER_STACKS}" > /dev/null || fatal "Java samples have incorrect PID attribution"
	[[ ! -e /tmp/libasyncProfiler.so ]] || fatal "Java agent library leaked"
}

assert_java_target_loss() {
	local pid
	for pid in "$@"; do
		grep -q "failed for pid ${pid}" "${PROFILER_OUT}" "${PROFILER_ERR}" \
			|| fatal "missing lost JVM diagnostic for PID ${pid}"
	done
	grep -q 'Error:' "${PROFILER_OUT}" "${PROFILER_ERR}" || fatal "missing Java target-loss error"
}

assert_java_final_samples() {
	# Include startup and shutdown, with headroom for whole-second clocks.
	((SECONDS - profiler_start_seconds < interval - 2)) \
		|| fatal "SIGINT case reached the first periodic window; final flushing is unproven"
	assert_profiler_stack "${target_pids[0]}" alphaHotMethod
	assert_profiler_stack "${target_pids[1]}" betaHotMethod
}

for test_case in some-killed all-killed interrupt terminate; do
	(
		trap cleanup_piped_processes EXIT
		case_dir="${JAVA_EXCEPTIONS_DIR}/${test_case}"
		mkdir -p "${case_dir}"
		log_info "Java exception: ${test_case}"
		target_pids=() target_start_times=() target_input_fds=() target_output_fds=()
		for workload in alpha beta; do
			start_piped_process "${case_dir}/${workload}" java \
				"-XX:CompileCommand=dontinline,TestProfilerJavaMultiPID.${workload}HotMethod" \
				-XX:CompileCommand=dontinline,TestProfilerJavaMultiPID.betaAfterFaultHotMethod \
				-cp "${JAVA_EXCEPTIONS_DIR}" TestProfilerJavaMultiPID "${workload}" controlled
			target_pids+=("${PROCESS_PID}")
			target_start_times+=("${PROCESS_START_TIME}")
			target_input_fds+=("${PROCESS_INPUT_FD}")
			target_output_fds+=("${PROCESS_OUTPUT_FD}")
			assert_process_message "${PROCESS_OUTPUT_FD}" "${case_dir}/${workload}.out" ready
		done
		duration=30 interval=1
		[[ ${test_case} != *-killed ]] || duration=5
		if [[ ${test_case} == interrupt ]]; then
			# No periodic file can precede SIGINT; samples must come from final flushing.
			duration=60 interval=60
		fi
		profiler_start_seconds=${SECONDS}
		start_exception_profiler "${case_dir}" java "${duration}" "${interval}" \
			--tool-path "${PROFILER_TOOL_DIR}" --pid "${target_pids[0]},${target_pids[1]}"
		if [[ ${test_case} == interrupt ]]; then
			assert_process_message "${PROFILER_OUTPUT_FD}" "${PROFILER_OUT}" 'data reading loop started'
			for index in 0 1; do
				printf 'checkpoint\n' >&"${target_input_fds[index]}"
				assert_process_message "${target_output_fds[index]}" "${case_dir}/checkpoint-${index}.out" checkpoint
			done
		else
			assert_profiler_samples_before_deadline "${target_pids[0]}" alphaHotMethod
			assert_profiler_samples_before_deadline "${target_pids[1]}" betaHotMethod
		fi

		case ${test_case} in
		some-killed | all-killed)
			killed_pids=("${target_pids[0]}")
			[[ ${test_case} != all-killed ]] || killed_pids+=("${target_pids[1]}")
			for pid in "${killed_pids[@]}"; do
				kill -KILL "${pid}"
				status=0
				wait "${pid}" || status=$?
				assert_eq "${status}" 137 "killed JVM exit" || fatal "JVM did not exit on SIGKILL"
			done
			if [[ ${test_case} == some-killed ]]; then
				# Only work requested after the peer's death can carry this stack.
				printf 'after-fault\n' >&"${target_input_fds[1]}"
				assert_process_message "${target_output_fds[1]}" "${case_dir}/beta.out" after-fault
				assert_profiler_samples_before_deadline "${target_pids[1]}" betaAfterFaultHotMethod
			fi
			# Let the duration expire so target-loss errors cannot be hidden by a signal.
			assert_profiler_exit 1
			assert_java_target_loss "${killed_pids[@]}"
			;;
		interrupt)
			kill -INT "${PROFILER_PID}"
			assert_profiler_exit 0
			assert_java_final_samples
			;;
		terminate)
			kill -TERM "${PROFILER_PID}"
			assert_profiler_exit 1
			;;
		esac
		assert_java_capture_finished "${target_pids[0]}" "${target_pids[1]}"

		# Reattachment also checks that stopping the profiler left healthy JVMs alive.
		if [[ ${test_case} != all-killed ]]; then
			if [[ ${test_case} == some-killed ]]; then
				recovery_pid=${target_pids[1]}
				recovery_start_time=${target_start_times[1]}
				recovery_method=betaAfterFaultHotMethod
			else
				for index in "${!target_pids[@]}"; do
					assert_process_alive "${target_pids[index]}" "${target_start_times[index]}" "Java target after profiler stop"
				done
				recovery_pid=${target_pids[0]}
				recovery_start_time=${target_start_times[0]}
				recovery_method=alphaHotMethod
			fi
			assert_process_alive "${recovery_pid}" "${recovery_start_time}" "Java recovery target"
			start_exception_profiler "${case_dir}/recovery" java 1 1 \
				--tool-path "${PROFILER_TOOL_DIR}" --pid "${recovery_pid}"
			assert_profiler_exit 0
			assert_profiler_stack "${recovery_pid}" "${recovery_method}"
		fi
	)
done
