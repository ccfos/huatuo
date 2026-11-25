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
profiler_collector_pid=""

cleanup() {
	[[ -z "${profiler_collector_pid}" ]] || stop_and_wait_by_pid "${profiler_collector_pid}" 5 || true
	[[ -n "${profiler_target_pid}" ]] || return 0
	kill -CONT "${profiler_target_pid}" 2> /dev/null || true
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
	local blocked_prefix process_prefix
	local match_count=0

	case "${expected}" in
	present | absent) ;;
	*) fatal "invalid expected result: ${expected}" ;;
	esac

	mkdir -p "${output_dir}"
	taskset -c "${PROFILER_SELECTED_CPU}" "${profiler_fixture_bin}" > /dev/null 2>&1 &
	profiler_target_pid=$!
	process_prefix="comm=\"offcpu-fixture\";cpu=\"${PROFILER_SELECTED_CPU}\";"
	blocked_prefix="${process_prefix}offcpu_reason=\"blocked\";pid=\"${profiler_target_pid}\";"
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

	if compgen -G "${output_dir}/perf_*.folded" > /dev/null; then
		match_count=$(grep -hF "${blocked_prefix}" "${output_dir}"/perf_*.folded | wc -l) || true
	fi
	case "${expected}" in
	present)
		[[ "${match_count}" -gt 0 ]] || fatal "blocked event not found for CPU ${cpuid}"
		;;
	absent)
		if compgen -G "${output_dir}/perf_*.folded" > /dev/null; then
			! grep -hF "pid=\"${profiler_target_pid}\";" "${output_dir}"/perf_*.folded \
				|| fatal "event unexpectedly found for excluded CPU ${cpuid}"
		fi
		;;
	esac
	profiler_target_pid=""
}

assert_profiler_offcpu_cpuid "${PROFILER_SELECTED_CPU}" present
assert_profiler_offcpu_cpuid "${PROFILER_EXCLUDED_CPU}" absent

profiler_migration_stopped() {
	local observations=$1
	[[ $(wc -l < "${profiler_work_dir}/migration.cpus") -ge ${observations} ]] \
		&& [[ $(sed -n 's/^State:[[:space:]]*\([A-Z]\).*/\1/p' "/proc/${profiler_target_pid}/status") == T ]]
}

assert_profiler_offcpu_migration() {
	local output_dir="${profiler_work_dir}/migration" reason prefix status=0
	mkdir -p "${output_dir}"
	taskset -c "${PROFILER_SELECTED_CPU}" "${profiler_fixture_bin}" --migrate \
		> "${profiler_work_dir}/migration.cpus" 2> "${output_dir}/fixture.err" &
	profiler_target_pid=$!
	wait_until 5 0.1 profiler_migration_stopped 1 || fatal "migration fixture did not stop initially"

	"${TOOL_BIN}" --type cpu --language c --cpu-mode offcpu --offcpu-phase all \
		--offcpu-min-duration-us 0 --cpuid "${PROFILER_SELECTED_CPU}" --pid "${profiler_target_pid}" \
		--duration "${PROFILER_DURATION}" --aggr-interval "${PROFILER_AGGR_INTERVAL}" \
		--log-level debug --log-file stdout --output-format collapsed --output-path "${output_dir}" \
		> "${output_dir}/profiler.out" 2> "${output_dir}/profiler.err" &
	profiler_collector_pid=$!
	wait_until 10 0.1 profiler_read_loop_started "${output_dir}/profiler.out" \
		|| fatal "migration profiler did not become ready; see ${output_dir}/profiler.out"

	kill -CONT "${profiler_target_pid}"
	wait_until 5 0.1 profiler_migration_stopped 2 || fatal "fixture did not switch out on source CPU"
	taskset -pc "${PROFILER_EXCLUDED_CPU}" "${profiler_target_pid}"
	kill -CONT "${profiler_target_pid}"
	wait_until 5 0.1 profiler_migration_stopped 3 || fatal "fixture did not resume on destination CPU"
	assert_eq "$(sed -n '2p' "${profiler_work_dir}/migration.cpus")" "${PROFILER_SELECTED_CPU}" \
		"switch-out CPU" || fatal "fixture did not run on source CPU"
	assert_eq "$(sed -n '3p' "${profiler_work_dir}/migration.cpus")" "${PROFILER_EXCLUDED_CPU}" \
		"resume CPU" || fatal "fixture did not migrate"
	kill -CONT "${profiler_target_pid}"
	wait "${profiler_target_pid}" || fatal "migration fixture failed"
	# Preserve the PID until sample assertions have matched this process.
	wait_until 20 0.1 process_exited "${profiler_collector_pid}" \
		|| fatal "migration profiler did not exit; see ${output_dir}/profiler.err"
	wait "${profiler_collector_pid}" || status=$?
	profiler_collector_pid=""
	[[ ${status} -eq 0 ]] || fatal "migration profiler exited ${status}; see ${output_dir}/profiler.err"
	for reason in blocked runqueue; do
		prefix="comm=\"offcpu-fixture\";cpu=\"${PROFILER_SELECTED_CPU}\";offcpu_reason=\"${reason}\";pid=\"${profiler_target_pid}\";"
		grep -hF "${prefix}" "${output_dir}"/perf_*.folded \
			|| fatal "missing ${reason} sample attributed to switch-out CPU; see ${output_dir}"
	done
	profiler_target_pid=""
}

assert_profiler_offcpu_migration
