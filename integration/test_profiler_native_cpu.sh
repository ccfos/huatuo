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

# Verify the native CPU profiler resolves user symbols: a main->f1->f2->f3
# fixture sampled at 99 Hz must produce at least one ";f1;f2;f3 N" line in
# the folded output. Anchoring on $ prevents prefix matches in deeper frames.

set -exuo pipefail

source "${ROOT_DIR}/integration/lib.sh"

# --- preconditions -----------------------------------------------------------

is_container && skip "native CPU profiler requires bare-metal cgroup/PMU access"

readonly TOOL_BIN="${ROOT_DIR}/_output/bin/profiler"
readonly TOOL_OUT="${HUATUO_BAMAI_TEST_TMPDIR}/profiler.out"
readonly TOOL_ERR="${HUATUO_BAMAI_TEST_TMPDIR}/profiler.err"
readonly FIXTURE_SRC="${ROOT_DIR}/integration/testdata/test_profiler_callchain.user.c"

command -v gcc > /dev/null || skip "gcc(1) not in PATH"
[[ -x "${TOOL_BIN}" ]] || fatal "profiler binary missing: ${TOOL_BIN}"
[[ -r "${ROOT_DIR}/_output/bpf/native_cpu_profiler.o" ]] || fatal "native bpf object missing"
[[ -r "${FIXTURE_SRC}" ]] || fatal "fixture source missing: ${FIXTURE_SRC}"

# Missing perf_event_paranoid ⇒ perf not exposed; skip rather than default
# to "2" which would mask the real issue as a misleading BPF load failure.
[[ -r /proc/sys/kernel/perf_event_paranoid ]] || skip "perf_event_paranoid not readable: perf unavailable"
readonly PARANOID=$(cat /proc/sys/kernel/perf_event_paranoid)
[[ "${PARANOID}" -le 2 ]] || skip "kernel.perf_event_paranoid=${PARANOID} (>2) blocks perf sampling"

# --- tunables ----------------------------------------------------------------

readonly PROFILER_DURATION=10
readonly PROFILER_FREQ=99
readonly PROFILER_AGGR_INTERVAL=5
# Fixture runs 30 s (hardcoded), outlasting the 10 s profiler window.
readonly CHAIN_PATTERN=';f1;f2;f3 [0-9]+$'

# --- workspace + cleanup -----------------------------------------------------

WORK_DIR=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/profiler-callchain.XXXXXX")
FIXTURE_BIN="${WORK_DIR}/callchain"
FIXTURE_OUT="${WORK_DIR}/callchain.out"
FIXTURE_ERR="${WORK_DIR}/callchain.err"
TARGET_PID=""

cleanup() {
	local rc=$?
	[[ -n "${TARGET_PID}" ]] && stop_by_pid "${TARGET_PID}" 5
	if [[ ${rc} -ne 0 ]]; then
		dump_file "profiler stdout" "${TOOL_OUT}"
		dump_file "profiler stderr" "${TOOL_ERR}"
		dump_file "fixture stdout"  "${FIXTURE_OUT}"
		dump_file "fixture stderr"  "${FIXTURE_ERR}"
		log_error "workspace preserved at ${WORK_DIR}"
	else
		rm -rf "${WORK_DIR}"
	fi
}
trap cleanup EXIT

# --- build fixture -----------------------------------------------------------

log_info "compiling fixture: $(basename "${FIXTURE_SRC}")"
gcc -O0 -g -fno-inline -fno-omit-frame-pointer \
	-o "${FIXTURE_BIN}" "${FIXTURE_SRC}" \
	2> "${WORK_DIR}/gcc.err" \
	|| fatal "gcc failed:"$'\n'"$(< "${WORK_DIR}/gcc.err")"

# --- launch target -----------------------------------------------------------

log_info "launching target with 30s"
"${FIXTURE_BIN}" > "${FIXTURE_OUT}" 2> "${FIXTURE_ERR}" &
TARGET_PID=$!
# Confirm the fixture didn't crash on startup.
kill -0 "${TARGET_PID}" 2> /dev/null || fatal "fixture exited immediately (pid=${TARGET_PID})"

log_info "target pid=${TARGET_PID}"

# --- run profiler ------------------------------------------------------------

log_info "running profiler for ${PROFILER_DURATION}s @ ${PROFILER_FREQ}Hz against pid=${TARGET_PID}"
if ! "${TOOL_BIN}" \
	--type cpu \
	--language c \
	--pid "${TARGET_PID}" \
	--duration "${PROFILER_DURATION}" \
	--freq "${PROFILER_FREQ}" \
	--output-format collapsed \
	--output-path "${WORK_DIR}" \
	--aggr-interval "${PROFILER_AGGR_INTERVAL}" \
	> "${TOOL_OUT}" 2> "${TOOL_ERR}"; then
	fatal "profiler exited non-zero (see ${TOOL_ERR})"
fi

# --- assert ------------------------------------------------------------------

mapfile -t FOLDED_FILES < <(find "${WORK_DIR}" -maxdepth 1 -name 'perf_*.folded' -type f)
[[ ${#FOLDED_FILES[@]} -gt 0 ]] || fatal "no perf_*.folded file produced in ${WORK_DIR}"

log_info "found ${#FOLDED_FILES[@]} folded file(s); asserting f1->f2->f3 chain"

MATCH_COUNT=$(grep -hE "${CHAIN_PATTERN}" "${FOLDED_FILES[@]}" | wc -l) || true
if [[ "${MATCH_COUNT}" -eq 0 ]]; then
	log_error "no line matched ${CHAIN_PATTERN}; folded contents:"
	cat "${FOLDED_FILES[@]}" >&2
	fatal "f1->f2->f3 chain not found in folded output"
fi

log_info "matched stack lines ending in ;f1;f2;f3: got ${MATCH_COUNT}, want >= 1"
