#!/usr/bin/env bash

# Copyright 2026 The HuaTuo Authors.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

is_container && skip "native off-CPU profiler requires scheduler tracepoint access"

readonly TOOL_BIN="${ROOT_DIR}/_output/bin/profiler"
readonly FIXTURE_SRC="${ROOT_DIR}/integration/testdata/test_profiler_offcpu.user.c"

[[ -x "${TOOL_BIN}" ]] || fatal "profiler binary missing: ${TOOL_BIN}"
[[ -r "${ROOT_DIR}/_output/bpf/native_cpu_offcpu_profiler.o" ]] || fatal "native off-CPU bpf object missing"

readonly PROFILER_DURATION=10
readonly PROFILER_AGGR_INTERVAL=5
readonly BLOCKED_PATTERN='off-CPU blocked;.*;wait_loop;blocking_wait'

WORK_DIR=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/profiler-offcpu.XXXXXX")
TOOL_OUT="${WORK_DIR}/profiler.out"
TOOL_ERR="${WORK_DIR}/profiler.err"
FIXTURE_BIN="${WORK_DIR}/offcpu-fixture"
TARGET_PID=""

cleanup() {
	[[ -n "${TARGET_PID}" ]] && stop_by_pid "${TARGET_PID}" 5 || true
}
trap cleanup EXIT

compile_user_fixture "${FIXTURE_SRC}" "${FIXTURE_BIN}"
"${FIXTURE_BIN}" > /dev/null 2>&1 &
TARGET_PID=$!
kill -0 "${TARGET_PID}" 2> /dev/null || fatal "fixture exited immediately (pid=${TARGET_PID})"

log_info "running off-CPU profiler for ${PROFILER_DURATION}s against pid=${TARGET_PID}"
if ! "${TOOL_BIN}" \
	--type cpu \
	--language c \
	--cpu-mode offcpu \
	--offcpu-metric blocked \
	--offcpu-min-us 100 \
	--pid "${TARGET_PID}" \
	--duration "${PROFILER_DURATION}" \
	--aggr-interval "${PROFILER_AGGR_INTERVAL}" \
	--output-format collapsed \
	--output-path "${WORK_DIR}" \
	> "${TOOL_OUT}" 2> "${TOOL_ERR}"; then
	fatal "off-CPU profiler exited non-zero (see ${TOOL_ERR})"
fi

mapfile -t FOLDED_FILES < <(find "${WORK_DIR}" -maxdepth 1 -name 'perf_*.folded' -type f)
[[ ${#FOLDED_FILES[@]} -gt 0 ]] || fatal "no perf_*.folded file produced in ${WORK_DIR}"

MATCH_COUNT=$(grep -hE "${BLOCKED_PATTERN}" "${FOLDED_FILES[@]}" | wc -l) || true
if [[ "${MATCH_COUNT}" -eq 0 ]]; then
	fatal "blocking wait stack not found in off-CPU folded output"
fi

log_info "matched off-CPU blocking stacks: got ${MATCH_COUNT}, want >= 1"
