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
readonly FIXTURE_SRC="${ROOT_DIR}/integration/testdata/test_profiler_mmap.user.c"

command -v gcc > /dev/null || skip "gcc(1) not in PATH"
[[ -x "${TOOL_BIN}" ]] || fatal "profiler binary missing: ${TOOL_BIN}"
[[ -r "${ROOT_DIR}/_output/bpf/native_virtual_alloc.o" ]] || fatal "native bpf object missing"
[[ -r "${FIXTURE_SRC}" ]] || fatal "fixture source missing: ${FIXTURE_SRC}"

# --- tunables ----------------------------------------------------------------

readonly PROFILER_DURATION=10
readonly PROFILER_AGGR_INTERVAL=5
readonly PROFILER_READY_TIMEOUT=15
readonly PROFILER_READY_INTERVAL=1

readonly USER_MARKER_SYMBOL="test_alloc_free_loop"
readonly KERNEL_MMAP_SYMBOL="do_mmap"
EXPECTED_SYMBOL="${USER_MARKER_SYMBOL}"

if kernel_version_le 5 4; then
	# Linux 5.4 and older can return shallow BPF user stacks from kprobe/do_mmap.
	# Validate the kernel hook instead of requiring a specific user-space frame.
	EXPECTED_SYMBOL="${KERNEL_MMAP_SYMBOL}"
fi
readonly EXPECTED_SYMBOL

# --- workspace + cleanup -----------------------------------------------------

WORK_DIR=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/profiler-mem.XXXXXX")
TOOL_OUT="${WORK_DIR}/profiler.out"
TOOL_ERR="${WORK_DIR}/profiler.err"
FIXTURE_BIN="${WORK_DIR}/mmap_workload"
FIXTURE_OUT="${WORK_DIR}/mmap.out"
FIXTURE_ERR="${WORK_DIR}/mmap.err"
TARGET_PID=""
PROFILER_PID=""

cleanup() {
	[[ -n "${PROFILER_PID}" ]] && stop_by_pid "${PROFILER_PID}" 5 || true
	[[ -n "${TARGET_PID}" ]] && stop_by_pid "${TARGET_PID}" 5 || true
}
trap cleanup EXIT

# --- build fixture -----------------------------------------------------------

log_info "compiling fixture: $(basename "${FIXTURE_SRC}")"
gcc -O0 -g -fno-inline -fno-omit-frame-pointer \
	-o "${FIXTURE_BIN}" "${FIXTURE_SRC}" \
	2> "${WORK_DIR}/gcc.err" \
	|| fatal "gcc failed:"$'\n'"$(< "${WORK_DIR}/gcc.err")"

# --- launch target -----------------------------------------------------------

log_info "launching target and waiting for SIGUSR1"
"${FIXTURE_BIN}" > "${FIXTURE_OUT}" 2> "${FIXTURE_ERR}" &
TARGET_PID=$!
kill -0 "${TARGET_PID}" 2> /dev/null || fatal "fixture exited immediately (pid=${TARGET_PID})"

log_info "target pid=${TARGET_PID}"

# --- run profiler ------------------------------------------------------------

log_info "running profiler for ${PROFILER_DURATION}s with --memory-mode virtual_alloc against pid=${TARGET_PID}"
("${TOOL_BIN}" \
	--type mem \
	--language c \
	--memory-mode virtual_alloc \
	--pid "${TARGET_PID}" \
	--duration "${PROFILER_DURATION}" \
	--output-format collapsed \
	--output-path "${WORK_DIR}" \
	--aggr-interval "${PROFILER_AGGR_INTERVAL}" \
	--verbose \
	> "${TOOL_OUT}" 2> "${TOOL_ERR}") &
PROFILER_PID=$!
kill -0 "${PROFILER_PID}" 2> /dev/null || fatal "failed to launch profiler"

wait_until "${PROFILER_READY_TIMEOUT}" "${PROFILER_READY_INTERVAL}" \
	"profiler ready" \
	profiler_ready "${TOOL_OUT}" || fatal "profiler did not start the read loop"

log_info "sending SIGUSR1 to target pid=${TARGET_PID}"
kill -USR1 "${TARGET_PID}" || fatal "failed to signal fixture pid=${TARGET_PID}"

if ! wait "${PROFILER_PID}"; then
	fatal "profiler exited non-zero (see ${TOOL_ERR})"
fi

if ! wait "${TARGET_PID}"; then
	fatal "fixture exited non-zero (see ${FIXTURE_ERR})"
fi

# --- assert ------------------------------------------------------------------

mapfile -t FOLDED_FILES < <(find "${WORK_DIR}" -maxdepth 1 -name 'perf_*.folded' -type f)
[[ ${#FOLDED_FILES[@]} -gt 0 ]] || fatal "no perf_*.folded file produced in ${WORK_DIR}"

log_info "found ${#FOLDED_FILES[@]} folded file(s); asserting non-empty output"

# Check that we have at least some profiling data
LINE_COUNT=$(wc -l < "${FOLDED_FILES[0]}") || true
if [[ "${LINE_COUNT}" -eq 0 ]]; then
	fatal "no profiling data captured"
fi

# --- verify expected symbol appears in output --------------------------------

log_info "checking for expected symbol '${EXPECTED_SYMBOL}' in profiler output"

if ! grep -q "${EXPECTED_SYMBOL}" "${FOLDED_FILES[@]}"; then
	fatal "expected symbol '${EXPECTED_SYMBOL}' not found in profiler output"
fi

log_info "found expected symbol '${EXPECTED_SYMBOL}'"

# --- verify memory values match expected allocations -------------------------

log_info "verifying memory allocation values"

# Extract lines containing the expected symbol and sum up the bytes.
# The folded output stores the byte count as the last whitespace-delimited field.
TOTAL_CAPTURED_BYTES=0
while IFS= read -r line; do
	bytes=$(awk '{print $NF}' <<< "${line}")
	if [[ "${bytes}" =~ ^[0-9]+$ ]]; then
		TOTAL_CAPTURED_BYTES=$((TOTAL_CAPTURED_BYTES + bytes))
	fi
done < <(grep "${EXPECTED_SYMBOL}" "${FOLDED_FILES[@]}")

if [[ ${TOTAL_CAPTURED_BYTES} -eq 0 ]]; then
	log_error "no memory bytes captured for ${EXPECTED_SYMBOL}"
	fatal "memory verification failed"
fi

ACTUAL_ALLOCATED_BYTES=$(awk -F= '/^actual_allocated_bytes=/{value=$2} END {print value}' "${FIXTURE_ERR}")
if [[ ! "${ACTUAL_ALLOCATED_BYTES}" =~ ^[0-9]+$ ]]; then
	fatal "missing actual_allocated_bytes in fixture stderr"
fi

log_info "fixture reported ${ACTUAL_ALLOCATED_BYTES} bytes"

if [[ ${TOTAL_CAPTURED_BYTES} -ne ${ACTUAL_ALLOCATED_BYTES} ]]; then
	log_error "captured memory (${TOTAL_CAPTURED_BYTES} bytes) does not match actual allocated bytes (${ACTUAL_ALLOCATED_BYTES})"
	fatal "memory verification failed"
fi

log_info "folded file has ${LINE_COUNT} lines; test passed"
log_info "total captured bytes: ${TOTAL_CAPTURED_BYTES}"
