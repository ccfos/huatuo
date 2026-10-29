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

# Observe real py-spy attachment over a FIFO before killing targets or tools.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"

require_commands jq mkfifo setsid python3 "${PROFILER_TOOL_DIR}/py-spy"

PYTHON_EXCEPTIONS_DIR=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/python-exceptions.XXXXXX")
readonly PYTHON_EXCEPTIONS_DIR
mkdir "${PYTHON_EXCEPTIONS_DIR}/tools"
export PROFILER_REAL_PYSPY="${PROFILER_TOOL_DIR}/py-spy"
cat > "${PYTHON_EXCEPTIONS_DIR}/tools/py-spy" << 'SH'
#!/usr/bin/env bash
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
exec > "$PROFILER_EXCEPTION_FIFO"
printf 'collector %s %s\n' "$$" "$(process_start_time "$$")"
exec "$PROFILER_REAL_PYSPY" "$@"
SH
chmod +x "${PYTHON_EXCEPTIONS_DIR}/tools/py-spy"

assert_collector_messages() {
	local expected=$1 count=$2 line collector_pid collector_start_time extra
	local deadline=$((SECONDS + 15))
	while [[ $(grep -cF "${expected}" "${case_dir}/collectors.out" || true) -lt ${count} ]]; do
		((SECONDS < deadline)) || fatal "py-spy timed out before '${expected}'"
		IFS= read -r -t "$((deadline - SECONDS))" -u "${collector_fd}" line \
			|| fatal "py-spy exited or timed out before '${expected}'"
		printf '%s\n' "${line}" >> "${case_dir}/collectors.out"
		if [[ ${line} == 'collector '* ]]; then
			read -r collector_pid collector_start_time extra <<< "${line#collector }"
			[[ ${collector_pid} =~ ^[1-9][0-9]*$ && ${collector_start_time} =~ ^[0-9]+$ && -z ${extra} ]] \
				|| fatal "invalid collector identity: ${line}"
			collector_pids+=("${collector_pid}")
			collector_start_times+=("${collector_start_time}")
		fi
	done
}

assert_python_checkpoint() {
	local index=$1
	printf 'checkpoint\n' >&"${target_input_fds[index]}"
	assert_process_message "${target_output_fds[index]}" "${case_dir}/target-${index}.out" checkpoint
}

assert_python_capture_has_no_samples() {
	[[ ! -s ${PROFILER_STACKS} ]] || fatal "unexpected Python samples for ${test_case}; see ${PROFILER_OUT}"
}

assert_python_collector_failed() {
	local target_pid=$1
	grep -qE "failed for pid ${target_pid}: .*signal: killed([[:space:]]|$)" "${PROFILER_OUT}" "${PROFILER_ERR}" \
		|| fatal "missing SIGKILL diagnostic for collector target PID ${target_pid}"
	assert_python_capture_has_no_samples
}

