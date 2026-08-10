#!/usr/bin/env bash

# Copyright 2026 The HuaTuo Authors.
# SPDX-License-Identifier: Apache-2.0

# Verify the native off-CPU profiler attributes blocked time to the CPU from
# which the task switched out. The assertion anchors on the process and blocked
# category because older kernels may truncate the userspace call chain.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

is_container && skip "native off-CPU PID filtering requires host PID namespace"

bpf_tool_setup profiler native_offcpu_profiler profiler-offcpu
readonly FIXTURE_SRC="${ROOT_DIR}/integration/testdata/test_profiler_offcpu.user.c"

command -v taskset > /dev/null || skip "taskset(1) not in PATH"

allowed_cpu_ids() {
	local allowed_list segment start end cpu
	allowed_list=$(sed -n 's/^Cpus_allowed_list:[[:space:]]*//p' /proc/self/status)
	[[ -n "${allowed_list}" ]] || return

	while IFS= read -r segment; do
		if [[ "${segment}" == *-* ]]; then
			start=${segment%-*}
			end=${segment#*-}
			for ((cpu = start; cpu <= end; cpu++)); do
				echo "${cpu}"
			done
		else
			echo "${segment}"
		fi
	done < <(tr ',' '\n' <<< "${allowed_list}")
}

mapfile -t CPU_IDS < <(allowed_cpu_ids)
[[ ${#CPU_IDS[@]} -ge 2 ]] || skip "need at least 2 CPUs in the current affinity set"
readonly SELECTED_CPU=${CPU_IDS[0]}
readonly EXCLUDED_CPU=${CPU_IDS[1]}

readonly PROFILER_DURATION=10
readonly PROFILER_AGGR_INTERVAL=5
WORK_DIR=${TOOL_WORK_DIR}
FIXTURE_BIN="${WORK_DIR}/offcpu-fixture"
TARGET_PID=""

cleanup() {
	[[ -n "${TARGET_PID}" ]] || return 0
	stop_by_pid "${TARGET_PID}" 1 || true
	wait "${TARGET_PID}" 2> /dev/null || true
}
trap cleanup EXIT

compile_user_fixture "${FIXTURE_SRC}" "${FIXTURE_BIN}"

verify_offcpu_cpuid() {
	local cpuid=$1 expected=$2
	local output_dir="${WORK_DIR}/cpu-${cpuid}"
	local tool_out="${output_dir}/profiler.out"
	local tool_err="${output_dir}/profiler.err"
	local blocked_prefix
	local match_count=0

	case "${expected}" in
	present | absent) ;;
	*) fatal "invalid expected result: ${expected}" ;;
	esac

	mkdir -p "${output_dir}"
	taskset -c "${SELECTED_CPU}" "${FIXTURE_BIN}" > /dev/null 2>&1 &
	TARGET_PID=$!
	blocked_prefix="process ${TARGET_PID}:offcpu-fixture;off-CPU blocked;"
	kill -0 "${TARGET_PID}" 2> /dev/null || fatal "fixture exited immediately (pid=${TARGET_PID})"

	log_info "fixture on CPU ${SELECTED_CPU}; profiler --cpuid ${cpuid}; expect ${expected} event"
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
		--log-level info \
		--log-file stdout \
		--output-format collapsed \
		--output-path "${output_dir}" \
		> "${tool_out}" 2> "${tool_err}"; then
		fatal "off-CPU profiler exited non-zero (see ${tool_err})"
	fi

	stop_by_pid "${TARGET_PID}" 1
	wait "${TARGET_PID}" 2> /dev/null || true
	TARGET_PID=""

	if compgen -G "${output_dir}/perf_*.folded" > /dev/null; then
		match_count=$(grep -hF "${blocked_prefix}" "${output_dir}"/perf_*.folded | wc -l) || true
	fi
	case "${expected}" in
	present)
		[[ "${match_count}" -gt 0 ]] || fatal "blocked event not found for CPU ${cpuid}"
		;;
	absent)
		[[ "${match_count}" -eq 0 ]] || fatal "blocked event unexpectedly found for CPU ${cpuid}"
		;;
	esac
}

verify_offcpu_cpuid "${SELECTED_CPU}" present
verify_offcpu_cpuid "${EXCLUDED_CPU}" absent
