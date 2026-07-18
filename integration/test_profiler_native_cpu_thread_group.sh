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

# Verify --thread-group controls whether native CPU profiling includes workers.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

is_container && skip "native CPU profiler requires bare-metal cgroup/PMU access"

readonly TOOL_BIN="${ROOT_DIR}/_output/bin/profiler"
readonly FIXTURE_SRC="${ROOT_DIR}/integration/testdata/test_profiler_cpu_thread_group.user.c"
readonly WORKER_SYMBOL="thread_group_cpu_loop"
readonly PROFILER_DURATION=5
readonly PROFILER_FREQ=99
readonly PROFILER_AGGR_INTERVAL=2

[[ -x "${TOOL_BIN}" ]] || fatal "profiler binary missing: ${TOOL_BIN}"
[[ -r "${ROOT_DIR}/_output/bpf/native_cpu_profiler.o" ]] || fatal "native bpf object missing"
[[ -r /proc/sys/kernel/perf_event_paranoid ]] || skip "perf_event_paranoid not readable: perf unavailable"
readonly PARANOID=$(cat /proc/sys/kernel/perf_event_paranoid)
[[ "${PARANOID}" -le 2 ]] || skip "kernel.perf_event_paranoid=${PARANOID} (>2) blocks perf sampling"

WORK_DIR=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/profiler-cpu-thread-group.XXXXXX")
FIXTURE_BIN="${WORK_DIR}/thread_group_workload"
TARGET_PID=""
PROFILER_PID=""

cleanup() {
	[[ -n "${PROFILER_PID}" ]] && stop_by_pid "${PROFILER_PID}" 5 || true
	[[ -n "${TARGET_PID}" ]] && stop_by_pid "${TARGET_PID}" 5 || true
}
trap cleanup EXIT

compile_user_fixture "${FIXTURE_SRC}" "${FIXTURE_BIN}" -pthread

run_case() {
	local name=$1
	local expect_worker=$2
	local case_dir="${WORK_DIR}/${name}"
	local tool_out="${case_dir}/profiler.out"
	local tool_err="${case_dir}/profiler.err"
	local -a thread_group_arg=()
	local -a folded_files=()

	mkdir -p "${case_dir}"
	if [[ "${expect_worker}" == "yes" ]]; then
		thread_group_arg=(--thread-group)
	fi

	"${FIXTURE_BIN}" >"${case_dir}/fixture.out" 2>"${case_dir}/fixture.err" &
	TARGET_PID=$!
	kill -0 "${TARGET_PID}" 2>/dev/null || fatal "fixture exited immediately (pid=${TARGET_PID})"

	("${TOOL_BIN}" \
		--type cpu \
		--language c \
		--pid "${TARGET_PID}" \
		--duration "${PROFILER_DURATION}" \
		--freq "${PROFILER_FREQ}" \
		--aggr-interval "${PROFILER_AGGR_INTERVAL}" \
		--output-format collapsed \
		--output-path "${case_dir}" \
		--verbose \
		"${thread_group_arg[@]}" \
		>"${tool_out}" 2>"${tool_err}") &
	PROFILER_PID=$!

	wait_until 15 1 profiler_ready "${tool_out}" || fatal "profiler did not start the read loop"
	kill -USR1 "${TARGET_PID}" || fatal "failed to signal fixture pid=${TARGET_PID}"
	wait "${PROFILER_PID}" || fatal "profiler exited non-zero (see ${tool_err})"
	PROFILER_PID=""
	wait "${TARGET_PID}" || fatal "fixture exited non-zero"
	TARGET_PID=""

	mapfile -t folded_files < <(find "${case_dir}" -maxdepth 1 -name 'perf_*.folded' -type f)
	if [[ "${expect_worker}" == "yes" ]]; then
		[[ ${#folded_files[@]} -gt 0 ]] || fatal "no perf_*.folded file produced"
		grep -q "${WORKER_SYMBOL}" "${folded_files[@]}" ||
			fatal "worker symbol ${WORKER_SYMBOL} not found with --thread-group"
	elif [[ ${#folded_files[@]} -gt 0 ]] && grep -q "${WORKER_SYMBOL}" "${folded_files[@]}"; then
		fatal "worker symbol ${WORKER_SYMBOL} captured without --thread-group"
	fi
}

run_case "target-thread" "no"
run_case "thread-group" "yes"

log_info "native CPU thread-group filtering matched expected behavior"
