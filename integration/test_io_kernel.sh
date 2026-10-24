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

# Run internal Go/BPF qualifications separately from public IO contracts.
# Wait for the test binary itself so cancellation cannot bypass owned cleanup.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
require_commands go setsid unshare ps

readonly WORK_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/io-kernel"
readonly EXECUTION_SECONDS=${TEST_IO_KERNEL_TIMEOUT_SECONDS:-300}
readonly CLEANUP_SECONDS=${TEST_IO_KERNEL_CLEANUP_TIMEOUT_SECONDS:-60}
[[ ${EXECUTION_SECONDS} =~ ^[1-9][0-9]*$ && ${CLEANUP_SECONDS} =~ ^[1-9][0-9]*$ ]] \
	|| fatal "IO kernel execution and cleanup budgets must be positive integers"
mkdir -p "${WORK_DIR}/build"

test_pid=""
interrupted=0
trap 'interrupted=130' INT
trap 'interrupted=143' TERM

io_kernel_session_active() {
	ps -eo sid=,stat= | awk -v session="$1" '
		$1 == session && $2 !~ /^Z/ { found = 1 }
		END { exit !found }
	'
}

signal_io_kernel_session() {
	local signal=$1 group
	# fio can create its own process group inside the test-owned session.
	while read -r group; do
		kill "-${signal}" -- "-${group}" 2> /dev/null || true
	done < <(ps -eo sid=,pgid= | awk -v session="${test_pid}" '$1 == session && !seen[$2]++ { print $2 }')
}

wait_io_kernel_command() {
	local deadline=$((SECONDS + $1)) stop_status=0 cleanup_deadline=0 status=0 failed=0
	while ! process_exited "${test_pid}"; do
		if ((stop_status == 0)); then
			if ((interrupted)); then
				stop_status=${interrupted}
			elif ((SECONDS >= deadline)); then
				stop_status=124
			fi
			if ((stop_status)); then
				# Signal only the binary first; its cleanup commands need to finish.
				kill -TERM "${test_pid}" 2> /dev/null || true
				cleanup_deadline=$((SECONDS + CLEANUP_SECONDS))
			fi
		elif ((SECONDS >= cleanup_deadline)); then
			log_error "IO kernel cleanup exceeded ${CLEANUP_SECONDS}s: pid=${test_pid}; inspect ${WORK_DIR} before reclaiming owned resources"
			signal_io_kernel_session KILL
			failed=1
			break
		fi
		sleep 0.1 || true
	done

	# A finished leader must not leave workload descendants behind.
	if io_kernel_session_active "${test_pid}"; then
		failed=1
		signal_io_kernel_session TERM
		((cleanup_deadline > 0)) || cleanup_deadline=$((SECONDS + CLEANUP_SECONDS))
		while io_kernel_session_active "${test_pid}" && ((SECONDS < cleanup_deadline)); do
			sleep 0.1 || true
		done
		if io_kernel_session_active "${test_pid}"; then
			signal_io_kernel_session KILL
			wait_until 2 0.1 io_kernel_session_stopped "${test_pid}" || true
		fi
	fi
	if ! process_exited "${test_pid}" || io_kernel_session_active "${test_pid}"; then
		ps -eo pid=,ppid=,sid=,pgid=,stat=,args= | awk -v session="${test_pid}" '$3 == session' \
			> "${WORK_DIR}/retained-processes.txt"
		log_error "IO kernel processes remain in session ${test_pid}; retain owned resources and inspect ${WORK_DIR}"
		status=1
	else
		# Trapped signals can interrupt wait before the child has been reaped.
		while true; do
			if wait "${test_pid}"; then status=0; else status=$?; fi
			process_exited "${test_pid}" && break
		done
	fi
	printf 'pid=%s test_status=%s stop_status=%s cleanup_failed=%s\n' \
		"${test_pid}" "${status}" "${stop_status}" "${failed}" >> "${WORK_DIR}/resources.txt"
	test_pid=""
	((failed == 0)) || return 1
	((stop_status == 0)) || return "${stop_status}"
	((status == 0)) || return "${status}"
	((interrupted == 0)) || return "${interrupted}"
}

