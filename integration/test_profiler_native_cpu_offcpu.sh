#!/usr/bin/env bash

# Copyright 2026 The HuaTuo Authors.
# SPDX-License-Identifier: Apache-2.0

# Verify the native off-CPU profiler attributes blocked time to the CPU from
# which the task switched out. The assertion anchors on wait_loop because user
# stack depth varies by kernel and glibc build.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

is_container && skip "native off-CPU profiler requires scheduler tracepoint access"

readonly TOOL_BIN="${ROOT_DIR}/_output/bin/profiler"
readonly FIXTURE_SRC="${ROOT_DIR}/integration/testdata/test_profiler_offcpu.user.c"

command -v taskset > /dev/null || skip "taskset(1) not in PATH"
[[ -x "${TOOL_BIN}" ]] || fatal "profiler binary missing: ${TOOL_BIN}"
[[ -r "${ROOT_DIR}/_output/bpf/native_offcpu_profiler.o" ]] || fatal "native off-CPU bpf object missing"
[[ "$(nproc)" -ge 2 ]] || skip "need at least 2 CPUs"

readonly PROFILER_DURATION=10
readonly PROFILER_AGGR_INTERVAL=5
readonly BLOCKED_PATTERN='off-CPU blocked;.*;wait_loop'

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
taskset -c 0 "${FIXTURE_BIN}" > /dev/null 2>&1 &
TARGET_PID=$!
kill -0 "${TARGET_PID}" 2> /dev/null || fatal "fixture exited immediately (pid=${TARGET_PID})"

verify_offcpu_cpuid() {
	local cpuid=$1 expected=$2
	local output_dir="${WORK_DIR}/cpu-${cpuid}"
	local match_count=0
	mkdir -p "${output_dir}"

	log_info "running off-CPU profiler with --cpuid ${cpuid}, expect ${expected} stack"
	if ! "${TOOL_BIN}" \
		--type cpu \
		--language c \
		--cpu-mode offcpu \
		--offcpu-phase blocked \
		--offcpu-min-duration-us 100 \
		--cpuid "${cpuid}" \
		--pid "${TARGET_PID}" \
		--duration "${PROFILER_DURATION}" \
		--aggr-interval "${PROFILER_AGGR_INTERVAL}" \
		--output-format collapsed \
		--output-path "${output_dir}" \
		> "${TOOL_OUT}" 2> "${TOOL_ERR}"; then
		fatal "off-CPU profiler exited non-zero (see ${TOOL_ERR})"
	fi

	if compgen -G "${output_dir}/perf_*.folded" > /dev/null; then
		match_count=$(grep -hE "${BLOCKED_PATTERN}" "${output_dir}"/perf_*.folded | wc -l) || true
	fi
	case "${expected}" in
	present)
		[[ "${match_count}" -gt 0 ]] || fatal "blocking wait stack not found for CPU ${cpuid}"
		;;
	absent)
		[[ "${match_count}" -eq 0 ]] || fatal "blocking wait stack unexpectedly found for CPU ${cpuid}"
		;;
	esac
}

verify_offcpu_cpuid 0 present
verify_offcpu_cpuid 1 absent
