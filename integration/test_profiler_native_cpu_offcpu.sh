#!/usr/bin/env bash

# Copyright 2026 The HuaTuo Authors.
# SPDX-License-Identifier: Apache-2.0

# Verify the native off-CPU profiler attributes blocked time to the CPU from
# which the task switched out. The assertion anchors on the process and blocked
# reason because older kernels may truncate the userspace call chain.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_profiler.sh"

require_native_pid_profiling

require_readable /sys/kernel/btf/vmlinux

bpf_tool_setup profiler native_offcpu_profiler profiler-offcpu
readonly PROFILER_FIXTURE_SRC="${ROOT_DIR}/integration/testdata/test_profiler_offcpu.user.c"

require_commands taskset

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

mapfile -t profiler_cpu_ids < <(allowed_cpu_ids)
[[ ${#profiler_cpu_ids[@]} -ge 2 ]] || skip "need at least 2 CPUs in the current affinity set"
readonly PROFILER_SELECTED_CPU=${profiler_cpu_ids[0]}
readonly PROFILER_EXCLUDED_CPU=${profiler_cpu_ids[1]}

readonly PROFILER_DURATION=10
readonly PROFILER_AGGR_INTERVAL=5
profiler_work_dir=${TOOL_WORK_DIR}
profiler_fixture_bin="${profiler_work_dir}/offcpu-fixture"
profiler_target_pid=""

cleanup() {
	[[ -n "${profiler_target_pid}" ]] || return 0
	stop_by_pid "${profiler_target_pid}" 1 || true
	wait "${profiler_target_pid}" 2> /dev/null || true
}
trap cleanup EXIT

compile_user_fixture "${PROFILER_FIXTURE_SRC}" "${profiler_fixture_bin}"

assert_profiler_offcpu_cpuid() {
	local cpuid=$1 expected=$2
	local output_dir="${profiler_work_dir}/cpu-${cpuid}"
	local tool_out="${output_dir}/profiler.out"
	local tool_err="${output_dir}/profiler.err"
	local blocked_prefix
	local match_count=0

	case "${expected}" in
	present | absent) ;;
	*) fatal "invalid expected result: ${expected}" ;;
	esac

	mkdir -p "${output_dir}"
	taskset -c "${PROFILER_SELECTED_CPU}" "${profiler_fixture_bin}" > /dev/null 2>&1 &
	profiler_target_pid=$!
	blocked_prefix="comm=\"offcpu-fixture\";offcpu_reason=\"blocked\";pid=\"${profiler_target_pid}\";"
	kill -0 "${profiler_target_pid}" 2> /dev/null || fatal "fixture exited immediately (pid=${profiler_target_pid})"

	log_info "fixture on CPU ${PROFILER_SELECTED_CPU}; profiler --cpuid ${cpuid}; expect ${expected} event"
	if ! "${TOOL_BIN}" \
		--type cpu \
		--language c \
		--cpu-mode offcpu \
		--offcpu-phase blocked \
		--offcpu-min-duration-us 100 \
		--cpuid "${cpuid}" \
		--pid "${profiler_target_pid}" \
		--duration "${PROFILER_DURATION}" \
		--aggr-interval "${PROFILER_AGGR_INTERVAL}" \
		--log-level info \
		--log-file stdout \
		--output-format collapsed \
		--output-path "${output_dir}" \
		> "${tool_out}" 2> "${tool_err}"; then
		fatal "off-CPU profiler exited non-zero (see ${tool_err})"
	fi

	stop_by_pid "${profiler_target_pid}" 1
	wait "${profiler_target_pid}" 2> /dev/null || true
	profiler_target_pid=""

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

assert_profiler_offcpu_cpuid "${PROFILER_SELECTED_CPU}" present
assert_profiler_offcpu_cpuid "${PROFILER_EXCLUDED_CPU}" absent
