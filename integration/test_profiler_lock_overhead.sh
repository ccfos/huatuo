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

is_container && skip "lock profiler benchmark requires bare-metal BPF access"
[[ ${EUID} -eq 0 ]] || skip "lock profiler benchmark requires root"

readonly PROFILER_BIN="${ROOT_DIR}/_output/bin/profiler"
readonly PROFILER_BPF="${ROOT_DIR}/_output/bpf/native_mutex_profiler.o"
readonly FIXTURE_DIR="${ROOT_DIR}/integration/testdata/mutexprof_fixture"
readonly KERNEL_BUILD_DIR="/lib/modules/$(uname -r)/build"
readonly TRIALS=3
readonly WORKLOAD_SECONDS=4
readonly START_DELAY_SECONDS=2
readonly MAX_SLOWDOWN_PERCENT=${HUATUO_LOCK_MAX_SLOWDOWN_PERCENT:-50}

[[ -x "${PROFILER_BIN}" ]] || fatal "profiler binary missing: ${PROFILER_BIN}"
[[ -r "${PROFILER_BPF}" ]] || fatal "BPF object missing: ${PROFILER_BPF}"
[[ -d "${KERNEL_BUILD_DIR}" ]] || skip "matching kernel headers unavailable"
[[ "${MAX_SLOWDOWN_PERCENT}" =~ ^[0-9]+$ ]] \
	|| fatal "HUATUO_LOCK_MAX_SLOWDOWN_PERCENT must be an integer"
((MAX_SLOWDOWN_PERCENT < 100)) \
	|| fatal "HUATUO_LOCK_MAX_SLOWDOWN_PERCENT must be less than 100"

if [[ ! -r /sys/kernel/tracing/events/lock/contention_begin/id ]] \
	&& [[ ! -r /sys/kernel/debug/tracing/events/lock/contention_begin/id ]] \
	&& ! kprobe_available __mutex_lock_slowpath; then
	skip "kernel exposes no supported mutex contention hook"
fi

WORK_DIR=$(mktemp -d \
	"${HUATUO_BAMAI_TEST_TMPDIR}/profiler-lock-overhead.XXXXXX")
MODULE_DIR="${WORK_DIR}/module"
MODULE_NAME="huatuo_mutexprof_test"
DEVICE_PATH="${WORK_DIR}/huatuo_mutexprof_fixture"
WORKLOAD_BIN="${WORK_DIR}/mutexprof_workload"
PROFILER_PID=""
TARGET_PID=""

cleanup() {
	[[ -n "${PROFILER_PID}" ]] \
		&& stop_by_pid "${PROFILER_PID}" 5 || true
	[[ -n "${TARGET_PID}" ]] \
		&& stop_by_pid "${TARGET_PID}" 5 || true
	rmmod "${MODULE_NAME}" > /dev/null 2>&1 || true
}
trap cleanup EXIT

mkdir -p "${MODULE_DIR}"
cp "${FIXTURE_DIR}/Makefile" "${MODULE_DIR}/"
cp "${FIXTURE_DIR}/huatuo_mutexprof_test.kernel.c" \
	"${MODULE_DIR}/huatuo_mutexprof_test.c"
if ! make -s -C "${KERNEL_BUILD_DIR}" M="${MODULE_DIR}" modules \
	> "${WORK_DIR}/module-build.log" 2>&1; then
	fatal "failed to build kernel mutex fixture"
fi

rmmod "${MODULE_NAME}" > /dev/null 2>&1 || true
insmod "${MODULE_DIR}/${MODULE_NAME}.ko" \
	|| fatal "failed to load ${MODULE_NAME}.ko"

readonly DEVICE_SYSFS="/sys/class/misc/huatuo_mutexprof_fixture/dev"
wait_until 5 1 test -r "${DEVICE_SYSFS}" \
	|| fatal "fixture miscdevice did not appear"
readonly DEVICE_NUMBER=$(< "${DEVICE_SYSFS}")
mknod "${DEVICE_PATH}" c "${DEVICE_NUMBER%:*}" "${DEVICE_NUMBER#*:}"
chmod 600 "${DEVICE_PATH}"
compile_user_fixture \
	"${FIXTURE_DIR}/mutexprof_workload.user.c" \
	"${WORKLOAD_BIN}" \
	-pthread

run_trial() {
	local mode=$1 trial=$2
	local workload_out="${WORK_DIR}/${mode}-${trial}.workload"
	local profiler_out="${WORK_DIR}/${mode}-${trial}.profiler.out"
	local profiler_err="${WORK_DIR}/${mode}-${trial}.profiler.err"

	mkdir -p "${WORK_DIR}/${mode}-${trial}"
	"${WORKLOAD_BIN}" "${DEVICE_PATH}" \
		"${WORKLOAD_SECONDS}" "${START_DELAY_SECONDS}" \
		> "${workload_out}" 2> "${workload_out}.err" &
	TARGET_PID=$!

	if [[ "${mode}" == "profiled" ]]; then
		timeout --signal=TERM --kill-after=5s 20s \
			"${PROFILER_BIN}" \
			--type lock \
			--language c \
			--lock-type mutex \
			--pid "${TARGET_PID}" \
			--thread-group \
			--lock-wait-threshold 1us \
			--duration 8 \
			--aggr-interval 2 \
			--output-format collapsed \
			--output-path "${WORK_DIR}/${mode}-${trial}" \
			--verbose \
			> "${profiler_out}" 2> "${profiler_err}" &
		PROFILER_PID=$!
		wait_until "${START_DELAY_SECONDS}" 1 \
			profiler_ready "${profiler_out}" >&2 \
			|| fatal "profiler was not ready before workload started"
	fi

	wait "${TARGET_PID}" || fatal "${mode} workload trial ${trial} failed"
	TARGET_PID=""
	if [[ -n "${PROFILER_PID}" ]]; then
		wait "${PROFILER_PID}" \
			|| fatal "profiler failed during overhead trial ${trial}"
		PROFILER_PID=""
	fi

	awk -F= '/^operations=[0-9]+$/ { print $2; found=1 }
		END { exit !found }' "${workload_out}"
}

median() {
	sort -n | awk 'NR == 2 { print; found=1 }
		END { exit !found }'
}

baseline_results=()
profiled_results=()
for ((trial = 1; trial <= TRIALS; trial++)); do
	baseline_results+=("$(run_trial baseline "${trial}")")
	profiled_results+=("$(run_trial profiled "${trial}")")
done

baseline_median=$(printf '%s\n' "${baseline_results[@]}" | median)
profiled_median=$(printf '%s\n' "${profiled_results[@]}" | median)
minimum_profiled=$(
	awk -v baseline="${baseline_median}" \
		-v limit="${MAX_SLOWDOWN_PERCENT}" \
		'BEGIN { printf "%.0f", baseline * (100 - limit) / 100 }'
)

if ((profiled_median < minimum_profiled)); then
	fatal "median mutex throughput ${profiled_median} is below " \
		"${minimum_profiled}; baseline=${baseline_median}, " \
		"slowdown limit=${MAX_SLOWDOWN_PERCENT}%"
fi

log_info "lock overhead benchmark passed: baseline=${baseline_median}, " \
	"profiled=${profiled_median}, limit=${MAX_SLOWDOWN_PERCENT}%"
