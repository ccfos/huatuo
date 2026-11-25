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

# Verify --cpuid: fixture on same CPU → chain present; on different CPU → absent.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_profiler.sh"

require_native_pid_profiling

require_readable /sys/kernel/btf/vmlinux

# --- preconditions -----------------------------------------------------------

bpf_tool_setup profiler native_oncpu_profiler profiler-cpuid
readonly PROFILER_BIN=${TOOL_BIN}
readonly FIXTURE_SRC="${ROOT_DIR}/integration/testdata/test_profiler_callchain.user.c"

require_commands taskset
[[ -r /proc/sys/kernel/perf_event_paranoid ]] || skip "perf_event_paranoid not readable: perf unavailable"
readonly PARANOID=$(cat /proc/sys/kernel/perf_event_paranoid)
[[ "${PARANOID}" -le 2 ]] || skip "kernel.perf_event_paranoid=${PARANOID} (>2) blocks perf sampling"
[[ "$(nproc)" -ge 2 ]] || skip "need at least 2 CPUs"

readonly CHAIN_PATTERN=';f1;f2;f3 [0-9]+$'

# --- workspace + cleanup -----------------------------------------------------

readonly FIXTURE_OUTDIR=${TOOL_WORK_DIR}
FIXTURE_BIN="${FIXTURE_OUTDIR}/callchain"
PROFILER_OUT="${FIXTURE_OUTDIR}/profiler.out"
PROFILER_ERR="${FIXTURE_OUTDIR}/profiler.err"
TARGET_PID=""

cleanup() {
	[[ -n "${TARGET_PID}" ]] && stop_by_pid "${TARGET_PID}" 5 || true
}
trap cleanup EXIT

# --- build fixture -----------------------------------------------------------

compile_user_fixture "${FIXTURE_SRC}" "${FIXTURE_BIN}"

# --- helper ------------------------------------------------------------------

# run_cpuid_case <pin_cpu> <cpuid> <expect_chain>
#   pin_cpu:       CPU to pin the fixture process to
#   cpuid:         --cpuid value passed to the profiler
#   expect_chain:  "present" or "absent"
run_cpuid_case() {
	local pin_cpu=$1 cpuid=$2 expect_chain=$3
	local out_dir="${FIXTURE_OUTDIR}/${pin_cpu}"
	local -a folded_files=()

	log_info "fixture on CPU${pin_cpu}, profiler --cpuid ${cpuid}, expect chain ${expect_chain}"
	mkdir -p "${out_dir}"

	taskset -c "${pin_cpu}" "${FIXTURE_BIN}" > /dev/null 2>&1 &
	TARGET_PID=$!
	kill -0 "${TARGET_PID}" 2> /dev/null || fatal "fixture exited immediately"

	"${PROFILER_BIN}" --type cpu --language c --pid "${TARGET_PID}" \
		--cpuid "${cpuid}" --duration 10 --freq 99 \
		--output-format collapsed --output-path "${out_dir}" \
		--aggr-interval 5 \
		> "${PROFILER_OUT}" 2> "${PROFILER_ERR}" \
		|| fatal "profiler exited non-zero"

	stop_by_pid "${TARGET_PID}" 5
	TARGET_PID=""

	mapfile -t folded_files < <(find "$out_dir" -maxdepth 1 -type f -name 'perf_*.folded')
	assert_folded_stack "$expect_chain" "$CHAIN_PATTERN" "${folded_files[@]}" \
		|| fatal "CPU ${pin_cpu}: unexpected stack for --cpuid ${cpuid}"

}

# --- tests -------------------------------------------------------------------

run_cpuid_case 0 0 present
run_cpuid_case 1 0 absent

log_info "all cpuid tests passed"
