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

# Compare Docker's workload PID with kernel sample PIDs only on the host.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_profiler.sh"
require_native_pid_profiling
source "${ROOT_DIR}/integration/lib_profiler_native_memory_container.sh"

cleanup() {
	local status=$?
	trap - EXIT
	cleanup_running_jobs || status=1
	native_container_stop || status=1
	exit "${status}"
}
trap cleanup EXIT

assert_container_memory_pid() {
	local folded_file=$1 expected_pid=$2 unexpected_sample
	[[ -s ${folded_file} ]] || {
		log_error "expected nonempty container memory profile: ${folded_file}"
		return 1
	}
	unexpected_sample=$(awk -v pid_label="pid=\"${expected_pid}\"" \
		'index($0, pid_label) == 0 {print "line " NR ": " $0; exit}' "${folded_file}") || return 1
	[[ -z ${unexpected_sample} ]] || {
		log_error "expected PID ${expected_pid} on every sample; actual ${unexpected_sample}"
		return 1
	}
}

native_container_memory_collect
assert_native_container_memory_profile "${TOOL_WORK_DIR}/result.folded" "${TOOL_WORK_DIR}/container-logs.err" \
	|| fatal "container memory CSS profile validation failed"
assert_container_memory_pid "${TOOL_WORK_DIR}/result.folded" "${native_memory_container_pid}" \
	|| fatal "container memory sample PID differs from Docker's workload PID"
log_info "container memory sample PID matches Docker's workload PID: ${native_memory_container_pid}"
