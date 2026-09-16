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

command -v clang > /dev/null || skip "clang command is not installed"
is_container && skip "native CPU profiler requires the host PID namespace"

bpf_tool_setup profiler native_oncpu_profiler profiler-cpp-symbols
readonly CPP_SYMBOL_FIXTURE_SRC="${ROOT_DIR}/integration/testdata/test_profiler_cpp_symbols.user.cc"
readonly CPP_SYMBOL_DURATION=10
readonly CPP_SYMBOL_FREQ=99
readonly CPP_SYMBOL_AGGR_INTERVAL=5
readonly CPP_SYMBOL_EXPECTED='void huatuo::symbol_fixture::consume<unsigned long>(unsigned long)'
readonly CPP_SYMBOL_MANGLED='_ZN6huatuo14symbol_fixture7consumeImEEvT_'

[[ -r /proc/sys/kernel/perf_event_paranoid ]] \
	|| skip "perf_event_paranoid not readable: perf unavailable"
readonly CPP_SYMBOL_PARANOID=$(cat /proc/sys/kernel/perf_event_paranoid)
[[ "${CPP_SYMBOL_PARANOID}" -le 2 ]] \
	|| skip "kernel.perf_event_paranoid=${CPP_SYMBOL_PARANOID} (>2) blocks perf sampling"

readonly CPP_SYMBOL_WORK_DIR=${TOOL_WORK_DIR}
readonly CPP_SYMBOL_FIXTURE_BIN="${CPP_SYMBOL_WORK_DIR}/cpp-symbol-fixture"
readonly CPP_SYMBOL_COMPILE_LOG="${CPP_SYMBOL_WORK_DIR}/cpp-symbol-fixture.compile.log"
readonly CPP_SYMBOL_FIXTURE_OUT="${CPP_SYMBOL_WORK_DIR}/cpp-symbol-fixture.out"
readonly CPP_SYMBOL_FIXTURE_ERR="${CPP_SYMBOL_WORK_DIR}/cpp-symbol-fixture.err"
cpp_symbol_target_pid=""

cleanup() {
	[[ -n "${cpp_symbol_target_pid}" ]] && stop_by_pid "${cpp_symbol_target_pid}" 5 || true
}
trap cleanup EXIT

log_info "compiling C++ symbol fixture"
clang -x c++ -O0 -g -Wall -Wextra -std=c++17 -fno-exceptions -fno-rtti \
	-fno-inline -fno-omit-frame-pointer \
	-o "${CPP_SYMBOL_FIXTURE_BIN}" "${CPP_SYMBOL_FIXTURE_SRC}" \
	2> "${CPP_SYMBOL_COMPILE_LOG}" \
	|| fatal "clang failed compiling ${CPP_SYMBOL_FIXTURE_SRC}:"$'\n'"$(< "${CPP_SYMBOL_COMPILE_LOG}")"

"${CPP_SYMBOL_FIXTURE_BIN}" \
	> "${CPP_SYMBOL_FIXTURE_OUT}" 2> "${CPP_SYMBOL_FIXTURE_ERR}" &
cpp_symbol_target_pid=$!
kill -0 "${cpp_symbol_target_pid}" 2> /dev/null \
	|| fatal "C++ fixture exited immediately (pid=${cpp_symbol_target_pid})"

log_info "profiling C++ fixture pid=${cpp_symbol_target_pid}"
if ! "${TOOL_BIN}" \
	--type cpu \
	--language c++ \
	--pid "${cpp_symbol_target_pid}" \
	--duration "${CPP_SYMBOL_DURATION}" \
	--freq "${CPP_SYMBOL_FREQ}" \
	--aggr-interval "${CPP_SYMBOL_AGGR_INTERVAL}" \
	--output-format collapsed \
	--output-path "${CPP_SYMBOL_WORK_DIR}" \
	--verbose \
	> "${TOOL_OUT}" 2> "${TOOL_ERR}"; then
	fatal "profiler exited non-zero (see ${TOOL_ERR})"
fi

mapfile -t cpp_symbol_folded_files < <(
	find "${CPP_SYMBOL_WORK_DIR}" -maxdepth 1 -name 'perf_*.folded' -type f
)
[[ ${#cpp_symbol_folded_files[@]} -gt 0 ]] \
	|| fatal "no perf_*.folded file produced in ${CPP_SYMBOL_WORK_DIR}"

grep -qhF "${CPP_SYMBOL_EXPECTED}" "${cpp_symbol_folded_files[@]}" \
	|| fatal "demangled C++ frame not found: ${CPP_SYMBOL_EXPECTED}"
if grep -qhF "${CPP_SYMBOL_MANGLED}" "${cpp_symbol_folded_files[@]}"; then
	fatal "mangled C++ frame remained in folded output: ${CPP_SYMBOL_MANGLED}"
fi

log_info "captured C++ folded stack(s):"
grep -hF "${CPP_SYMBOL_EXPECTED}" "${cpp_symbol_folded_files[@]}"
log_info "C++ template frame was demangled in folded output"
