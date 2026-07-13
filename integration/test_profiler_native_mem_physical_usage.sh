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

readonly TOOL_BIN="${ROOT_DIR}/_output/bin/profiler"
readonly FIXTURE_SRC="${ROOT_DIR}/integration/testdata/test_profiler_physical_usage.user.c"
readonly PROFILER_DURATION=6
readonly PROFILER_AGGR_INTERVAL=2
readonly PROFILER_READY_TIMEOUT=15
readonly PROFILER_READY_INTERVAL=1

[[ -x "${TOOL_BIN}" ]] || fatal "profiler binary missing: ${TOOL_BIN}"
[[ -r "${ROOT_DIR}/_output/bpf/native_physical_alloc.o" ]] || fatal "native physical alloc bpf object missing"
[[ -r "${ROOT_DIR}/_output/bpf/native_physical_usage.o" ]] || fatal "native physical usage bpf object missing"
if kprobe_available page_add_new_anon_rmap && kprobe_available page_remove_rmap; then
	log_info "using page rmap kprobes"
elif kprobe_available folio_add_new_anon_rmap && kprobe_available folio_remove_rmap_ptes; then
	log_info "using folio rmap kprobes"
else
	skip "no supported physical usage kprobe pair found"
fi

WORK_DIR=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/profiler-physical-usage.XXXXXX")
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
		--type mem \
		--language c \
		--memory-mode "${mode}" \
		--pid "${TARGET_PID}" \
		--duration "${PROFILER_DURATION}" \
		--output-format collapsed \
		--output-path "${out_dir}" \
		--aggr-interval "${PROFILER_AGGR_INTERVAL}" \
		--flags "--probability=100" \
		--verbose \
		> "${profiler_out}" 2> "${profiler_err}") &
	PROFILER_PID=$!
	kill -0 "${PROFILER_PID}" 2> /dev/null || fatal "failed to launch profiler mode=${mode}"

	wait_until "${PROFILER_READY_TIMEOUT}" "${PROFILER_READY_INTERVAL}" \
		"profiler ready mode=${mode}" \
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

compile_user_fixture "${FIXTURE_SRC}" "${FIXTURE_BIN}"

ALLOC_DIR="${WORK_DIR}/physical_alloc"
run_profile_case physical_alloc "${ALLOC_DIR}"
ALLOC_LINES=$(folded_line_count "${ALLOC_DIR}")
[[ "${ALLOC_LINES}" -gt 0 ]] || fatal "physical_alloc captured no folded output"

USAGE_DIR="${WORK_DIR}/physical_usage"
run_profile_case physical_usage "${USAGE_DIR}"
USAGE_LINES=$(folded_line_count "${USAGE_DIR}")
if [[ "${USAGE_LINES}" -ne 0 ]]; then
	log_error "physical_usage should emit no folded symbols for balanced alloc/free; lines=${USAGE_LINES}"
	fatal "physical_usage balance verification failed"
fi

log_info "physical_usage balanced alloc/free produced no folded symbols"
