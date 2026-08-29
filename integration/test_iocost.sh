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

# Qualify IOCOST accounting on the live kernel with owned devices and cgroups.
set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

case ${TEST_IOCOST_MODE:-all} in
functional) tests=(Functional) ;;
lifecycle) tests=(Lifecycle) ;;
faults) tests=(Faults) ;;
pressure) tests=(Pressure) ;;
compat) tests=(Compatibility) ;;
all) tests=(Functional Compatibility Faults Lifecycle Pressure) ;;
*) fatal "unknown IOCOST qualification mode: ${TEST_IOCOST_MODE}" ;;
esac

if [[ -e /sys/fs/cgroup/cgroup.controllers ]]; then
	cgroup_root=/sys/fs/cgroup
	control_prefix=io
else
	cgroup_root=/sys/fs/cgroup/blkio
	control_prefix=blkio
fi
[[ -w ${cgroup_root}/${control_prefix}.cost.model && -w ${cgroup_root}/${control_prefix}.cost.qos ]] \
	|| skip 'IOCOST qualification requires writable io.cost or blkio.cost interfaces'
require_commands go flock
cd "${ROOT_DIR}"
mkdir -p /run/huatuo-iocost-test _output/test-bpf
exec 9> /run/huatuo-iocost-test/runner.lock
flock -n 9 || fatal 'another IOCOST qualification is running'

"${ROOT_DIR}/build/clang.sh" -I "${ROOT_DIR}/bpf/include" \
	-s "${ROOT_DIR}/integration/testdata/iocost_tracing_test.c" \
	-o "${ROOT_DIR}/_output/test-bpf/iocost_tracing_test.o"
export TEST_INTEGRATION=true
results_file="${HUATUO_BAMAI_TEST_TMPDIR}/iocost-results.log"
test_binary="${HUATUO_BAMAI_TEST_TMPDIR}/iocost-kernel.test"
go test -mod=vendor -p=1 -c -o "${test_binary}" ./core/metrics

# Signal the test binary, not the go command, and wait for its owned cleanup.
test_pid=""
interrupted=0
forward_interrupt() {
	interrupted=$1
	[[ -z ${test_pid} ]] || kill -TERM "${test_pid}" 2> /dev/null || true
}
trap 'forward_interrupt 130' INT
trap 'forward_interrupt 143' TERM
deadline=$((SECONDS + 300))
: > "${results_file}"
status=0
# A signal during successful cleanup need not fail that Go test. Keep
# the interruption in this runner and check it before starting each next one.
for test_name in "${tests[@]}"; do
	((interrupted == 0)) || break
	remaining=$((deadline - SECONDS))
	# Each fixture reserves the final minute for cleanup before its hard timeout.
	if ((remaining <= 60)); then
		log_error "IOCOST qualification deadline reached before ${test_name}"
		status=124
		break
	fi
	(
		cd "${ROOT_DIR}/core/metrics"
		exec "${test_binary}" -test.count=1 -test.v -test.failfast \
			-test.timeout="${remaining}s" -test.run="^TestIOCostKernel${test_name}$"
	) >> "${results_file}" 2>&1 &
	test_pid=$!
	if ((interrupted)); then
		kill -TERM "${test_pid}" 2> /dev/null || true
	fi
	while true; do
		if wait "${test_pid}"; then
			status=0
			break
		else
			status=$?
		fi
		# A trapped signal interrupts wait before the test has finished cleanup.
		kill -0 "${test_pid}" 2> /dev/null || break
	done
	test_pid=""
	if ((interrupted || status)); then
		break
	fi
done
trap - INT TERM
cat "${results_file}"
if ((interrupted)); then
	log_error "IOCOST interrupted; see owned-resource cleanup results above"
	exit "${interrupted}"
fi
if ((status)); then
	log_error "IOCOST exited ${status}; check cleanup errors and owned-resource paths above"
	exit "${status}"
fi

if ! grep -q '^--- PASS: TestIOCostKernel' "${results_file}"; then
	grep -q '^--- SKIP: TestIOCostKernel' "${results_file}" \
		|| fatal 'IOCOST qualification reported no test results'
	skip 'all selected IOCOST qualification tests were skipped'
fi
