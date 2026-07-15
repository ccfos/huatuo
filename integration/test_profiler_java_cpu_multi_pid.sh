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

# Verify one Java profiler invocation samples two explicit PIDs and keeps
# their distinct workloads under the correct process roots.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

readonly TOOL_BIN="${ROOT_DIR}/_output/bin/profiler"
readonly FIXTURE_SRC="${ROOT_DIR}/integration/testdata/TestProfilerJavaMultiPID.java"
readonly PROFILER_DURATION=10
readonly PROFILER_FREQ=99
readonly PROFILER_AGGR_INTERVAL=5

command -v java > /dev/null || skip "java is not installed"
command -v javac > /dev/null || skip "javac is not installed"
[[ -x "${TOOL_BIN}" ]] || fatal "profiler binary missing: ${TOOL_BIN}"
[[ -x "${JAVA_PROFILER_TOOL_PATH}/bin/asprof" ]] \
	|| skip "asprof missing: ${JAVA_PROFILER_TOOL_PATH}/bin/asprof"
[[ -r "${JAVA_PROFILER_TOOL_PATH}/lib/libasyncProfiler.so" ]] \
	|| skip "async-profiler library missing: ${JAVA_PROFILER_TOOL_PATH}/lib/libasyncProfiler.so"

WORK_DIR=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/profiler-java-multi.XXXXXX")
PROFILER_STDOUT="${WORK_DIR}/profiler.out"
PROFILER_STDERR="${WORK_DIR}/profiler.err"
PROFILER_TARGET_PID0=""
PROFILER_TARGET_PID1=""

cleanup() {
	[[ -n "${PROFILER_TARGET_PID0}" ]] && stop_by_pid "${PROFILER_TARGET_PID0}" 5 || true
	[[ -n "${PROFILER_TARGET_PID1}" ]] && stop_by_pid "${PROFILER_TARGET_PID1}" 5 || true
}
trap cleanup EXIT

javac -d "${WORK_DIR}" "${FIXTURE_SRC}"

java \
	-XX:CompileCommand=dontinline,TestProfilerJavaMultiPID.alphaHotMethod \
	-cp "${WORK_DIR}" TestProfilerJavaMultiPID alpha \
	> "${WORK_DIR}/alpha.out" 2> "${WORK_DIR}/alpha.err" &
PROFILER_TARGET_PID0=$!

java \
	-XX:CompileCommand=dontinline,TestProfilerJavaMultiPID.betaHotMethod \
	-cp "${WORK_DIR}" TestProfilerJavaMultiPID beta \
	> "${WORK_DIR}/beta.out" 2> "${WORK_DIR}/beta.err" &
PROFILER_TARGET_PID1=$!

kill -0 "${PROFILER_TARGET_PID0}" 2> /dev/null || fatal "alpha fixture exited immediately"
kill -0 "${PROFILER_TARGET_PID1}" 2> /dev/null || fatal "beta fixture exited immediately"

log_info "profiling Java pids=${PROFILER_TARGET_PID0},${PROFILER_TARGET_PID1}"
if ! "${TOOL_BIN}" \
	--type cpu \
	--language java \
	--pid "${PROFILER_TARGET_PID0},${PROFILER_TARGET_PID1}" \
	--tool-path "${JAVA_PROFILER_TOOL_PATH}" \
	--duration "${PROFILER_DURATION}" \
	--freq "${PROFILER_FREQ}" \
	--aggr-interval "${PROFILER_AGGR_INTERVAL}" \
	--output-format collapsed \
	--output-path "${WORK_DIR}" \
	> "${PROFILER_STDOUT}" 2> "${PROFILER_STDERR}"; then
	fatal "profiler exited non-zero (see ${PROFILER_STDERR})"
fi

mapfile -t FOLDED_FILES < <(find "${WORK_DIR}" -maxdepth 1 -name 'perf_*.folded' -type f)
[[ ${#FOLDED_FILES[@]} -gt 0 ]] || fatal "no perf_*.folded file produced"

grep -qh "process ${PROFILER_TARGET_PID0};" "${FOLDED_FILES[@]}" \
	|| fatal "PID ${PROFILER_TARGET_PID0} root not found"
grep -qh "process ${PROFILER_TARGET_PID1};" "${FOLDED_FILES[@]}" \
	|| fatal "PID ${PROFILER_TARGET_PID1} root not found"
grep -qh "process ${PROFILER_TARGET_PID0};.*alphaHotMethod" "${FOLDED_FILES[@]}" \
	|| fatal "alpha workload stack not found for PID ${PROFILER_TARGET_PID0}"
grep -qh "process ${PROFILER_TARGET_PID1};.*betaHotMethod" "${FOLDED_FILES[@]}" \
	|| fatal "beta workload stack not found for PID ${PROFILER_TARGET_PID1}"

if grep -qh "process ${PROFILER_TARGET_PID0};.*betaHotMethod" "${FOLDED_FILES[@]}"; then
	fatal "beta workload was attributed to PID ${PROFILER_TARGET_PID0}"
fi
if grep -qh "process ${PROFILER_TARGET_PID1};.*alphaHotMethod" "${FOLDED_FILES[@]}"; then
	fatal "alpha workload was attributed to PID ${PROFILER_TARGET_PID1}"
fi

log_info "both Java PIDs produced distinct correctly attributed stacks"
