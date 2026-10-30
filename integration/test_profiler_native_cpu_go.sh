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

require_commands go
require_kernel_btf
require_profiler_bpf_sampling

GO_CPU_CC=$(go env CC) || fatal "cannot determine the external Go compiler with go env CC"
readonly GO_CPU_CC
# Go handles quoting and wrapper arguments in CC. Preflight only a plain
# executable here rather than incorrectly splitting a compiler command line.
if [[ "${GO_CPU_CC}" != *[[:space:]]* && "${GO_CPU_CC}" != *[\"\'\\]* ]]; then
	require_commands "${GO_CPU_CC}"
fi

bpf_tool_setup profiler native_oncpu_profiler profiler-go-cpu

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
	local -a thread_group_args=()
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
}

assert_go_cpu_profile() {
	local mode=$1 scope=$2
	local case_dir="${TOOL_WORK_DIR}/${mode}/${scope}"
	local -a folded_files=()
	mapfile -t folded_files < <(find "${case_dir}" -maxdepth 1 -name 'perf_*.folded' -type f)
	if [[ "${scope}" == thread-group ]]; then
		[[ ${#folded_files[@]} -gt 0 ]] || fatal "Go ${mode}: no perf_*.folded file produced"
		grep -qE "${GO_CPU_CHAIN}" "${folded_files[@]}" \
			|| fatal "Go ${mode}: expected ${GO_CPU_CHAIN}; actual folded output:"$'\n'"$(head -n 20 "${folded_files[@]}")"
	elif [[ ${#folded_files[@]} -gt 0 ]] && grep -qE "${GO_CPU_WORKER}" "${folded_files[@]}"; then
		fatal "Go ${mode}: expected no worker without --thread-group; actual folded output:"$'\n'"$(head -n 20 "${folded_files[@]}")"
	fi
}

for link_mode in internal external; do
	cgo_enabled=0
	[[ "${link_mode}" != external ]] || cgo_enabled=1
	for mode in exe pie stripped stripped-pie; do
		variant="${link_mode}-${mode}"
		case_dir="${TOOL_WORK_DIR}/${variant}"
		fixture_bin="${case_dir}/go-cpu-fixture"
		build_args=()
		ldflags="-linkmode=${link_mode}"
		case "${mode}" in
		pie) build_args=(-buildmode=pie) ;;
		stripped) ldflags+=' -s -w' ;;
		stripped-pie)
			build_args=(-buildmode=pie)
			ldflags+=' -s -w'
			;;
		esac
		mkdir -p "${case_dir}"
		# Keep normal optimizations: noinline directives preserve only the asserted frames.
		CGO_ENABLED="${cgo_enabled}" go build -mod=vendor "${build_args[@]}" \
			-ldflags="${ldflags}" -o "${fixture_bin}" "${GO_CPU_FIXTURE_SRC}" \
			> "${case_dir}/compile.log" 2>&1 \
			|| fatal "Go ${variant} fixture compilation failed:"$'\n'"$(< "${case_dir}/compile.log")"

		"${fixture_bin}" > "${case_dir}/fixture.out" 2> "${case_dir}/fixture.err" &
		go_cpu_target_pid=$!
		wait_until 15 0.1 go_cpu_fixture_ready "${case_dir}/fixture.out" \
			|| fatal "Go ${variant} workers not ready on separate threads (see ${case_dir}/fixture.out and fixture.err)"

		# The same live workload must be excluded by TID, then included by TGID.
		if [[ "${mode}" == exe ]]; then
			run_go_cpu_profile "${variant}" target-thread
			assert_go_cpu_profile "${variant}" target-thread
		fi
		run_go_cpu_profile "${variant}" thread-group
		assert_go_cpu_profile "${variant}" thread-group
		stop_by_pid "${go_cpu_target_pid}" 5
		wait "${go_cpu_target_pid}" || true
		go_cpu_target_pid=""
	done
done

log_info "Go CPU call chains resolved for internal/external linking with exe, PIE, stripped and stripped PIE; thread-group filtering verified"
