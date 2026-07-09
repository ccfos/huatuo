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

# Expected allocation sizes from test_profiler_mmap.user.c
# Total: 4096 + 16384 + 65536 + 262144 = 348160 bytes per iteration
readonly EXPECTED_ALLOC_SIZE_1=4096
readonly EXPECTED_ALLOC_SIZE_2=16384
readonly EXPECTED_ALLOC_SIZE_3=65536
readonly EXPECTED_ALLOC_SIZE_4=262144
readonly EXPECTED_TOTAL_PER_ITER=$((EXPECTED_ALLOC_SIZE_1 + EXPECTED_ALLOC_SIZE_2 + EXPECTED_ALLOC_SIZE_3 + EXPECTED_ALLOC_SIZE_4))

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

# --- verify expected symbol appears in output --------------------------------

log_info "checking for expected symbol 'test_mmap_allocator' in profiler output"

# Concatenate all folded files for symbol search
ALL_FOLDED="${WORK_DIR}/all_folded.txt"
cat "${FOLDED_FILES[@]}" > "${ALL_FOLDED}"

if ! grep -q "test_mmap_allocator" "${ALL_FOLDED}"; then
	log_error "expected symbol 'test_mmap_allocator' not found in profiler output"
	log_error "folded file contents:"
	cat "${FOLDED_FILES[@]}" >&2
	fatal "symbol verification failed"
fi

log_info "found expected symbol 'test_mmap_allocator'"

# --- verify memory values match expected allocations -------------------------

log_info "verifying memory allocation values"

# Extract lines containing test_mmap_allocator and sum up the bytes
# Format: stack_trace;bytes
TOTAL_CAPTURED_BYTES=0
while IFS= read -r line; do
	# Extract the byte count (last field after semicolon)
	bytes=$(echo "${line}" | rev | cut -d';' -f1 | rev)
	if [[ "${bytes}" =~ ^[0-9]+$ ]]; then
		TOTAL_CAPTURED_BYTES=$((TOTAL_CAPTURED_BYTES + bytes))
	fi
done < <(grep "test_mmap_allocator" "${ALL_FOLDED}")

if [[ ${TOTAL_CAPTURED_BYTES} -eq 0 ]]; then
	log_error "no memory bytes captured for test_mmap_allocator"
	fatal "memory verification failed"
fi

# Calculate expected total: iterations * bytes per iteration
# With 10ms sleep per iteration and 10s duration, expect ~1000 iterations
# Each iteration allocates EXPECTED_TOTAL_PER_ITER bytes
# Allow some tolerance since profiler may miss some events
MIN_EXPECTED_BYTES=$((EXPECTED_TOTAL_PER_ITER * 100)) # At least 100 iterations worth

log_info "captured ${TOTAL_CAPTURED_BYTES} bytes for test_mmap_allocator (min expected: ${MIN_EXPECTED_BYTES})"

if [[ ${TOTAL_CAPTURED_BYTES} -lt ${MIN_EXPECTED_BYTES} ]]; then
	log_error "captured memory (${TOTAL_CAPTURED_BYTES} bytes) is less than expected minimum (${MIN_EXPECTED_BYTES} bytes)"
	fatal "memory verification failed"
fi

# Verify individual allocation sizes appear in output (with tolerance for page alignment)
# The profiler should capture allocations of 4096, 16384, 65536, 262144 bytes
# Check that at least some of these sizes appear
FOUND_SIZE_COUNT=0
for expected_size in ${EXPECTED_ALLOC_SIZE_1} ${EXPECTED_ALLOC_SIZE_2} ${EXPECTED_ALLOC_SIZE_3} ${EXPECTED_ALLOC_SIZE_4}; do
	# Look for patterns like ";${expected_size}" at end of line or followed by non-digit
	if grep -qE ";${expected_size}([^0-9]|\$)" "${ALL_FOLDED}"; then
		FOUND_SIZE_COUNT=$((FOUND_SIZE_COUNT + 1))
		log_info "found allocation size ${expected_size} in output"
	fi
done

if [[ ${FOUND_SIZE_COUNT} -lt 2 ]]; then
	log_error "expected at least 2 distinct allocation sizes, found ${FOUND_SIZE_COUNT}"
	fatal "allocation size verification failed"
fi

log_info "folded file has ${LINE_COUNT} lines; test passed"
log_info "total captured bytes: ${TOTAL_CAPTURED_BYTES}; found ${FOUND_SIZE_COUNT}/4 expected sizes"
