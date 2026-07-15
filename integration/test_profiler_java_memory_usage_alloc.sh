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

# Verify Java allocation and live-object modes produce allocation stacks.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

readonly TOOL_BIN="${ROOT_DIR}/_output/bin/profiler"
readonly FIXTURE_SRC="${ROOT_DIR}/integration/testdata/TestProfilerJavaMemory.java"
readonly PROFILER_DURATION=10
readonly PROFILER_AGGR_INTERVAL=10
readonly EXPECTED_METHOD="TestProfilerJavaMemory.allocateHotMethod"

command -v java > /dev/null || skip "java is not installed"
command -v javac > /dev/null || skip "javac is not installed"
[[ -x "${TOOL_BIN}" ]] || fatal "profiler binary missing: ${TOOL_BIN}"
[[ -x "${JAVA_PROFILER_TOOL_PATH}/bin/asprof" ]] \
	|| skip "asprof missing: ${JAVA_PROFILER_TOOL_PATH}/bin/asprof"
[[ -r "${JAVA_PROFILER_TOOL_PATH}/lib/libasyncProfiler.so" ]] \
	|| skip "async-profiler library missing: ${JAVA_PROFILER_TOOL_PATH}/lib/libasyncProfiler.so"

WORK_DIR=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/profiler-java-mem.XXXXXX")
PROFILER_TARGET_PID=""

cleanup() {
	[[ -n "${PROFILER_TARGET_PID}" ]] && stop_by_pid "${PROFILER_TARGET_PID}" 5 || true
}
trap cleanup EXIT

javac -d "${WORK_DIR}" "${FIXTURE_SRC}"

run_profile_case() {
	local mode=$1
	local out_dir="${WORK_DIR}/${mode}"
	local captured_bytes
	local folded_file

	mkdir -p "${out_dir}"
	java \
		-XX:CompileCommand=dontinline,TestProfilerJavaMemory.allocateHotMethod \
		-cp "${WORK_DIR}" TestProfilerJavaMemory \
		> "${out_dir}/java.out" 2> "${out_dir}/java.err" &
	PROFILER_TARGET_PID=$!
	kill -0 "${PROFILER_TARGET_PID}" 2> /dev/null \
		|| fatal "Java memory fixture exited immediately for mode=${mode}"

	log_info "running Java memory profiler mode=${mode} pid=${PROFILER_TARGET_PID}"
	if ! "${TOOL_BIN}" \
		--type mem \
		--language java \
		--memory-mode "${mode}" \
		--pid "${PROFILER_TARGET_PID}" \
		--tool-path "${JAVA_PROFILER_TOOL_PATH}" \
		--duration "${PROFILER_DURATION}" \
		--aggr-interval "${PROFILER_AGGR_INTERVAL}" \
		--output-format collapsed \
		--output-path "${out_dir}" \
		> "${out_dir}/profiler.out" 2> "${out_dir}/profiler.err"; then
		fatal "Java memory profiler failed for mode=${mode}"
	fi

	stop_by_pid "${PROFILER_TARGET_PID}" 5
	PROFILER_TARGET_PID=""
	folded_file=$(find "${out_dir}" -maxdepth 1 -name 'perf_*.folded' -type f -size +0c -print -quit)
	[[ -n "${folded_file}" ]] || fatal "no non-empty folded output for mode=${mode}"
	captured_bytes=$(awk -v method="${EXPECTED_METHOD}" \
		'index($0, method) { total += $NF } END { printf "%.0f", total }' \
		"${folded_file}")
	((captured_bytes > 0)) || fatal "no allocation bytes captured for mode=${mode}"
	log_info "Java memory profiler mode=${mode} captured_bytes=${captured_bytes}"
}

for mode in object_alloc object_usage; do
	run_profile_case "${mode}"
done

# Sampling windows and GC timing are nondeterministic, so object_usage is not
# required to be less than object_alloc. Each mode only needs a positive value.
log_info "Java object_alloc and object_usage modes produced allocation stacks"
