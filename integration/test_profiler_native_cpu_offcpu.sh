#!/usr/bin/env bash

# Copyright 2026 The HuaTuo Authors.
# SPDX-License-Identifier: Apache-2.0

# Verify the native off-CPU profiler attributes blocked time to the CPU from
# which the task switched out. The assertion anchors on wait_loop because user
# stack depth varies by kernel and glibc build.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

is_container && skip "native off-CPU PID filtering requires host PID namespace"

readonly TOOL_BIN="${ROOT_DIR}/_output/bin/profiler"
readonly FIXTURE_SRC="${ROOT_DIR}/integration/testdata/test_profiler_offcpu.user.c"

command -v taskset > /dev/null || skip "taskset(1) not in PATH"
[[ -x "${TOOL_BIN}" ]] || fatal "profiler binary missing: ${TOOL_BIN}"
[[ -r "${ROOT_DIR}/_output/bpf/native_offcpu_profiler.o" ]] || fatal "native off-CPU bpf object missing"

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
readonly BLOCKED_PATTERN='off-CPU blocked;.*;wait_loop'

WORK_DIR=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/profiler-offcpu.XXXXXX")
FIXTURE_BIN="${WORK_DIR}/offcpu-fixture"
TARGET_PID=""

cleanup() {
	[[ -n "${TARGET_PID}" ]] || return
	stop_by_pid "${TARGET_PID}" 1 || true
	wait "${TARGET_PID}" 2> /dev/null || true
}
trap cleanup EXIT

compile_user_fixture "${FIXTURE_SRC}" "${FIXTURE_BIN}"

read_offcpu_stat() {
	local log_file=$1 stat_name=$2
	sed -nE "s/.*off-CPU stats:.*${stat_name}=([0-9]+).*/\\1/p" "${log_file}" | tail -n 1
}

verify_offcpu_cpuid() {
	local cpuid=$1 expected=$2
	local output_dir="${WORK_DIR}/cpu-${cpuid}"
	local tool_out="${output_dir}/profiler.out"
	local tool_err="${output_dir}/profiler.err"
	local match_count=0
	local tracked blocked_emitted

	case "${expected}" in
	present | absent) ;;
	*) fatal "invalid expected result: ${expected}" ;;
	esac

	mkdir -p "${output_dir}"
	taskset -c "${SELECTED_CPU}" "${FIXTURE_BIN}" > /dev/null 2>&1 &
	TARGET_PID=$!
	kill -0 "${TARGET_PID}" 2> /dev/null || fatal "fixture exited immediately (pid=${TARGET_PID})"

	log_info "fixture on CPU ${SELECTED_CPU}; profiler --cpuid ${cpuid}; expect ${expected} stack"
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

	tracked=$(read_offcpu_stat "${tool_out}" tracked)
	blocked_emitted=$(read_offcpu_stat "${tool_out}" blocked_emitted)
	[[ "${tracked}" =~ ^[0-9]+$ ]] || fatal "off-CPU tracked stat missing for CPU ${cpuid}"
	[[ "${blocked_emitted}" =~ ^[0-9]+$ ]] || fatal "off-CPU blocked_emitted stat missing for CPU ${cpuid}"

	if compgen -G "${output_dir}/perf_*.folded" > /dev/null; then
		match_count=$(grep -hE "${BLOCKED_PATTERN}" "${output_dir}"/perf_*.folded | wc -l) || true
	fi
	case "${expected}" in
	present)
		[[ "${tracked}" -gt 0 ]] || fatal "no off-CPU intervals tracked for CPU ${cpuid}"
		[[ "${blocked_emitted}" -gt 0 ]] || fatal "no blocked events emitted for CPU ${cpuid}"
		[[ "${match_count}" -gt 0 ]] || fatal "blocking wait stack not found for CPU ${cpuid}"
		;;
	absent)
		[[ "${tracked}" -eq 0 ]] || fatal "off-CPU intervals unexpectedly tracked for CPU ${cpuid}"
		[[ "${blocked_emitted}" -eq 0 ]] || fatal "blocked events unexpectedly emitted for CPU ${cpuid}"
		[[ "${match_count}" -eq 0 ]] || fatal "blocking wait stack unexpectedly found for CPU ${cpuid}"
		;;
	esac
}

verify_offcpu_cpuid "${SELECTED_CPU}" present
verify_offcpu_cpuid "${EXCLUDED_CPU}" absent
