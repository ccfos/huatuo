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

# Verify --thread-group captures allocations performed by a non-main thread.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

is_container && skip "native memory profiler requires bare-metal cgroup/PMU access"

readonly TOOL_BIN="${ROOT_DIR}/_output/bin/profiler"
readonly FIXTURE_SRC="${ROOT_DIR}/integration/testdata/test_profiler_thread_group.user.c"
readonly EXPECTED_SYMBOL="do_mmap"
readonly PROFILER_DURATION=10
readonly PROFILER_AGGR_INTERVAL=5

[[ -x "${TOOL_BIN}" ]] || fatal "profiler binary missing: ${TOOL_BIN}"
[[ -r "${ROOT_DIR}/_output/bpf/native_virtual_alloc.o" ]] || fatal "native bpf object missing"

WORK_DIR=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/profiler-thread-group.XXXXXX")
TOOL_OUT="${WORK_DIR}/profiler.out"
TOOL_ERR="${WORK_DIR}/profiler.err"
FIXTURE_BIN="${WORK_DIR}/thread_group_workload"
TARGET_PID=""
PROFILER_PID=""

cleanup() {
	[[ -n "${PROFILER_PID}" ]] && stop_by_pid "${PROFILER_PID}" 5 || true
	[[ -n "${TARGET_PID}" ]] && stop_by_pid "${TARGET_PID}" 5 || true
}
trap cleanup EXIT

compile_user_fixture "${FIXTURE_SRC}" "${FIXTURE_BIN}" -pthread

"${FIXTURE_BIN}" > "${WORK_DIR}/fixture.out" 2> "${WORK_DIR}/fixture.err" &
TARGET_PID=$!
kill -0 "${TARGET_PID}" 2> /dev/null || fatal "fixture exited immediately (pid=${TARGET_PID})"

("${TOOL_BIN}" \
	--type memory \
	--language c \
	--memory-mode virtual_alloc \
	--thread-group \
	--pid "${TARGET_PID}" \
	--duration "${PROFILER_DURATION}" \
	--aggr-interval "${PROFILER_AGGR_INTERVAL}" \
	--output-format collapsed \
	--output-path "${WORK_DIR}" \
	--verbose \
	> "${TOOL_OUT}" 2> "${TOOL_ERR}") &
PROFILER_PID=$!

wait_until 15 1 profiler_ready "${TOOL_OUT}" || fatal "profiler did not start the read loop"
kill -USR1 "${TARGET_PID}" || fatal "failed to signal fixture pid=${TARGET_PID}"

wait "${PROFILER_PID}" || fatal "profiler exited non-zero (see ${TOOL_ERR})"
wait "${TARGET_PID}" || fatal "fixture exited non-zero"

mapfile -t FOLDED_FILES < <(find "${WORK_DIR}" -maxdepth 1 -name 'perf_*.folded' -type f)
[[ ${#FOLDED_FILES[@]} -gt 0 ]] || fatal "no perf_*.folded file produced"

grep -q "${EXPECTED_SYMBOL}" "${FOLDED_FILES[@]}" \
	|| fatal "kernel mmap symbol ${EXPECTED_SYMBOL} not found in profiler output"

log_info "captured worker-thread allocations with --thread-group"
