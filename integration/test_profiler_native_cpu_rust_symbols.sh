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

# Verify the native resolver demangles a Rust v0 generic frame and normalizes
# its array-type semicolon only at the collapsed-output boundary.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

command -v rustc > /dev/null || skip "rustc command is not installed"

readonly RUST_SYMBOL_RUSTC_VERSION=$(rustc --version | awk '{ print $2 }')
if [[ ! "${RUST_SYMBOL_RUSTC_VERSION}" =~ ^([0-9]+)\.([0-9]+)\. ]]; then
	skip "cannot parse rustc version: ${RUST_SYMBOL_RUSTC_VERSION}"
fi
readonly RUST_SYMBOL_RUSTC_MAJOR=${BASH_REMATCH[1]}
readonly RUST_SYMBOL_RUSTC_MINOR=${BASH_REMATCH[2]}
if ((RUST_SYMBOL_RUSTC_MAJOR < 1 || (RUST_SYMBOL_RUSTC_MAJOR == 1 && RUST_SYMBOL_RUSTC_MINOR < 59))); then
	skip "rustc ${RUST_SYMBOL_RUSTC_VERSION} does not support stable Rust v0 symbols"
fi

bpf_tool_setup profiler native_oncpu_profiler profiler-rust-symbols
readonly RUST_SYMBOL_FIXTURE_SRC="${ROOT_DIR}/integration/testdata/test_profiler_rust_symbols.user.rs"
readonly RUST_SYMBOL_DURATION=10
readonly RUST_SYMBOL_FREQ=99
readonly RUST_SYMBOL_AGGR_INTERVAL=5
readonly RUST_SYMBOL_EXPECTED='profiler_symbol_fixture::consume::<[u8: 7]>'
readonly RUST_SYMBOL_UNNORMALIZED='profiler_symbol_fixture::consume::<[u8; 7]>'

[[ -r /proc/sys/kernel/perf_event_paranoid ]] \
	|| skip "perf_event_paranoid not readable: perf unavailable"
readonly RUST_SYMBOL_PARANOID=$(cat /proc/sys/kernel/perf_event_paranoid)
[[ "${RUST_SYMBOL_PARANOID}" -le 2 ]] \
	|| skip "kernel.perf_event_paranoid=${RUST_SYMBOL_PARANOID} (>2) blocks perf sampling"

readonly RUST_SYMBOL_WORK_DIR=${TOOL_WORK_DIR}
readonly RUST_SYMBOL_FIXTURE_BIN="${RUST_SYMBOL_WORK_DIR}/rust-symbol-fixture"
readonly RUST_SYMBOL_COMPILE_LOG="${RUST_SYMBOL_WORK_DIR}/rust-symbol-fixture.compile.log"
readonly RUST_SYMBOL_FIXTURE_OUT="${RUST_SYMBOL_WORK_DIR}/rust-symbol-fixture.out"
readonly RUST_SYMBOL_FIXTURE_ERR="${RUST_SYMBOL_WORK_DIR}/rust-symbol-fixture.err"
rust_symbol_target_pid=""

cleanup() {
	[[ -n "${rust_symbol_target_pid}" ]] && stop_by_pid "${rust_symbol_target_pid}" 5 || true
}
trap cleanup EXIT

log_info "compiling Rust v0 symbol fixture"
rustc \
	--crate-name profiler_symbol_fixture \
	--edition 2021 \
	-C opt-level=0 \
	-C debuginfo=2 \
	-C force-frame-pointers=yes \
	-C symbol-mangling-version=v0 \
	-o "${RUST_SYMBOL_FIXTURE_BIN}" \
	"${RUST_SYMBOL_FIXTURE_SRC}" \
	2> "${RUST_SYMBOL_COMPILE_LOG}" \
	|| fatal "rustc failed compiling ${RUST_SYMBOL_FIXTURE_SRC}:"$'\n'"$(< "${RUST_SYMBOL_COMPILE_LOG}")"

"${RUST_SYMBOL_FIXTURE_BIN}" \
	> "${RUST_SYMBOL_FIXTURE_OUT}" 2> "${RUST_SYMBOL_FIXTURE_ERR}" &
rust_symbol_target_pid=$!
kill -0 "${rust_symbol_target_pid}" 2> /dev/null \
	|| fatal "Rust fixture exited immediately (pid=${rust_symbol_target_pid})"

# Rust has no CLI selector yet; c selects the same language-neutral native
# collector and symbol resolver used by C++ and Go.
log_info "profiling Rust fixture pid=${rust_symbol_target_pid}"
if ! "${TOOL_BIN}" \
	--type cpu \
	--language c \
	--pid "${rust_symbol_target_pid}" \
	--duration "${RUST_SYMBOL_DURATION}" \
	--freq "${RUST_SYMBOL_FREQ}" \
	--aggr-interval "${RUST_SYMBOL_AGGR_INTERVAL}" \
	--output-format collapsed \
	--output-path "${RUST_SYMBOL_WORK_DIR}" \
	--verbose \
	> "${TOOL_OUT}" 2> "${TOOL_ERR}"; then
	fatal "profiler exited non-zero (see ${TOOL_ERR})"
fi

mapfile -t rust_symbol_folded_files < <(
	find "${RUST_SYMBOL_WORK_DIR}" -maxdepth 1 -name 'perf_*.folded' -type f
)
[[ ${#rust_symbol_folded_files[@]} -gt 0 ]] \
	|| fatal "no perf_*.folded file produced in ${RUST_SYMBOL_WORK_DIR}"

grep -qhF "${RUST_SYMBOL_EXPECTED}" "${rust_symbol_folded_files[@]}" \
	|| fatal "demangled Rust v0 frame not found: ${RUST_SYMBOL_EXPECTED}"
if grep -qhF "${RUST_SYMBOL_UNNORMALIZED}" "${rust_symbol_folded_files[@]}"; then
	fatal "Rust frame retained a folded delimiter: ${RUST_SYMBOL_UNNORMALIZED}"
fi

log_info "captured Rust folded stack(s):"
grep -hF "${RUST_SYMBOL_EXPECTED}" "${rust_symbol_folded_files[@]}"
log_info "Rust v0 generic frame was demangled and normalized in folded output"
