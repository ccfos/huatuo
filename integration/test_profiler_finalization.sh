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

# Exercise CLI stop policy and final multi-PID delivery through an external tool.
# The stand-in emits samples only when stopped, making final output deterministic.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
require_commands python3
readonly FINALIZE_BIN="${ROOT_DIR}/_output/bin/profiler"
readonly FINALIZE_WORK="${HUATUO_BAMAI_TEST_TMPDIR}/finalization"
finalize_targets=()
finalize_pid=""
cleanup() {
	[[ -z ${finalize_pid} ]] || stop_and_wait_by_pid "${finalize_pid}" 5 || true
	local pid
	for pid in "${finalize_targets[@]}"; do stop_and_wait_by_pid "${pid}" 5 || true; done
}
trap cleanup EXIT

finalize_tools_ready() {
	local ready_dir=$1
	[[ -f ${ready_dir}/${finalize_targets[0]} && -f ${ready_dir}/${finalize_targets[1]} ]]
}

assert_finalization_result() {
	local expected=$1 pid=$2 dir=$3 status=0 total target
	wait_until 35 0.05 process_exited "${pid}" \
		|| fatal "profiler did not exit within 35s: ${dir}/stderr"
	wait "${pid}" || status=$?
	if [[ ${expected} == failure ]]; then
		[[ ${status} != 0 ]] || fatal "${dir}: expected nonzero exit, got 0"
		[[ ! -e ${dir}/result.folded ]] || fatal "failed profiling published output: ${dir}/result.folded"
		return
	fi

	[[ ${status} == 0 ]] || fatal "${dir}: expected exit 0, got ${status} (see stderr)"
	[[ -s ${dir}/result.folded ]] || fatal "missing final sample output: ${dir}/result.folded"
	for target in "${finalize_targets[@]}"; do
		total=$(awk -v marker="pid=\"${target}\";" 'index($0, marker) && /final_worker/ {sum += $NF} END {print sum+0}' "${dir}/result.folded")
		[[ ${total} == 30000000 ]] || fatal "PID ${target}: expected 30000000 final CPU nanoseconds, got ${total}"
	done
}

run_finalization_case() {
	local scenario=$1 duration=30 signal="" fail_pid="" expected=success
	local dir="${FINALIZE_WORK}/${scenario}"
	case ${scenario} in
	duration) duration=2 ;;
	INT) signal=INT ;;
	TERM | HUP) signal=${scenario} expected=failure ;;
	tool_failure) signal=INT fail_pid=${finalize_targets[1]} expected=failure ;;
	*) fatal "unknown finalization scenario: ${scenario}" ;;
	esac

	mkdir -p "${dir}/ready"
	PROFILER_FINALIZE_READY_DIR="${dir}/ready" PROFILER_FINALIZE_FAIL_PID="${fail_pid}" \
		"${FINALIZE_BIN}" --type cpu --language python \
		--pid "${finalize_targets[0]},${finalize_targets[1]}" \
		--tool-path "${FINALIZE_WORK}/tools" --freq 100 \
		--duration "${duration}" --aggr-interval "${duration}" \
		--output-format collapsed --output-path "${dir}/result.folded" \
		> "${dir}/stdout" 2> "${dir}/stderr" &
	finalize_pid=$!
	wait_until 10 0.05 finalize_tools_ready "${dir}/ready" \
		|| fatal "external tools did not become ready: ${dir}"
	if [[ -n ${signal} ]]; then
		kill -s "${signal}" "${finalize_pid}" || fatal "failed to send ${signal} to profiler"
	fi
	assert_finalization_result "${expected}" "${finalize_pid}" "${dir}"
	finalize_pid=""
	log_info "${scenario}: finalization policy verified"
}

mkdir -p "${FINALIZE_WORK}/tools"
cp "${ROOT_DIR}/integration/testdata/tools/test_pyspy_finalize.py" "${FINALIZE_WORK}/tools/py-spy"
chmod +x "${FINALIZE_WORK}/tools/py-spy"
for target in first second; do
	python3 -c 'import time; time.sleep(300)' > "${FINALIZE_WORK}/${target}.log" 2>&1 &
	finalize_targets+=("$!")
done

for scenario in duration INT TERM HUP tool_failure; do
	run_finalization_case "${scenario}"
done
