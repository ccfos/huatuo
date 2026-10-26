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

# Verify inherited membership, then the daemon's own startup/steady CPU and
# memory limits, including changed configuration after a graceful restart.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"
source "${ROOT_DIR}/integration/lib_cgroup.sh"

readonly LIMITS_FIXTURES="${HUATUO_BAMAI_TEST_TMPDIR}/fixtures"
readonly LIMITS_GATE="${LIMITS_FIXTURES}/dev/sda0"
readonly LIMITS_PID_FILE="${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid"
readonly LIMITS_LOCK_FILE=/run/lock/huatuo-bamai-runtime-limits.lock
# Bash does not export arrays to the child test process.
readonly CURL_TIMEOUT=(--connect-timeout 2 --max-time 3)

require_commands curl ss awk mkfifo flock dd
[[ -f "${HUATUO_BAMAI_TEST_FIXTURES}/dev/sda0" ]] || fatal "XFS device fixture is missing"

# The daemon uses fixed cgroup names across all test workspaces.
# Keep the lock file: unlinking it would let another run lock a different inode.
exec {limits_lock_fd}> "${LIMITS_LOCK_FILE}" || skip "cannot open runtime limits lock: ${LIMITS_LOCK_FILE}"
flock -n "${limits_lock_fd}" || skip "another runtime limits test holds ${LIMITS_LOCK_FILE}"

LIMITS_PORT=$(allocate_available_port) || fatal "failed to allocate a bamai port"
readonly LIMITS_PORT
HUATUO_BAMAI_METRICS_API="http://127.0.0.1:${LIMITS_PORT}/metrics"

huatuo_bamai_require_cgroup

limits_observer_pid=""
limits_cpu_path=""
limits_memory_path=""
limits_quota=""
limits_period=""
limits_memory=""

cleanup() {
	local status=$?
	stop_and_wait_by_pid "${limits_observer_pid}" 1 || true
	huatuo_bamai_stop || status=1
	# An initially absent group may now belong to another service. Let the
	# daemon clean up its own resources; never stop or delete a group by name.
	assert_limits_cgroups_removed || status=1
	exit "${status}"
}
trap cleanup EXIT

assert_limits_cgroups_removed() {
	local root status=0
	for root in "${RUNTIME_CGROUP_ROOTS[@]}"; do
		if [[ -d "${root}${RUNTIME_CGROUP_PATH}" ]]; then
			log_error "expected cgroup removal after shutdown; preserving unverified group: ${root}${RUNTIME_CGROUP_PATH}"
			status=1
		fi
	done
	return "${status}"
}

limits_locate_cgroup() {
	local pid cpu_path memory_path
	[[ -s "${LIMITS_PID_FILE}" ]] || return 1
	pid=$(< "${LIMITS_PID_FILE}")
	[[ -r /proc/${pid}/cgroup ]] || return 1

	if [[ ${RUNTIME_CGROUP_MODE} == v2 ]]; then
		cpu_path=$(awk -F: '$1 == "0" {print $3}' "/proc/${pid}/cgroup")
		memory_path=${cpu_path}
	else
		cpu_path=$(awk -F: '$2 ~ /(^|,)cpu(,|$)/ {print $3}' "/proc/${pid}/cgroup")
		memory_path=$(awk -F: '$2 ~ /(^|,)memory(,|$)/ {print $3}' "/proc/${pid}/cgroup")
	fi
	if [[ ${cpu_path} != "${RUNTIME_CGROUP_PATH}" || ${memory_path} != "${RUNTIME_CGROUP_PATH}" ]]; then
		log_error "daemon pid=${pid}: CPU cgroup=${cpu_path}, memory cgroup=${memory_path}; expected=${RUNTIME_CGROUP_PATH}"
		return 1
	fi

	limits_cpu_path="${RUNTIME_CGROUP_CPU_ROOT}${cpu_path}"
	limits_memory_path="${RUNTIME_CGROUP_MEMORY_ROOT}${memory_path}"
	grep -qx "${pid}" "${limits_cpu_path}/cgroup.procs" \
		&& grep -qx "${pid}" "${limits_memory_path}/cgroup.procs"
}

limits_match() {
	local cpu=$1 memory_mib=$2
	if [[ ${RUNTIME_CGROUP_MODE} == v2 ]]; then
		read -r limits_quota limits_period < "${limits_cpu_path}/cpu.max" || return 1
		limits_memory=$(< "${limits_memory_path}/memory.max")
	else
		limits_quota=$(< "${limits_cpu_path}/cpu.cfs_quota_us")
		limits_period=$(< "${limits_cpu_path}/cpu.cfs_period_us")
		limits_memory=$(< "${limits_memory_path}/memory.limit_in_bytes")
	fi

	[[ ${limits_memory} == "$((memory_mib * 1024 * 1024))" ]] || return 1
	awk -v quota="${limits_quota}" -v period="${limits_period}" -v cpu="${cpu}" \
		'BEGIN {exit !(quota > 0 && period > 0 && quota == cpu * period)}'
}

assert_limits() {
	local phase=$1 cpu=$2 memory_mib=$3
	limits_locate_cgroup || fatal "${phase}: expected daemon cgroup ${RUNTIME_CGROUP_PATH}; PID file: ${LIMITS_PID_FILE}"
	wait_until 10 0.1 limits_match "${cpu}" "${memory_mib}" \
		|| fatal "${phase}: CPU=${limits_quota}/${limits_period}, memory=${limits_memory}; expected ${cpu} cores, ${memory_mib} MiB"
	log_info "${phase}: ${limits_cpu_path} CPU=${limits_quota}/${limits_period}, memory=${limits_memory} bytes"
}

