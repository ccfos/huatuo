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

# Verify --cpuid: fixture on same CPU → chain present; on different CPU → absent.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

# --- preconditions -----------------------------------------------------------

is_container && skip "native CPU profiler requires bare-metal cgroup/PMU access"

readonly PROFILER_BIN="${ROOT_DIR}/_output/bin/profiler"
readonly PROFILER_BPF="${ROOT_DIR}/_output/bpf/native_cpu_profiler.o"
readonly FIXTURE_SRC="${ROOT_DIR}/integration/testdata/test_profiler_callchain.user.c"

command -v gcc > /dev/null || skip "gcc(1) not in PATH"
command -v taskset > /dev/null || skip "taskset(1) not in PATH"
[[ -x "${PROFILER_BIN}" ]] || fatal "profiler binary missing: ${PROFILER_BIN}"
[[ -r "${PROFILER_BPF}" ]] || fatal "native bpf object missing: ${PROFILER_BPF}"
[[ -r "${FIXTURE_SRC}" ]] || fatal "fixture source missing: ${FIXTURE_SRC}"
[[ -r /proc/sys/kernel/perf_event_paranoid ]] || skip "perf_event_paranoid not readable: perf unavailable"
readonly PARANOID=$(cat /proc/sys/kernel/perf_event_paranoid)
[[ "${PARANOID}" -le 2 ]] || skip "kernel.perf_event_paranoid=${PARANOID} (>2) blocks perf sampling"
[[ "$(nproc)" -ge 2 ]] || skip "need at least 2 CPUs"

readonly CHAIN_PATTERN=';f1;f2;f3 [0-9]+$'

# --- workspace + cleanup -----------------------------------------------------

readonly FIXTURE_OUTDIR=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/profiler-cpuid.XXXXXX")
FIXTURE_BIN="${FIXTURE_OUTDIR}/callchain"
PROFILER_OUT="${FIXTURE_OUTDIR}/profiler.out"
PROFILER_ERR="${FIXTURE_OUTDIR}/profiler.err"
TARGET_PID=""

cleanup() {
	local rc=$?
	[[ -n "${TARGET_PID}" ]] && stop_by_pid "${TARGET_PID}" 5
	if [[ ${rc} -ne 0 ]]; then
		dump_file "profiler stdout" "${PROFILER_OUT}"
		dump_file "profiler stderr" "${PROFILER_ERR}"
		log_error "workspace preserved at ${FIXTURE_OUTDIR}"
	else
		rm -rf "${FIXTURE_OUTDIR}"
	fi
}
trap cleanup EXIT

# --- build fixture -----------------------------------------------------------

log_info "compiling fixture"
gcc -O0 -g -fno-inline -fno-omit-frame-pointer \
	-o "${FIXTURE_BIN}" "${FIXTURE_SRC}" \
	2> "${FIXTURE_OUTDIR}/gcc.err" \
	|| fatal "gcc failed:"$'\n'"$(< "${FIXTURE_OUTDIR}/gcc.err")"

# --- helper ------------------------------------------------------------------

# verify_cpuid_chain <pin_cpu> <cpuid> <expect_chain>
#   pin_cpu:       CPU to pin the fixture process to
#   cpuid:         --cpuid value passed to the profiler
#   expect_chain:  "present" or "absent"
verify_cpuid_chain() {
	local pin_cpu=$1 cpuid=$2 expect_chain=$3
	local out_dir="${FIXTURE_OUTDIR}/${pin_cpu}"
	local folded_glob="${out_dir}/perf_*.folded"
	local match_count=0

	log_info "fixture on CPU${pin_cpu}, profiler --cpuid ${cpuid}, expect chain ${expect_chain}"
	mkdir -p "${out_dir}"

	taskset -c "${pin_cpu}" "${FIXTURE_BIN}" > /dev/null 2>&1 &
	TARGET_PID=$!
	kill -0 "${TARGET_PID}" 2> /dev/null || fatal "fixture exited immediately"

	"${PROFILER_BIN}" --type cpu --language c --pid "${TARGET_PID}" \
		--cpuid "${cpuid}" --duration 10 --freq 99 \
		--output-format collapsed --output-path "${out_dir}" \
		--aggr-interval 5 \
		> "${PROFILER_OUT}" 2> "${PROFILER_ERR}" \
		|| fatal "profiler exited non-zero"

	stop_by_pid "${TARGET_PID}" 5
	TARGET_PID=""

	if compgen -G "${folded_glob}" > /dev/null; then
		match_count=$(grep -hE "${CHAIN_PATTERN}" ${folded_glob} | wc -l) || true
	elif [[ "${expect_chain}" == "present" ]]; then
		fatal "no folded file produced"
	fi

	case "${expect_chain}" in
	present)
		[[ "${match_count}" -ge 1 ]] || fail_with_folded "${out_dir}" "chain not found (matches=${match_count})"
		log_info "chain matched (${match_count} lines)"
		;;
	absent)
		[[ "${match_count}" -eq 0 ]] || fail_with_folded "${out_dir}" "chain unexpectedly found (matches=${match_count})"
		log_info "chain absent as expected"
		;;
	esac
}

fail_with_folded() {
	local out_dir=$1
	local message=$2

	log_error "folded contents in ${out_dir}:"
	find "${out_dir}" -name 'perf_*.folded' -type f -print -exec cat {} \; >&2
	fatal "${message}"
}

# --- tests -------------------------------------------------------------------

verify_cpuid_chain 0 0 present
verify_cpuid_chain 1 0 absent

log_info "all cpuid tests passed"
