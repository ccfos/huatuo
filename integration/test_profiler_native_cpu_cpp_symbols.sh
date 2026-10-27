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

# Verify the native CPU profiler demangles a sampled C++ template frame before
# writing collapsed output.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

require_commands clang
require_kernel_btf
require_profiler_bpf_sampling

bpf_tool_setup profiler native_oncpu_profiler profiler-cpp-symbols
readonly CPP_SYMBOL_FIXTURE_SRC="${ROOT_DIR}/integration/testdata/test_profiler_cpp_symbols.user.cc"
readonly CPP_SYMBOL_DURATION=10
readonly CPP_SYMBOL_FREQ=99
readonly CPP_SYMBOL_AGGR_INTERVAL=5
readonly CPP_SYMBOL_EXPECTED='void huatuo::symbol_fixture::consume<unsigned long>(unsigned long)'
readonly CPP_SYMBOL_MANGLED='_ZN6huatuo14symbol_fixture7consumeImEEvT_'

cpp_symbol_target_pid=""

cleanup() {
	[[ -n "${cpp_symbol_target_pid}" ]] && stop_by_pid "${cpp_symbol_target_pid}" 5 || true
}
trap cleanup EXIT

assert_cpp_symbol_profile() {
	local work_dir=$1
	local -a folded_files=()
	mapfile -t folded_files < <(find "${work_dir}" -maxdepth 1 -name 'perf_*.folded' -type f)
	[[ ${#folded_files[@]} -gt 0 ]] \
		|| fatal "no perf_*.folded file produced in ${work_dir}"
	grep -qhF "${CPP_SYMBOL_EXPECTED}" "${folded_files[@]}" \
		|| fatal "demangled C++ frame not found in ${work_dir}: ${CPP_SYMBOL_EXPECTED}"
	if grep -qhF "${CPP_SYMBOL_MANGLED}" "${folded_files[@]}"; then
		fatal "mangled C++ frame remained in ${work_dir}: ${CPP_SYMBOL_MANGLED}"
	fi
}

run_cpp_symbol_profile() {
	local mode=$1
	shift
	local work_dir="${TOOL_WORK_DIR}/${mode}"
	local fixture_bin="${work_dir}/cpp-symbol-fixture"
	mkdir -p "${work_dir}"
	log_info "compiling C++ ${mode} symbol fixture"
	clang -x c++ -O0 -g -Wall -Wextra -std=c++17 -fno-exceptions -fno-rtti \
		-fno-inline -fno-omit-frame-pointer "$@" \
		-o "${fixture_bin}" "${CPP_SYMBOL_FIXTURE_SRC}" \
		2> "${work_dir}/compile.log" \
		|| fatal "clang failed compiling C++ ${mode}:"$'\n'"$(< "${work_dir}/compile.log")"
	"${fixture_bin}" > "${work_dir}/fixture.out" 2> "${work_dir}/fixture.err" &
	cpp_symbol_target_pid=$!
	kill -0 "${cpp_symbol_target_pid}" 2> /dev/null \
		|| fatal "C++ ${mode} fixture exited immediately (pid=${cpp_symbol_target_pid})"
	log_info "profiling C++ ${mode} fixture pid=${cpp_symbol_target_pid}"
	if ! "${TOOL_BIN}" \
		--type cpu \
		--language c++ \
		--pid "${cpp_symbol_target_pid}" \
		--duration "${CPP_SYMBOL_DURATION}" \
		--freq "${CPP_SYMBOL_FREQ}" \
		--aggr-interval "${CPP_SYMBOL_AGGR_INTERVAL}" \
		--output-format collapsed \
		--output-path "${work_dir}" \
		--verbose \
		> "${work_dir}/profiler.out" 2> "${work_dir}/profiler.err"; then
		fatal "C++ ${mode} profiler exited non-zero (see ${work_dir}/profiler.err)"
	fi
	assert_cpp_symbol_profile "${work_dir}"
	stop_by_pid "${cpp_symbol_target_pid}" 5
	wait "${cpp_symbol_target_pid}" || true
	cpp_symbol_target_pid=""
}

run_cpp_symbol_profile default
# GNU's compact layout can map one file page into both RX and RW segments.
run_cpp_symbol_profile compact-pie -fPIE -pie -Wl,-z,noseparate-code
log_info "C++ template frames resolved in default and compact PIE folded output"
