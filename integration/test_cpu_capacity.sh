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

readonly DELEGATED_ROOT=${HUATUO_CPU_CAPACITY_CGROUP_ROOT:-}
[[ -n "${DELEGATED_ROOT}" ]] \
	|| skip "set HUATUO_CPU_CAPACITY_CGROUP_ROOT to an explicitly delegated cgroup v2 directory"
[[ -d "${DELEGATED_ROOT}" ]] \
	|| skip "delegated cgroup directory does not exist: ${DELEGATED_ROOT}"
[[ -w "${DELEGATED_ROOT}" ]] \
	|| skip "delegated cgroup directory is not writable: ${DELEGATED_ROOT}"
command -v go > /dev/null || skip "go command is not installed"

readonly WORK_DIR=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/cpu-capacity.XXXXXX")
readonly PROBE_BIN="${WORK_DIR}/cpu-capacity"
readonly PROBE_LOG="${WORK_DIR}/cpu-capacity.log"

go build -mod=vendor -tags=integration -o "${PROBE_BIN}" \
	"${ROOT_DIR}/integration/testdata/cpu_capacity.go" \
	> "${WORK_DIR}/build.log" 2>&1 \
	|| fatal "failed to build CPU capacity probe:"$'\n'"$(< "${WORK_DIR}/build.log")"

# The probe verifies cgroup2 mount/delegation before creating an owned subtree.
# It never remounts cgroupfs or modifies the supplied root's controller settings.
if "${PROBE_BIN}" "${DELEGATED_ROOT}" > "${PROBE_LOG}" 2>&1; then
	log_info "$(< "${PROBE_LOG}")"
	log_info "real cgroup v2 CPU capacity integration test passed"
else
	probe_status=$?
	[[ ${probe_status} -ne 77 ]] || skip "$(< "${PROBE_LOG}")"
	fatal "CPU capacity probe failed (${probe_status}):"$'\n'"$(< "${PROBE_LOG}")"
fi