for test_case in some-killed all-killed interrupt-after-targets-exit backend-killed interrupt terminate kill; do
	(
		collector_pids=() collector_start_times=()
		cleanup() {
			local index pid
			cleanup_piped_processes
			# py-spy has its own process group. Clean it up even when the CLI dies
			# before its parent-death handling works, without touching reused PIDs.
			for index in "${!collector_pids[@]}"; do
				pid=${collector_pids[index]}
				[[ /proc/${pid}/exe -ef ${PROFILER_REAL_PYSPY} ]] || continue
				process_exited "${pid}" "${collector_start_times[index]}" && continue
				kill -KILL -- "-${pid}" 2> /dev/null || true
			done
		}
		trap cleanup EXIT
		case_dir="${PYTHON_EXCEPTIONS_DIR}/${test_case}"
		mkdir "${case_dir}"
		log_info "Python exception: ${test_case}"
		target_pids=() target_start_times=() target_input_fds=() target_output_fds=()
		target_count=1
		case ${test_case} in some-killed | all-killed | interrupt-after-targets-exit) target_count=2 ;; esac
		for ((index = 0; index < target_count; index++)); do
			start_piped_process "${case_dir}/target-${index}" python3 \
				"${ROOT_DIR}/integration/testdata/test_profiler_python_cpu.py" controlled
			target_pids+=("${PROCESS_PID}")
			target_start_times+=("${PROCESS_START_TIME}")
			target_input_fds+=("${PROCESS_INPUT_FD}")
			target_output_fds+=("${PROCESS_OUTPUT_FD}")
			assert_process_message "${PROCESS_OUTPUT_FD}" "${case_dir}/target-${index}.out" ready
		done
		export PROFILER_EXCEPTION_FIFO="${case_dir}/collectors.fifo"
		mkfifo "${PROFILER_EXCEPTION_FIFO}"
		: > "${case_dir}/collectors.out"
		exec {collector_gate_fd}<> "${PROFILER_EXCEPTION_FIFO}"
		exec {collector_fd}< "${PROFILER_EXCEPTION_FIFO}"
		pid_list=$(
			IFS=,
			echo "${target_pids[*]}"
		)
		start_exception_profiler "${case_dir}" python 30 30 \
			--tool-path "${PYTHON_EXCEPTIONS_DIR}/tools" --pid "${pid_list}"
		assert_collector_messages 'Sampling process' "${target_count}"
		assert_eq "${#collector_pids[@]}" "${target_count}" "py-spy collector count" \
			|| fatal "wrong number of py-spy collectors"
		# All real collectors now hold a writer; later require both EOF and exit.
		exec {collector_gate_fd}>&-
		for ((index = 0; index < target_count; index++)); do
			assert_python_checkpoint "${index}"
		done

		case ${test_case} in
		some-killed | all-killed | interrupt-after-targets-exit)
			killed_count=${target_count}
			[[ ${test_case} != some-killed ]] || killed_count=1
			for ((index = 0; index < killed_count; index++)); do
				kill -KILL "${target_pids[index]}"
				status=0
				wait "${target_pids[index]}" || status=$?
				assert_eq "${status}" 137 "killed Python exit" || fatal "Python did not exit on SIGKILL"
			done
			assert_collector_messages 'Wrote raw' "${killed_count}"
			[[ ${test_case} != some-killed ]] || assert_python_checkpoint 1
			# All-killed must also succeed without an explicit stop signal.
			if [[ ${test_case} == some-killed ]]; then
				kill -INT "${PROFILER_PID}"
			elif [[ ${test_case} == interrupt-after-targets-exit ]]; then
				# Natural completion may win this race. The provider unit test
				# deterministically interrupts between its two final deliveries.
				kill -INT "${PROFILER_PID}" 2> /dev/null \
					|| log_info "Python capture completed before the late interrupt"
			fi
			assert_profiler_exit 0
			for pid in "${target_pids[@]}"; do
				assert_profiler_stack "${pid}" controlled_hot_method
			done
			if [[ ${test_case} == some-killed ]]; then
				assert_process_alive "${target_pids[1]}" "${target_start_times[1]}" "surviving Python target"
			fi
			;;
		backend-killed)
			kill -KILL "${collector_pids[0]}"
			assert_profiler_exit 1
			assert_python_collector_failed "${target_pids[0]}"
			assert_process_alive "${target_pids[0]}" "${target_start_times[0]}" "Python target after collector failure"
			;;
		interrupt | terminate | kill)
			case ${test_case} in
			interrupt) signal=INT expected_status=0 ;;
			terminate) signal=TERM expected_status=1 ;;
			kill) signal=KILL expected_status=137 ;;
			esac
			kill -"${signal}" "${PROFILER_PID}"
			assert_profiler_exit "${expected_status}"
			if [[ ${test_case} == interrupt ]]; then
				assert_profiler_stack "${target_pids[0]}" controlled_hot_method
			else
				assert_python_capture_has_no_samples
			fi
			assert_process_alive "${target_pids[0]}" "${target_start_times[0]}" "Python target after profiler stop"
			;;
		esac
		assert_collectors_exited
		if [[ ${test_case} == kill ]]; then
			start_exception_profiler "${case_dir}/recovery" python 1 1 \
				--tool-path "${PROFILER_TOOL_DIR}" --pid "${target_pids[0]}"
			assert_profiler_exit 0
			assert_profiler_stack "${target_pids[0]}" controlled_hot_method
		fi
	)
done
