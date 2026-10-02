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

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

is_container && skip "native memory profiler requires bare-metal cgroup/PMU access"

bpf_tool_setup profiler native_physical_usage profiler-physical-usage
readonly FIXTURE_SRC="${ROOT_DIR}/integration/testdata/test_profiler_physical_usage.user.c"
readonly PROFILER_DURATION=6
readonly PROFILER_AGGR_INTERVAL=2
readonly PROFILER_READY_TIMEOUT=15
readonly PROFILER_READY_INTERVAL=1
readonly PHYSICAL_MARKER_SYMBOL="test_physical_usage_touch_pages"

if kprobe_available folio_add_new_anon_rmap && kprobe_available folio_remove_rmap_ptes; then
	log_info "using folio rmap kprobes"
elif kprobe_available folio_add_new_anon_rmap && kprobe_available page_remove_rmap; then
	log_info "using mixed folio/page rmap kprobes"
elif kprobe_available page_add_new_anon_rmap && kprobe_available page_remove_rmap; then
	log_info "using page rmap kprobes"
else
	skip "no supported physical usage kprobe pair found"
fi

WORK_DIR=${TOOL_WORK_DIR}
FIXTURE_BIN="${WORK_DIR}/physical_usage_workload"
TARGET_PID=""
PROFILER_PID=""

cleanup() {
	[[ -n "${PROFILER_PID}" ]] && stop_by_pid "${PROFILER_PID}" 5 || true
	[[ -n "${TARGET_PID}" ]] && stop_by_pid "${TARGET_PID}" 5 || true
}
trap cleanup EXIT

run_profile_case() {
	local mode=$1 out_dir=$2
	local fixture_out="${out_dir}/fixture.out"
	local fixture_err="${out_dir}/fixture.err"
	local profiler_out="${out_dir}/profiler.out"
	local profiler_err="${out_dir}/profiler.err"

	mkdir -p "${out_dir}"
	"${FIXTURE_BIN}" > "${fixture_out}" 2> "${fixture_err}" &
	TARGET_PID=$!
	kill -0 "${TARGET_PID}" 2> /dev/null || fatal "fixture exited immediately for mode=${mode}"

	log_info "running profiler mode=${mode} pid=${TARGET_PID}"
	("${TOOL_BIN}" \
		--type memory \
		--language c \
		--memory-mode "${mode}" \
		--pid "${TARGET_PID}" \
		--duration "${PROFILER_DURATION}" \
		--output-format collapsed \
		--output-path "${out_dir}" \
		--aggr-interval "${PROFILER_AGGR_INTERVAL}" \
		--physical-memory-probability 100 \
		--verbose \
		> "${profiler_out}" 2> "${profiler_err}") &
	PROFILER_PID=$!
	kill -0 "${PROFILER_PID}" 2> /dev/null || fatal "failed to launch profiler mode=${mode}"

	wait_until "${PROFILER_READY_TIMEOUT}" "${PROFILER_READY_INTERVAL}" \
		profiler_ready "${profiler_out}" || fatal "profiler did not start read loop mode=${mode}"

	kill -USR1 "${TARGET_PID}" || fatal "failed to signal fixture mode=${mode}"

	if ! wait "${PROFILER_PID}"; then
		PROFILER_PID=""
		fatal "profiler exited non-zero mode=${mode}"
	fi
	PROFILER_PID=""

	if ! wait "${TARGET_PID}"; then
		TARGET_PID=""
		fatal "fixture exited non-zero mode=${mode}"
	fi
	TARGET_PID=""
}

folded_line_count() {
	local dir=$1
	local count=0
	local file

	while IFS= read -r file; do
		count=$((count + $(awk 'NF { count++ } END { print count + 0 }' "${file}")))
	done < <(find "${dir}" -maxdepth 1 -name 'perf_*.folded' -type f)

	echo "${count}"
}

folded_value_sum() {
	local dir=$1 symbol=$2
	local sum=0
	local file

	while IFS= read -r file; do
		sum=$((sum + $(awk -v symbol="${symbol}" \
			'index($0, symbol) { sum += $NF } END { printf "%.0f\n", sum + 0 }' "${file}")))
	done < <(find "${dir}" -maxdepth 1 -name 'perf_*.folded' -type f)

	echo "${sum}"
}

compile_user_fixture "${FIXTURE_SRC}" "${FIXTURE_BIN}"

ALLOC_DIR="${WORK_DIR}/physical_alloc"
run_profile_case physical_alloc "${ALLOC_DIR}"
ALLOC_CAPTURED_BYTES=$(folded_value_sum "${ALLOC_DIR}" "${PHYSICAL_MARKER_SYMBOL}")
[[ "${ALLOC_CAPTURED_BYTES}" -gt 0 ]] \
	|| fatal "physical_alloc captured no bytes for ${PHYSICAL_MARKER_SYMBOL}"

ALLOC_EXPECTED_BYTES=$(awk -F= '/^actual_allocated_bytes=/{value=$2} END {print value}' \
	"${ALLOC_DIR}/fixture.err")
[[ "${ALLOC_EXPECTED_BYTES}" =~ ^[0-9]+$ ]] \
	|| fatal "physical_alloc fixture did not report actual_allocated_bytes"

if [[ "${ALLOC_CAPTURED_BYTES}" -ne "${ALLOC_EXPECTED_BYTES}" ]]; then
	log_error "physical_alloc captured ${ALLOC_CAPTURED_BYTES} bytes; expected ${ALLOC_EXPECTED_BYTES}"
	fatal "physical_alloc byte conversion verification failed"
fi
log_info "physical_alloc captured ${ALLOC_CAPTURED_BYTES} bytes without overcounting"

USAGE_DIR="${WORK_DIR}/physical_usage"
run_profile_case physical_usage "${USAGE_DIR}"
USAGE_LINES=$(folded_line_count "${USAGE_DIR}")
if [[ "${USAGE_LINES}" -ne 0 ]]; then
	log_error "physical_usage should emit no folded symbols for balanced alloc/free; lines=${USAGE_LINES}"
	fatal "physical_usage balance verification failed"
fi

log_info "physical_usage balanced alloc/free produced no folded symbols"
