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

# Exercise Go symbol resolution through real sampling, including relocated and
# stripped pclntab metadata, while keeping worker goroutines off the leader TID.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

is_container && skip "native CPU profiler requires bare-metal cgroup/PMU access"
require_commands go
bpf_tool_setup profiler native_oncpu_profiler profiler-go-cpu

[[ -r /proc/sys/kernel/perf_event_paranoid ]] \
	|| skip "perf_event_paranoid not readable: perf unavailable"
readonly GO_CPU_PARANOID=$(cat /proc/sys/kernel/perf_event_paranoid)
if [[ "${GO_CPU_PARANOID}" -gt 2 ]]; then
	# CAP_PERFMON (38) or CAP_SYS_ADMIN (21) bypasses perf_event_paranoid.
	# Do not skip privileged runners or weaken the host's sysctl to run tests.
	readonly GO_CPU_CAP_EFF=$(awk '$1 == "CapEff:" { print $2 }' /proc/self/status)
	(((16#${GO_CPU_CAP_EFF} & ((1 << 38) | (1 << 21))) != 0)) \
		|| skip "kernel.perf_event_paranoid=${GO_CPU_PARANOID} requires CAP_PERFMON or CAP_SYS_ADMIN"
fi

readonly GO_CPU_FIXTURE_SRC="${ROOT_DIR}/integration/testdata/test_profiler_go_cpu.go"
readonly GO_CPU_CHAIN=';main\.goCPUWorker;main\.goCPUMiddle;main\.goCPUHot [1-9][0-9]*$'
readonly GO_CPU_WORKER=';main\.goCPUWorker(;| )'
go_cpu_target_pid=""

cleanup() {
	[[ -n "${go_cpu_target_pid}" ]] && stop_by_pid "${go_cpu_target_pid}" 5 || true
}
trap cleanup EXIT

go_cpu_fixture_ready() {
	local output=$1
	kill -0 "${go_cpu_target_pid}" 2> /dev/null || return 1
	grep -qE "^ready main=${go_cpu_target_pid} worker=[0-9]+ worker=[0-9]+$" "${output}"
}

run_go_cpu_profile() {
	local mode=$1 scope=$2
	local case_dir="${TOOL_WORK_DIR}/${mode}/${scope}"
	local -a thread_group_args=() folded_files=()
	[[ "${scope}" != thread-group ]] || thread_group_args=(--thread-group)

	mkdir -p "${case_dir}"
	log_info "profiling Go ${mode} ${scope}, pid=${go_cpu_target_pid}"
	if ! "${TOOL_BIN}" \
		--type cpu \
		--language go \
		--pid "${go_cpu_target_pid}" \
		--duration 10 \
		--freq 99 \
		--aggr-interval 5 \
		--output-format collapsed \
		--output-path "${case_dir}" \
		--verbose \
		"${thread_group_args[@]}" \
		> "${case_dir}/profiler.out" 2> "${case_dir}/profiler.err"; then
		fatal "Go ${mode} ${scope} profiler failed (see ${case_dir}/profiler.err)"
	fi
	kill -0 "${go_cpu_target_pid}" 2> /dev/null \
		|| fatal "Go ${mode} fixture exited during profiling (see ${TOOL_WORK_DIR}/${mode}/fixture.err)"

	mapfile -t folded_files < <(find "${case_dir}" -maxdepth 1 -name 'perf_*.folded' -type f)
	if [[ "${scope}" == thread-group ]]; then
		[[ ${#folded_files[@]} -gt 0 ]] || fatal "Go ${mode}: no perf_*.folded file produced"
		grep -qE "${GO_CPU_CHAIN}" "${folded_files[@]}" \
			|| fatal "Go ${mode}: positive worker->middle->hot stack not found in ${case_dir}"
	elif [[ ${#folded_files[@]} -gt 0 ]] && grep -qE "${GO_CPU_WORKER}" "${folded_files[@]}"; then
		fatal "Go ${mode}: worker captured without --thread-group in ${case_dir}"
	fi
}

for mode in exe pie stripped stripped-pie; do
	case_dir="${TOOL_WORK_DIR}/${mode}"
	fixture_bin="${case_dir}/go-cpu-fixture"
	build_args=()
	case "${mode}" in
	pie) build_args=(-buildmode=pie) ;;
	stripped) build_args=(-ldflags='-s -w') ;;
	stripped-pie) build_args=(-buildmode=pie -ldflags='-s -w') ;;
	esac
	mkdir -p "${case_dir}"
	# Keep normal optimizations: noinline directives preserve only the asserted frames.
	CGO_ENABLED=0 go build -mod=vendor "${build_args[@]}" -o "${fixture_bin}" "${GO_CPU_FIXTURE_SRC}" \
		> "${case_dir}/compile.log" 2>&1 \
		|| fatal "Go ${mode} fixture compilation failed:"$'\n'"$(< "${case_dir}/compile.log")"

	"${fixture_bin}" > "${case_dir}/fixture.out" 2> "${case_dir}/fixture.err" &
	go_cpu_target_pid=$!
	wait_until 15 0.1 go_cpu_fixture_ready "${case_dir}/fixture.out" \
		|| fatal "Go ${mode} workers not ready on separate threads (see ${case_dir}/fixture.out and fixture.err)"

	# The same live workload must be excluded by TID, then included by TGID.
	if [[ "${mode}" == exe ]]; then
		run_go_cpu_profile "${mode}" target-thread
	fi
	run_go_cpu_profile "${mode}" thread-group
	stop_by_pid "${go_cpu_target_pid}" 5
	wait "${go_cpu_target_pid}" || true
	go_cpu_target_pid=""
done

log_info "Go CPU call chains resolved for exe, PIE, stripped and stripped PIE; thread-group filtering verified"
