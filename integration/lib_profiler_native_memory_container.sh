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

# Callers own assertions and EXIT cleanup of jobs and the workload container.
set -euo pipefail
source "${ROOT_DIR}/integration/lib_profiler_native_container.sh"

native_memory_container_pid=""

require_native_memory_cgroup_hierarchy() {
	local target_pid=$1 mode cpu_hierarchy="" memory_hierarchy=""
	if [[ ${HUATUO_TEST_CGROUP_MODE} == unified ]]; then
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

assert_native_container_memory_profile() {
	local folded_file=$1 fixture_stderr=$2
	local expected_symbol=test_alloc_free_loop expected_bytes actual_bytes
	[[ -s ${folded_file} ]] || {
		log_error "expected nonempty container memory profile: ${folded_file}"
		return 1
	}
	if kernel_version_le 5 4; then expected_symbol=do_mmap; fi
	expected_bytes=$(awk -F= '/^actual_allocated_bytes=/ {print $2}' "${fixture_stderr}") || return 1
	actual_bytes=$(awk -v symbol="${expected_symbol}" 'index($0, symbol) {sum += $NF} END {print sum+0}' "${folded_file}") || return 1
	[[ ${expected_bytes} =~ ^[1-9][0-9]*$ && ${actual_bytes} == "${expected_bytes}" ]] || {
		log_error "container memory bytes for ${expected_symbol}: expected ${expected_bytes}, actual ${actual_bytes}"
		return 1
	}
}

native_container_memory_collect() {
	local image container_start_time container_log_fd profiler_pid profiler_output_fd
	require_commands docker mkfifo setsid timeout awk
	require_readable /sys/kernel/btf/vmlinux
	timeout --kill-after=2 10 docker info > /dev/null 2>&1 || skip "Docker daemon is unavailable"
	image=${NATIVE_PROFILER_DOCKER_IMAGE:-busybox:1.36.1}
	timeout --kill-after=2 10 docker image inspect "${image}" > /dev/null 2>&1 \
		|| skip "native memory workload image is unavailable: ${image}"

	bpf_tool_setup profiler native_virtual_alloc profiler-memory-container
	compile_user_fixture "${ROOT_DIR}/integration/testdata/test_profiler_mmap.user.c" "${TOOL_WORK_DIR}/workload" -static
	native_container_start docker "${image}" "${TOOL_WORK_DIR}/workload" workload final
	native_memory_container_pid=$(timeout --kill-after=2 10 docker inspect --format '{{.State.Pid}}' "${native_container_id}")
	[[ ${native_memory_container_pid} =~ ^[1-9][0-9]*$ ]] || fatal "invalid container workload PID: ${native_memory_container_pid}"
	container_start_time=$(process_start_time "${native_memory_container_pid}") || fatal "container workload exited"

	require_native_memory_cgroup_hierarchy "${native_memory_container_pid}"

	start_piped_process "${TOOL_WORK_DIR}/container-logs" timeout --kill-after=2 120 docker logs --follow "${native_container_id}"
	container_log_fd=${PROCESS_OUTPUT_FD}
	exec {PROCESS_INPUT_FD}>&-
	assert_process_message "${container_log_fd}" "${TOOL_WORK_DIR}/container.log" ready

	# This new process resolves the container CSS locally; container ID is its only target filter.
	start_piped_process "${TOOL_WORK_DIR}/profiler" "${TOOL_BIN}" \
		--type memory --language c --memory-mode virtual_alloc --container-id "${native_container_id}" \
		--duration 30 --aggr-interval 30 \
		--output-format collapsed --output-path "${TOOL_WORK_DIR}/result.folded" --verbose
	profiler_pid=${PROCESS_PID}
	profiler_output_fd=${PROCESS_OUTPUT_FD}
	exec {PROCESS_INPUT_FD}>&-
	assert_process_message "${profiler_output_fd}" "${TOOL_OUT}" 'data reading loop started'

	timeout --kill-after=2 10 docker kill --signal=USR1 "${native_container_id}" > "${TOOL_WORK_DIR}/signal.log"
	assert_process_message "${container_log_fd}" "${TOOL_WORK_DIR}/container.log" 'final allocation complete'
	assert_process_shutdown_by_pid "${profiler_pid}" 35 INT || fatal "container memory profiler did not finalize"
	assert_process_alive "${native_memory_container_pid}" "${container_start_time}" "container workload after profiling" || fatal "workload process identity validation failed"
}
