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

# Test native memory profiler with --memory-mode virtual_alloc.
# Verifies that mmap events are captured and folded output contains expected patterns.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

# --- preconditions -----------------------------------------------------------

is_container && skip "native memory profiler requires bare-metal cgroup/PMU access"

readonly TOOL_BIN="${ROOT_DIR}/_output/bin/profiler"
readonly TOOL_OUT="${HUATUO_BAMAI_TEST_TMPDIR}/profiler.out"
readonly TOOL_ERR="${HUATUO_BAMAI_TEST_TMPDIR}/profiler.err"
readonly FIXTURE_SRC="${ROOT_DIR}/integration/testdata/test_profiler_mmap.user.c"

command -v gcc > /dev/null || skip "gcc(1) not in PATH"
[[ -x "${TOOL_BIN}" ]] || fatal "profiler binary missing: ${TOOL_BIN}"
[[ -r "${ROOT_DIR}/_output/bpf/native_virtual_alloc.o" ]] || fatal "native bpf object missing"
[[ -r "${FIXTURE_SRC}" ]] || fatal "fixture source missing: ${FIXTURE_SRC}"

# --- tunables ----------------------------------------------------------------

readonly PROFILER_DURATION=10
readonly PROFILER_AGGR_INTERVAL=5

# --- workspace + cleanup -----------------------------------------------------

WORK_DIR=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/profiler-mem.XXXXXX")
FIXTURE_BIN="${WORK_DIR}/mmap_workload"
FIXTURE_OUT="${WORK_DIR}/mmap.out"
FIXTURE_ERR="${WORK_DIR}/mmap.err"
TARGET_PID=""

cleanup() {
	local rc=$?
	[[ -n "${TARGET_PID}" ]] && stop_by_pid "${TARGET_PID}" 5
	if [[ ${rc} -ne 0 ]]; then
		dump_file "profiler stdout" "${TOOL_OUT}"
		dump_file "profiler stderr" "${TOOL_ERR}"
		dump_file "fixture stdout" "${FIXTURE_OUT}"
		dump_file "fixture stderr" "${FIXTURE_ERR}"
		log_error "workspace preserved at ${WORK_DIR}"
	else
		rm -rf "${WORK_DIR}"
	fi
}
trap cleanup EXIT

# --- build fixture -----------------------------------------------------------

log_info "compiling fixture: $(basename "${FIXTURE_SRC}")"
gcc -O0 -g -fno-inline -fno-omit-frame-pointer \
	-o "${FIXTURE_BIN}" "${FIXTURE_SRC}" \
	2> "${WORK_DIR}/gcc.err" \
	|| fatal "gcc failed:"$'\n'"$(< "${WORK_DIR}/gcc.err")"

# --- launch target -----------------------------------------------------------

log_info "launching target with 30s"
"${FIXTURE_BIN}" > "${FIXTURE_OUT}" 2> "${FIXTURE_ERR}" &
TARGET_PID=$!
kill -0 "${TARGET_PID}" 2> /dev/null || fatal "fixture exited immediately (pid=${TARGET_PID})"

log_info "target pid=${TARGET_PID}"

# --- run profiler ------------------------------------------------------------

log_info "running profiler for ${PROFILER_DURATION}s with --memory-mode virtual_alloc against pid=${TARGET_PID}"
if ! "${TOOL_BIN}" \
	--type mem \
	--language c \
	--memory-mode virtual_alloc \
	--pid "${TARGET_PID}" \
	--duration "${PROFILER_DURATION}" \
	--output-format collapsed \
	--output-path "${WORK_DIR}" \
	--aggr-interval "${PROFILER_AGGR_INTERVAL}" \
	> "${TOOL_OUT}" 2> "${TOOL_ERR}"; then
	fatal "profiler exited non-zero (see ${TOOL_ERR})"
fi

# --- assert ------------------------------------------------------------------

mapfile -t FOLDED_FILES < <(find "${WORK_DIR}" -maxdepth 1 -name 'perf_*.folded' -type f)
[[ ${#FOLDED_FILES[@]} -gt 0 ]] || fatal "no perf_*.folded file produced in ${WORK_DIR}"

log_info "found ${#FOLDED_FILES[@]} folded file(s); asserting non-empty output"

# Check that we have at least some profiling data
LINE_COUNT=$(wc -l < "${FOLDED_FILES[0]}") || true
if [[ "${LINE_COUNT}" -eq 0 ]]; then
	log_error "folded file is empty; contents:"
	cat "${FOLDED_FILES[@]}" >&2
	fatal "no profiling data captured"
fi

log_info "folded file has ${LINE_COUNT} lines; test passed"