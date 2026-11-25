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

# Verify --cpu-list: fixture on same CPU → chain present; on different CPU → absent.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_profiler.sh"

require_native_pid_profiling

require_readable /sys/kernel/btf/vmlinux

# --- preconditions -----------------------------------------------------------

bpf_tool_setup profiler native_oncpu_profiler profiler-cpu-list
readonly PROFILER_CPU_LIST_BIN=${TOOL_BIN}
readonly PROFILER_CPU_LIST_FIXTURE_SRC="${ROOT_DIR}/integration/testdata/test_profiler_callchain.user.c"

require_commands taskset awk
[[ -r /proc/sys/kernel/perf_event_paranoid ]] || skip "perf_event_paranoid not readable: perf unavailable"
readonly PROFILER_CPU_LIST_PARANOID=$(cat /proc/sys/kernel/perf_event_paranoid)
[[ "${PROFILER_CPU_LIST_PARANOID}" -le 2 ]] || skip "kernel.perf_event_paranoid=${PROFILER_CPU_LIST_PARANOID} (>2) blocks perf sampling"

readonly PROFILER_CPU_LIST_CHAIN_PATTERN=';f1;f2;f3 [0-9]+$'

# --- workspace + cleanup -----------------------------------------------------

readonly PROFILER_CPU_LIST_OUTDIR=${TOOL_WORK_DIR}
profiler_cpu_list_fixture_bin="${PROFILER_CPU_LIST_OUTDIR}/callchain"
profiler_cpu_list_stdout="${PROFILER_CPU_LIST_OUTDIR}/profiler.out"
profiler_cpu_list_stderr="${PROFILER_CPU_LIST_OUTDIR}/profiler.err"
profiler_cpu_list_pid=""

cleanup() {
	if [[ -n "${profiler_cpu_list_pid}" ]]; then
		stop_by_pid "${profiler_cpu_list_pid}" 5 || true
		wait "${profiler_cpu_list_pid}" || true
	fi
}
trap cleanup EXIT

# --- build fixture -----------------------------------------------------------

compile_user_fixture "${PROFILER_CPU_LIST_FIXTURE_SRC}" "${profiler_cpu_list_fixture_bin}"

# --- helper ------------------------------------------------------------------

run_cpu_list_case() {
	local pin_cpu=$1 cpu_list=$2 expect_chain=$3
	local out_dir="${PROFILER_CPU_LIST_OUTDIR}/${pin_cpu}-${cpu_list}"
	local -a folded_files=()

	log_info "fixture on CPU${pin_cpu}, profiler --cpu-list ${cpu_list}, expect chain ${expect_chain}"
	mkdir -p "${out_dir}"

	taskset -c "${pin_cpu}" "${profiler_cpu_list_fixture_bin}" > /dev/null 2>&1 &
	profiler_cpu_list_pid=$!
	kill -0 "${profiler_cpu_list_pid}" 2> /dev/null || fatal "fixture exited immediately"

	"${PROFILER_CPU_LIST_BIN}" --type cpu --language c --pid "${profiler_cpu_list_pid}" \
		--cpu-list "${cpu_list}" --duration 10 --freq 99 \
		--output-format collapsed --output-path "${out_dir}" \
		--aggr-interval 5 \
		> "${profiler_cpu_list_stdout}" 2> "${profiler_cpu_list_stderr}" \
		|| fatal "profiler exited non-zero"

	stop_by_pid "${profiler_cpu_list_pid}" 5
	wait "${profiler_cpu_list_pid}" || true
	profiler_cpu_list_pid=""

	mapfile -t folded_files < <(find "${out_dir}" -maxdepth 1 -type f -name 'perf_*.folded')
	assert_folded_stack "${expect_chain}" "${PROFILER_CPU_LIST_CHAIN_PATTERN}" "${folded_files[@]}" \
		|| fatal "CPU ${pin_cpu}: unexpected stack for --cpu-list ${cpu_list}"
}

# --- tests -------------------------------------------------------------------

mapfile -t profiler_cpu_list_ids < <(profiler_allowed_cpu_ids)
[[ ${#profiler_cpu_list_ids[@]} -ge 2 ]] || skip "need at least 2 CPUs in the current affinity set"
run_cpu_list_case "${profiler_cpu_list_ids[0]}" "${profiler_cpu_list_ids[0]}" present
run_cpu_list_case "${profiler_cpu_list_ids[1]}" "${profiler_cpu_list_ids[0]}" absent
run_cpu_list_case "${profiler_cpu_list_ids[1]}" "${profiler_cpu_list_ids[0]}-${profiler_cpu_list_ids[1]}" present

log_info "all CPU list tests passed"
