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

# A fresh CLI must resolve memory CSS from the container's kernel membership.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"

container_id=""
cleanup() {
	local status=$?
	trap - EXIT
	if [[ ${status} -eq 77 && ${NATIVE_PROFILER_REQUIRE_SPLIT_CGROUP_V1:-0} == 1 ]]; then
		log_error "required split-v1 memory regression was skipped; expected a passing test"
		status=1
	fi
	cleanup_piped_processes
	if [[ -n ${container_id} ]]; then
		timeout --kill-after=2 15 docker rm -f "${container_id}" > /dev/null 2>&1 || {
			log_error "cannot remove owned memory workload container: ${container_id}"
			status=1
		}
	fi
	exit "${status}"
}
trap cleanup EXIT

assert_container_cgroup_hierarchy() {
	local target_pid=$1 mode cpu_hierarchy="" memory_hierarchy=""
	if [[ -r /sys/fs/cgroup/cgroup.controllers ]]; then
		grep -qw memory /sys/fs/cgroup/cgroup.controllers || skip "cgroup v2 memory controller is unavailable"
		mode="unified cgroup v2"
	else
		cpu_hierarchy=$(awk -F: '$2 ~ /(^|,)cpu(,|$)/ {print $1}' "/proc/${target_pid}/cgroup")
		memory_hierarchy=$(awk -F: '$2 ~ /(^|,)memory(,|$)/ {print $1}' "/proc/${target_pid}/cgroup")
		[[ -n ${memory_hierarchy} ]] || skip "cgroup v1 memory controller is unavailable"
		mode="shared cgroup v1 hierarchy"
		if [[ -n ${cpu_hierarchy} && ${cpu_hierarchy} != "${memory_hierarchy}" ]]; then
			mode="split cgroup v1 CPU/memory hierarchies"
		fi
	fi
	if [[ ${NATIVE_PROFILER_REQUIRE_SPLIT_CGROUP_V1:-0} == 1 && ${mode} != "split cgroup v1 CPU/memory hierarchies" ]]; then
		fatal "expected split cgroup v1 CPU/memory hierarchies; actual ${mode} (CPU=${cpu_hierarchy}, memory=${memory_hierarchy})"
	fi
	log_info "container memory regression: ${mode}"
}

assert_local_memory_css_fallback() {
	local profiler_log=$1
	grep -qF 'falling back to local method' "${profiler_log}" \
		|| fatal "expected local memory CSS fallback; actual profiler output: $(tail -n 20 "${profiler_log}")"
}

assert_container_memory_profile() {
	local folded_file=$1 expected_pid=$2 fixture_stderr=$3
	local unexpected_sample expected_symbol=test_alloc_free_loop expected_bytes actual_bytes
	[[ -s ${folded_file} ]] || fatal "expected nonempty container memory profile; actual file missing or empty: ${folded_file}"
	unexpected_sample=$(awk -v pid_label="pid=\"${expected_pid}\"" \
		'index($0, pid_label) == 0 {print "line " NR ": " $0; exit}' "${folded_file}")
	[[ -z ${unexpected_sample} ]] || fatal "expected PID ${expected_pid} on every sample; actual ${unexpected_sample}"
	if kernel_version_le 5 4; then expected_symbol=do_mmap; fi
	expected_bytes=$(awk -F= '/^actual_allocated_bytes=/ {print $2}' "${fixture_stderr}")
	actual_bytes=$(awk -v symbol="${expected_symbol}" 'index($0, symbol) {sum += $NF} END {print sum+0}' "${folded_file}")
	[[ ${expected_bytes} =~ ^[1-9][0-9]*$ && ${actual_bytes} == "${expected_bytes}" ]] \
		|| fatal "container memory bytes for ${expected_symbol}: expected ${expected_bytes}, actual ${actual_bytes}"
	log_info "container memory CSS fallback captured ${actual_bytes} bytes for PID ${expected_pid}"
}

require_commands docker mkfifo setsid timeout awk
require_readable /sys/kernel/btf/vmlinux
timeout --kill-after=2 10 docker info > /dev/null 2>&1 || skip "Docker daemon is unavailable"
image=${NATIVE_PROFILER_DOCKER_IMAGE:-busybox:1.36.1}
timeout --kill-after=2 10 docker image inspect "${image}" > /dev/null 2>&1 \
	|| skip "native memory workload image is unavailable: ${image}"

bpf_tool_setup profiler native_virtual_alloc profiler-memory-container
compile_user_fixture "${ROOT_DIR}/integration/testdata/test_profiler_mmap.user.c" "${TOOL_WORK_DIR}/workload" -static
container_id=$(timeout --kill-after=2 15 docker run --detach \
	--volume "${TOOL_WORK_DIR}/workload:/work/workload:ro" \
	--entrypoint /work/workload "${image}" final) || fatal "cannot start native memory workload"
container_pid=$(timeout --kill-after=2 10 docker inspect --format '{{.State.Pid}}' "${container_id}")
[[ ${container_pid} =~ ^[1-9][0-9]*$ ]] || fatal "invalid container workload PID: ${container_pid}"
container_start_time=$(process_start_time "${container_pid}") || fatal "container workload exited"

assert_container_cgroup_hierarchy "${container_pid}"

start_piped_process "${TOOL_WORK_DIR}/container-logs" timeout --kill-after=2 120 docker logs --follow "${container_id}"
container_log_fd=${PROCESS_OUTPUT_FD}
exec {PROCESS_INPUT_FD}>&-
assert_process_message "${container_log_fd}" "${TOOL_WORK_DIR}/container.log" ready

# Port zero is rejected by the Node client, forcing local lookup without a server.
# This new process has no cached CSS; container ID is its only target filter.
start_piped_process "${TOOL_WORK_DIR}/profiler" "${TOOL_BIN}" \
	--type memory --language c --memory-mode virtual_alloc --container-id "${container_id}" \
	--huatuo-api-address 127.0.0.1:0 --duration 30 --aggr-interval 30 \
	--output-format collapsed --output-path "${TOOL_WORK_DIR}/result.folded" --verbose
profiler_pid=${PROCESS_PID}
profiler_output_fd=${PROCESS_OUTPUT_FD}
exec {PROCESS_INPUT_FD}>&-
assert_process_message "${profiler_output_fd}" "${TOOL_OUT}" 'data reading loop started'
assert_local_memory_css_fallback "${TOOL_OUT}"

timeout --kill-after=2 10 docker kill --signal=USR1 "${container_id}" > "${TOOL_WORK_DIR}/signal.log"
assert_process_message "${container_log_fd}" "${TOOL_WORK_DIR}/container.log" 'final allocation complete'
assert_process_shutdown_by_pid "${profiler_pid}" 35 INT || fatal "container memory profiler did not finalize"
assert_process_alive "${container_pid}" "${container_start_time}" "container workload after profiling"

assert_container_memory_profile "${TOOL_WORK_DIR}/result.folded" "${container_pid}" "${TOOL_WORK_DIR}/container-logs.err"