io_kernel_session_stopped() {
	! io_kernel_session_active "$1"
}

cleanup_io_kernel() {
	local status=$?
	trap - EXIT
	if ((status == 0 && interrupted)); then status=${interrupted}; fi
	if [[ -n ${test_pid} ]]; then
		interrupted=143
		wait_io_kernel_command 0 || status=1
	fi
	if ((status)); then
		log_error "IO kernel qualification failed (${status}); final test and cleanup results: ${WORK_DIR}"
	fi
	exit "${status}"
}
trap cleanup_io_kernel EXIT

run_io_kernel_command() {
	local label=$1 budget=$2 directory=$3 status=0
	shift 3
	((interrupted == 0)) || exit "${interrupted}"
	printf 'command=%s\n' "${label}" >> "${WORK_DIR}/resources.txt"
	(
		cd "${directory}"
		exec setsid "$@"
	) > "${WORK_DIR}/${label}.log" 2>&1 &
	test_pid=$!
	wait_io_kernel_command "${budget}" || status=$?
	cat "${WORK_DIR}/${label}.log"
	((status == 0)) || exit "${status}"
}

collectors=(
	core/metrics:TestBlkThrotlKernelThrottleLifecycle
	core/metrics:TestBlkThrotlKernelPendingCapacityFailure
	core/metrics:TestBlkThrotlStartAttachSmoke
	core/metrics:TestIOHealthFreezeKernel
	core/metrics:TestIOHealthRealCommandEvidence
	core/metrics:TestIOHealthRealTargetResolution
	core/metrics:TestIOLatencyArgumentProbeKernel
	core/metrics:TestIOLatencyCryptoProbeKernel
)
iocost=(
	core/metrics:TestIOCostKernelFunctional
	core/metrics:TestIOCostKernelCompatibility
	core/metrics:TestIOCostKernelFaults
	core/metrics:TestIOCostKernelLifecycle
	core/metrics:TestIOCostKernelPressure
)
md=(internal/iohealth:TestMDWatcherMonitorsMdadmLoopArray)
iotracing=(cmd/iotracing:TestIOTracingKernel)
case ${TEST_IO_KERNEL_MODE:-all} in
all) tests=("${collectors[@]}" "${md[@]}" "${iotracing[@]}" "${iocost[@]}") ;;
collectors) tests=("${collectors[@]}") ;;
md) tests=("${md[@]}") ;;
iotracing) tests=("${iotracing[@]}") ;;
iocost) tests=("${iocost[@]}") ;;
*) fatal "TEST_IO_KERNEL_MODE must be all, collectors, md, iotracing or iocost" ;;
esac

# Compile before any Go fixture creates devices. Shared packages build once.
for entry in "${tests[@]}"; do
	package=${entry%:*}
	binary="${WORK_DIR}/${package//\//.}.test"
	[[ ! -x ${binary} ]] || continue
	run_io_kernel_command "build-${package//\//.}" 120 "${ROOT_DIR}" \
		env GOTMPDIR="${WORK_DIR}/build" go test -mod=vendor -p=1 -tags=integration \
		-c -o "${binary}" "./${package}"
done

deadline=$((SECONDS + EXECUTION_SECONDS))
for entry in "${tests[@]}"; do
	remaining=$((deadline - SECONDS))
	((remaining > 0)) || exit 124
	package=${entry%:*}
	name=${entry#*:}
	run_io_kernel_command "${name}" "${remaining}" "${ROOT_DIR}/${package}" \
		unshare --mount --propagation private env TEST_INTEGRATION=true \
		"${WORK_DIR}/${package//\//.}.test" -test.count=1 -test.v -test.failfast \
		-test.timeout="$((remaining + CLEANUP_SECONDS + 5))s" -test.run="^${name}$"
done
log_info "IO internal Go/BPF qualifications completed; public CLI contracts have separate cases"
