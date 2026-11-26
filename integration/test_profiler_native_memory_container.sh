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

# Verify container memory CSS, allocation bytes and stacks without comparing PID namespaces.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_profiler.sh"
source "${ROOT_DIR}/integration/lib_profiler_native_memory_container.sh"

cleanup() {
	local status=$?
	trap - EXIT
	if [[ ${status} -eq 77 && ${NATIVE_PROFILER_REQUIRE_SPLIT_CGROUP_V1:-0} == 1 ]]; then
		log_error "required split-v1 memory regression was skipped; expected a passing test"
		status=1
	fi
	cleanup_running_jobs || status=1
	native_container_stop || status=1
	exit "${status}"
}
trap cleanup EXIT

# Symbolization reads procfs using kernel sample PIDs.
require_native_pid_profiling
native_container_memory_collect
assert_native_container_memory_profile "${TOOL_WORK_DIR}/result.folded" "${TOOL_WORK_DIR}/container-logs.err" \
	|| fatal "container memory CSS profile validation failed"
log_info "container memory CSS, allocation bytes and stacks passed"
