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

# Run production IOCOST sessions with owned devices and cgroups.
set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

case ${TEST_IOCOST_MODE:-all} in
functional) pattern='^TestIOCostKernelFunctional$' ;;
lifecycle) pattern='^TestIOCostKernelLifecycle$' ;;
faults) pattern='^TestIOCostKernelFaults$' ;;
pressure) pattern='^TestIOCostKernelPressure$' ;;
compat) pattern='^TestIOCostKernelCompatibility$' ;;
all) pattern='^TestIOCostKernel(Functional|Lifecycle|Faults|Pressure|Compatibility)$' ;;
*) fatal "unknown IOCOST qualification mode: ${TEST_IOCOST_MODE}" ;;
esac

[[ -w /sys/fs/cgroup/io.cost.model && -w /sys/fs/cgroup/io.cost.qos ]] \
	|| skip 'IOCOST qualification requires writable cgroup v2 io.cost interfaces'
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
go test -mod=vendor -p=1 -count=1 -v -timeout=5m \
	-run "${pattern}" ./core/metrics | tee "${results_file}"

if ! grep -q '^--- PASS: TestIOCostKernel' "${results_file}"; then
	grep -q '^--- SKIP: TestIOCostKernel' "${results_file}" \
		|| fatal 'IOCOST qualification reported no test results'
	skip 'all selected IOCOST qualification tests were skipped'
fi
