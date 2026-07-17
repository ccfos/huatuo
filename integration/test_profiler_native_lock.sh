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

# Exercise mutex, spinlock, and rwlock contention in the target process's
# syscall context. A short watchdog and dmesg check guard against the global
# stalls caused by probing every raw lock acquisition path.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

is_container && skip "native lock profiler requires bare-metal kernel module and BPF access"

readonly PROFILER_BIN="${ROOT_DIR}/_output/bin/profiler"
readonly PROFILER_BPF="${ROOT_DIR}/_output/bpf/native_lock_profiler.o"
readonly FIXTURE_SRC_DIR="${ROOT_DIR}/integration/testdata/lockprof_fixture"
readonly KERNEL_BUILD_DIR="/lib/modules/$(uname -r)/build"
readonly PROFILER_DURATION=8
readonly PROFILER_READY_TIMEOUT=10
readonly CASE_TIMEOUT=20

[[ -x "${PROFILER_BIN}" ]] || fatal "profiler binary missing: ${PROFILER_BIN}"
[[ -r "${PROFILER_BPF}" ]] || fatal "native lock profiler BPF object missing: ${PROFILER_BPF}"
[[ -d "${KERNEL_BUILD_DIR}" ]] || skip "matching kernel headers unavailable: ${KERNEL_BUILD_DIR}"
command -v timeout > /dev/null || skip "timeout(1) not in PATH"
command -v insmod > /dev/null || skip "insmod(8) not in PATH"
command -v rmmod > /dev/null || skip "rmmod(8) not in PATH"
command -v mknod > /dev/null || skip "mknod(1) not in PATH"

WORK_DIR=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/profiler-lock.XXXXXX")
MODULE_DIR="${WORK_DIR}/module"
MODULE_LOG="${WORK_DIR}/module-build.log"
MODULE_NAME="huatuo_lockprof_test"
DEVICE_PATH="${WORK_DIR}/huatuo_lockprof_fixture"
WORKLOAD_BIN="${WORK_DIR}/lockprof_workload"
PROFILER_PID=""
TARGET_PID=""

cleanup() {
	[[ -n "${PROFILER_PID}" ]] && stop_by_pid "${PROFILER_PID}" 5 || true
	[[ -n "${TARGET_PID}" ]] && stop_by_pid "${TARGET_PID}" 5 || true
	PROFILER_PID=""
	TARGET_PID=""
	rmmod "${MODULE_NAME}" > /dev/null 2>&1 || true
}
trap cleanup EXIT

mkdir -p "${MODULE_DIR}"
cp "${FIXTURE_SRC_DIR}/Makefile" "${FIXTURE_SRC_DIR}/huatuo_lockprof_test.c" "${MODULE_DIR}/"
if ! make -s -C "${KERNEL_BUILD_DIR}" M="${MODULE_DIR}" modules > "${MODULE_LOG}" 2>&1; then
	fatal "failed to build kernel lock fixture module"
fi

rmmod "${MODULE_NAME}" > /dev/null 2>&1 || true
insmod "${MODULE_DIR}/${MODULE_NAME}.ko" || fatal "failed to load ${MODULE_NAME}.ko"

readonly DEVICE_SYSFS="/sys/class/misc/huatuo_lockprof_fixture/dev"
wait_until 5 1 test -r "${DEVICE_SYSFS}" || fatal "fixture miscdevice did not appear"
readonly DEVICE_NUMBER=$(< "${DEVICE_SYSFS}")
readonly DEVICE_MAJOR=${DEVICE_NUMBER%:*}
readonly DEVICE_MINOR=${DEVICE_NUMBER#*:}
mknod "${DEVICE_PATH}" c "${DEVICE_MAJOR}" "${DEVICE_MINOR}"
chmod 600 "${DEVICE_PATH}"

compile_user_fixture \
	"${FIXTURE_SRC_DIR}/lockprof_workload.user.c" \
	"${WORKLOAD_BIN}" \
	-pthread

readonly DMESG_START=$(dmesg 2> /dev/null | wc -l)

run_lock_case() {
	local lock_type=$1 lock_mode=$2
	local case_dir="${WORK_DIR}/${lock_type}"
	local profiler_out="${case_dir}/profiler.out"
	local profiler_err="${case_dir}/profiler.err"
	local workload_out="${case_dir}/workload.out"
	local workload_err="${case_dir}/workload.err"

	mkdir -p "${case_dir}"
	log_info "running deterministic ${lock_type} contention (${lock_mode} mode)"
	"${WORKLOAD_BIN}" "${DEVICE_PATH}" "${lock_type}" \
		> "${workload_out}" 2> "${workload_err}" &
	TARGET_PID=$!
	kill -0 "${TARGET_PID}" 2> /dev/null || fatal "${lock_type} workload exited immediately"

	timeout --signal=TERM --kill-after=5s "${CASE_TIMEOUT}s" \
		"${PROFILER_BIN}" \
		--type lock \
		--language c \
		--scope tgid \
		--pid "${TARGET_PID}" \
		--lock-types "${lock_type}" \
		--lock-mode "${lock_mode}" \
		--lock-min-wait 0ns \
		--duration "${PROFILER_DURATION}" \
		--aggr-interval 2 \
		--output-format collapsed \
		--output-path "${case_dir}" \
		--verbose \
		> "${profiler_out}" 2> "${profiler_err}" &
	PROFILER_PID=$!

	wait_until "${PROFILER_READY_TIMEOUT}" 1 profiler_ready "${profiler_out}" \
		|| fatal "${lock_type} profiler did not enter its read loop"

	if wait "${PROFILER_PID}"; then
		:
	else
		local status=$?
		PROFILER_PID=""
		fatal "${lock_type} profiler failed or timed out (status=${status})"
	fi
	PROFILER_PID=""

	stop_by_pid "${TARGET_PID}" 5
	if ! wait "${TARGET_PID}"; then
		TARGET_PID=""
		fatal "${lock_type} workload exited non-zero"
	fi
	TARGET_PID=""

	mapfile -t folded_files < <(find "${case_dir}" -maxdepth 1 -name 'perf_*.folded' -type f)
	[[ ${#folded_files[@]} -gt 0 ]] || fatal "${lock_type}: no folded output produced"
	grep -q "lock type: ${lock_type}" "${folded_files[@]}" \
		|| fatal "${lock_type}: expected lock type frame not found"
	awk 'NF && $NF ~ /^[0-9]+$/ && $NF > 0 { found=1 } END { exit !found }' "${folded_files[@]}" \
		|| fatal "${lock_type}: no positive ${lock_mode} value found"

	log_info "${lock_type}/${lock_mode} contention profile passed"
}

run_lock_case mutex time
run_lock_case spinlock count
run_lock_case rwlock count

if dmesg 2> /dev/null | tail -n "+$((DMESG_START + 1))" \
	| grep -Eiq 'soft lockup|rcu[^:]*stall|hung task|watchdog: BUG'; then
	fatal "kernel reported a stall while lock profiling"
fi

log_info "all native lock profiling cases passed without kernel stalls"