assert_limits_restart() {
	local previous_pid=$1 cpu=$2 memory_mib=$3
	limits_pid=$(< "${LIMITS_PID_FILE}")
	[[ ${limits_pid} != "${previous_pid}" ]] || fatal "replacement PID=${limits_pid} equals the stopped PID=${previous_pid}"
	assert_limits running "${cpu}" "${memory_mib}"
	huatuo_bamai_ready || fatal "replacement PID=${limits_pid} is not ready at ${HUATUO_BAMAI_METRICS_API}"
}

limits_observe_startup() {
	local cpu=$1 memory_mib=$2 status=0
	# Opening the write end waits for XFS to open the read end. Withhold bytes
	# until the assertion completes so initialization cannot switch CPU quotas.
	# The parent's readiness/completion deadlines also bound this blocked open.
	exec 3> "${LIMITS_GATE}"
	(
		# The FIFO rendezvous does not order the parent's PID-file publication.
		wait_until 10 0.1 test -s "${LIMITS_PID_FILE}" \
			|| fatal "daemon PID file was not published at the startup gate: ${LIMITS_PID_FILE}"
		assert_limits startup "${cpu}" "${memory_mib}"
	) || status=$?

	# Release initialization even on assertion failure, allowing normal cleanup.
	# XFS closes after reading its superblock; write the 512-byte fixture once.
	if ! dd if="${HUATUO_BAMAI_TEST_FIXTURES}/dev/sda0" bs=512 count=1 status=none >&3; then
		log_error "failed to write XFS fixture to startup FIFO: ${LIMITS_GATE}"
		return 1
	fi
	return "${status}"
}

assert_limits_shutdown() {
	local phase=$1 pid
	pid=$(< "${LIMITS_PID_FILE}")
	stop_gracefully_by_pid "${pid}" 20 || fatal "daemon did not exit gracefully"
	rm -f "${LIMITS_PID_FILE}"

	assert_limits_cgroups_removed || fatal "runtime cgroup remains after shutdown"

	huatuo_bamai_log_check || fatal "daemon logged an error during startup or shutdown"
	mv "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-${phase}.log"
}

# 1. Without --enable-cgroup, the daemon must inherit the launcher's membership.
write_runtime_limits_config "${LIMITS_PORT}" 0.75 1.25 512
integration_huatuo_bamai_start : --region dev --procfs-prefix "${HUATUO_BAMAI_TEST_FIXTURES}" \
	--disable-storage --disable-kubelet

limits_pid=$(< "${LIMITS_PID_FILE}")
assert_eq "$(< "/proc/${limits_pid}/cgroup")" "$(< "/proc/$$/cgroup")" \
	"inherited cgroup membership pid=${limits_pid}" || fatal "default startup did not inherit cgroup membership"
log_info "default startup inherits the test process's cgroup"
assert_limits_shutdown inherited

# 2. Isolate the fixture so the startup gate never changes the shared input.
cp -a "${HUATUO_BAMAI_TEST_FIXTURES}" "${LIMITS_FIXTURES}"
limits_old_pid=${limits_pid}

for limits_values in '0.75 1.25 512' '1.5 0.5 768'; do
	# 3. Each restart must apply the new configuration in both startup and runtime.
	read -r limits_startup_cpu limits_cpu limits_memory_mib <<< "${limits_values}"
	write_runtime_limits_config "${LIMITS_PORT}" "${limits_startup_cpu}" "${limits_cpu}" "${limits_memory_mib}"
	rm -f "${LIMITS_GATE}"
	mkfifo "${LIMITS_GATE}"

	# 4. The observer blocks at the FIFO until XFS initialization opens it.
	limits_observe_startup "${limits_startup_cpu}" "${limits_memory_mib}" \
		> "${HUATUO_BAMAI_TEST_TMPDIR}/startup-${limits_startup_cpu}.log" 2>&1 &
	limits_observer_pid=$!

	# 5. Startup limits are checked before the observer releases initialization.
	integration_huatuo_bamai_start : \
		--region dev --procfs-prefix "${LIMITS_FIXTURES}" \
		--disable-storage --disable-kubelet --enable-cgroup

	# 6. Readiness alone must not hide a lost gate or a failed startup assertion.
	wait_until 10 0.1 process_exited "${limits_observer_pid}" \
		|| fatal "startup observer did not finish; the XFS fixture gate was not released"
	limits_observer_status=0
	wait "${limits_observer_pid}" || limits_observer_status=$?
	limits_observer_pid=""
	cat "${HUATUO_BAMAI_TEST_TMPDIR}/startup-${limits_startup_cpu}.log"
	assert_eq "${limits_observer_status}" 0 "startup observer exit" || fatal "startup limits check failed"

	# 7. A ready replacement must use the runtime limits in its own cgroup.
	assert_limits_restart "${limits_old_pid}" "${limits_cpu}" "${limits_memory_mib}"

	# 8. Require a successful graceful exit and cgroup removal before restarting.
	assert_limits_shutdown "${limits_startup_cpu}"
	limits_old_pid=${limits_pid}
done
