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

# Verify the two-stage gating in bpf/include/bpf_dbg.h: with -DDEBUG_BPF the
# bpf_dbg_msg() body must be emitted into the .o; without it, the macro
# collapses to (void) casts and leaves no trace. The fixture is compiled
# twice via the project's own build/clang.sh — exercising the real
# BPF_EXTRA_CFLAGS contract — then `strings` checks for a unique marker.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

FIXTURE="${ROOT_DIR}/integration/testdata/test_bpf_dbg.bpf.c"
# Markers MUST stay in sync with the fixture above.
BASE_MARKER="HUATUO_BPF_DBG_BASE_MARKER_V1"
DEBUG_MARKER="HUATUO_BPF_DBG_DEBUG_MARKER_V1"

command -v strings > /dev/null || fatal "strings(1) not found in PATH"

WORK_DIR=$(mktemp -d /tmp/huatuo-bpf-dbg.XXXXXX)
trap 'rm -rf "${WORK_DIR}"' EXIT

# compile_fixture <out.o> <extra_cflags>: invokes clang.sh via the same
# env-var contract the Makefile uses (BPF_EXTRA_CFLAGS), so this test
# tracks the real debug-toggle path instead of a parallel one.
compile_fixture() {
	BPF_EXTRA_CFLAGS="$2" "${ROOT_DIR}/build/clang.sh" \
		-s "${FIXTURE}" -o "$1" -I "${ROOT_DIR}/bpf/include" \
		> "${WORK_DIR}/clang.log" 2>&1 \
		|| fatal "clang.sh failed (BPF_EXTRA_CFLAGS='$2'):"$'\n'"$(< "${WORK_DIR}/clang.log")"
}

# expect_marker <obj> <"has"|"missing"> <marker>: single source of truth
# for the strings(1)-based assertion. `strings -a` scans the whole file
# (section layout varies by binutils version); `grep -Fq` makes the match
# literal and silent.
expect_marker() {
	local obj=$1 mode=$2 marker=$3
	if strings -a "${obj}" | grep -Fq -- "${marker}"; then
		[[ "${mode}" == "has" ]] && return 0
		fatal "marker '${marker}' must NOT appear in $(basename "${obj}")"
	fi
	[[ "${mode}" == "missing" ]] && return 0
	fatal "expected marker '${marker}' in $(basename "${obj}"), not found"
}

# Names track BPF_DEBUG=1 / BPF_DEBUG=0 (the user-facing knob), not the
# internal -DDEBUG_BPF macro, so reading the assertions below matches how
# a developer would invoke `make bpf-build BPF_DEBUG=...`.
OBJ_WITH_DEBUG="${WORK_DIR}/bpf_debug_on.o"
OBJ_WITHOUT_DEBUG="${WORK_DIR}/bpf_debug_off.o"

log_info "compiling fixture (BPF_DEBUG=1 and BPF_DEBUG=0)"
compile_fixture "${OBJ_WITH_DEBUG}" "-DDEBUG_BPF"
compile_fixture "${OBJ_WITHOUT_DEBUG}" ""

# Sanity: a plain .rodata string unrelated to the macro must appear in
# both builds. If not, the toolchain or fixture is broken and the DEBUG
# assertions below would be meaningless.
expect_marker "${OBJ_WITH_DEBUG}" has "${BASE_MARKER}"
expect_marker "${OBJ_WITHOUT_DEBUG}" has "${BASE_MARKER}"

# Core assertions: the marker inside bpf_dbg_msg() tracks macro expansion.
expect_marker "${OBJ_WITH_DEBUG}" has "${DEBUG_MARKER}"
expect_marker "${OBJ_WITHOUT_DEBUG}" missing "${DEBUG_MARKER}"

log_info "bpf_dbg macro gating verified for both build modes"
