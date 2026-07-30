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

is_container && skip "native rwlock profiler requires bare-metal BPF access"
[[ ${EUID} -eq 0 ]] || skip "native rwlock profiler requires root"

readonly PROFILER_BIN="${ROOT_DIR}/_output/bin/profiler"
readonly PROFILER_BPF="${ROOT_DIR}/_output/bpf/native_rwlock_profiler.o"
readonly FIXTURE_DIR="${ROOT_DIR}/integration/testdata/rwlockprof_fixture"
readonly KERNEL_BUILD_DIR="/lib/modules/$(uname -r)/build"

[[ -x "${PROFILER_BIN}" ]] || fatal "profiler binary missing: ${PROFILER_BIN}"
[[ -r "${PROFILER_BPF}" ]] || fatal "BPF object missing: ${PROFILER_BPF}"
[[ -d "${KERNEL_BUILD_DIR}" ]] || skip "matching kernel headers unavailable"
command -v timeout > /dev/null || skip "timeout(1) not in PATH"
command -v insmod > /dev/null || skip "insmod(8) not in PATH"
command -v rmmod > /dev/null || skip "rmmod(8) not in PATH"
command -v mknod > /dev/null || skip "mknod(1) not in PATH"

if [[ ! -r /sys/kernel/tracing/events/lock/contention_begin/id ]] \
	&& [[ ! -r /sys/kernel/debug/tracing/events/lock/contention_begin/id ]] \
	&& {
		! kprobe_available queued_read_lock_slowpath \
			|| ! kprobe_available queued_write_lock_slowpath
	}; then
	skip "kernel exposes no supported contention-only rwlock hooks"
fi

WORK_DIR=$(mktemp -d \
	"${HUATUO_BAMAI_TEST_TMPDIR}/profiler-rwlock.XXXXXX")
MODULE_DIR="${WORK_DIR}/module"
MODULE_NAME="huatuo_rwlockprof_test"
DEVICE_PATH="${WORK_DIR}/huatuo_rwlockprof_fixture"
WORKLOAD_BIN="${WORK_DIR}/rwlockprof_workload"
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
cp "${FIXTURE_DIR}/huatuo_rwlockprof_test.kernel.c" \
	"${MODULE_DIR}/huatuo_rwlockprof_test.c"
if ! make -s -C "${KERNEL_BUILD_DIR}" M="${MODULE_DIR}" modules \
	> "${WORK_DIR}/module-build.log" 2>&1; then
	fatal "failed to build kernel rwlock fixture"
fi

rmmod "${MODULE_NAME}" > /dev/null 2>&1 || true
insmod "${MODULE_DIR}/${MODULE_NAME}.ko" \
	|| fatal "failed to load ${MODULE_NAME}.ko"

readonly DEVICE_SYSFS="/sys/class/misc/huatuo_rwlockprof_fixture/dev"
wait_until 5 1 test -r "${DEVICE_SYSFS}" \
	|| fatal "fixture miscdevice did not appear"
readonly DEVICE_NUMBER=$(< "${DEVICE_SYSFS}")
mknod "${DEVICE_PATH}" c "${DEVICE_NUMBER%:*}" "${DEVICE_NUMBER#*:}"
chmod 600 "${DEVICE_PATH}"

compile_user_fixture \
	"${FIXTURE_DIR}/rwlockprof_workload.user.c" \
	"${WORKLOAD_BIN}" \
	-pthread

readonly DMESG_START=$(dmesg 2> /dev/null | wc -l)
"${WORKLOAD_BIN}" "${DEVICE_PATH}" \
	> "${WORK_DIR}/workload.out" 2> "${WORK_DIR}/workload.err" &
TARGET_PID=$!

timeout --signal=TERM --kill-after=5s 20s \
	"${PROFILER_BIN}" \
	--type lock \
	--language c \
	--lock-type rwlock \
	--lock-mode count \
	--pid "${TARGET_PID}" \
	--thread-group \
	--lock-wait-threshold 1us \
	--duration 8 \
	--aggr-interval 2 \
	--output-format collapsed \
	--output-path "${WORK_DIR}" \
	--verbose \
	> "${WORK_DIR}/profiler.out" 2> "${WORK_DIR}/profiler.err" &
PROFILER_PID=$!

wait_until 10 1 profiler_ready "${WORK_DIR}/profiler.out" \
	|| fatal "rwlock profiler did not enter its read loop"
if ! wait "${PROFILER_PID}"; then
	PROFILER_PID=""
	fatal "rwlock profiler failed or timed out"
fi
PROFILER_PID=""

stop_by_pid "${TARGET_PID}" 5
wait "${TARGET_PID}" || fatal "rwlock workload exited non-zero"
TARGET_PID=""

mapfile -t folded_files < <(
	find "${WORK_DIR}" -maxdepth 1 -name 'perf_*.folded' -type f
)
[[ ${#folded_files[@]} -gt 0 ]] || fatal "no folded output produced"
grep -q "lock type: rwlock:" "${folded_files[@]}" \
	|| fatal "rwlock access frame not found"
awk 'NF && $NF ~ /^[0-9]+$/ && $NF > 0 { found=1 }
	END { exit !found }' "${folded_files[@]}" \
	|| fatal "no positive rwlock contention count found"

if dmesg 2> /dev/null | tail -n "+$((DMESG_START + 1))" \
	| grep -Eiq 'soft lockup|rcu[^:]*stall|hung task|watchdog: BUG'; then
	fatal "kernel reported a stall while profiling rwlock contention"
fi

log_info "native rwlock contention profile passed without kernel stalls"
