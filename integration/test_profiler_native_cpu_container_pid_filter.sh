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

# Verify PID/container filter intersection and target deletion on the host.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_profiler.sh"
source "${ROOT_DIR}/integration/lib_profiler_native_container.sh"

require_commands jq mkfifo setsid timeout awk findmnt
require_native_pid_profiling
require_native_container_profiling
bpf_tool_setup profiler native_oncpu_profiler profiler-native-container
readonly NATIVE_CONTAINER_DOCKER_IMAGE="${NATIVE_PROFILER_DOCKER_IMAGE:-${NATIVE_PROFILER_CONTAINER_IMAGE:-busybox:1.36.1}}"
readonly NATIVE_CONTAINER_CONTAINERD_IMAGE="${NATIVE_PROFILER_CONTAINERD_IMAGE:-${NATIVE_PROFILER_CONTAINER_IMAGE:-busybox:1.36.1}}"
readonly NATIVE_CONTAINER_WORK=${TOOL_WORK_DIR}
readonly NATIVE_CONTAINER_FIXTURE="${NATIVE_CONTAINER_WORK}/callchain"
native_container_profiler_pid=""
native_container_runtimes_tested=0

cleanup() {
	local status=$?
	trap - EXIT
	[[ -z $native_container_profiler_pid ]] || stop_by_pid "$native_container_profiler_pid" 5 || true
	cleanup_running_jobs || status=1
	native_container_stop || status=1
	exit "$status"
}
trap cleanup EXIT

compile_user_fixture "${ROOT_DIR}/integration/testdata/test_profiler_callchain.user.c" "$NATIVE_CONTAINER_FIXTURE" -static

exercise_pid_container_intersection() (
	local runtime=$1 container_id=$2 container_pid=$3 container_start_time=$4
	local outside_pid outside_start_time
	[[ ${container_pid} =~ ^[1-9][0-9]*$ ]] || fatal "invalid ${runtime} container PID: ${container_pid}"
	trap cleanup_running_jobs EXIT
	log_info "checking native PID/container intersection: ${runtime}"
	start_piped_process "${NATIVE_CONTAINER_WORK}/${runtime}/outside" "${NATIVE_CONTAINER_FIXTURE}"
	outside_pid=${PROCESS_PID}
	outside_start_time=${PROCESS_START_TIME}
	exec {PROCESS_INPUT_FD}>&-
	exec {PROCESS_OUTPUT_FD}<&-
	# Prove the outside workload is sampled before checking that CSS excludes it.
	start_cpu_profiler "${NATIVE_CONTAINER_WORK}/${runtime}/outside-control" c 30 1 --pid "${outside_pid}"
	assert_profiler_samples_before_deadline "${outside_pid}" f3
	kill -INT "${PROFILER_PID}"
	assert_profiler_exit 0
	start_cpu_profiler "${NATIVE_CONTAINER_WORK}/${runtime}/outside-excluded" c 2 2 \
		--pid "${outside_pid}" --container-id "${container_id}"
	assert_process_message "${PROFILER_OUTPUT_FD}" "${PROFILER_OUT}" 'data reading loop started'
	assert_profiler_exit 0
	assert_process_alive "${outside_pid}" "${outside_start_time}" "outside workload during filter check" || fatal "workload process identity validation failed"
	assert_process_alive "${container_pid}" "${container_start_time}" "container workload during filter check" || fatal "workload process identity validation failed"
	[[ ! -s ${PROFILER_STACKS} ]] || fatal "combined PID/container filters accepted a nonmember PID"
)

exercise_pid_target_deletion() (
	local runtime=$1 container_id=$2 container_pid=$3
	trap cleanup_running_jobs EXIT
	log_info "destroying a profiled native container: ${runtime}"
	start_cpu_profiler "${NATIVE_CONTAINER_WORK}/${runtime}/destroyed" c 30 1 \
		--container-id "${container_id}" --pid "${container_pid}"
	assert_profiler_samples_before_deadline "${container_pid}" f3
	native_container_stop || fatal "cannot remove PID-filtered profiling target"
	kill -INT "${PROFILER_PID}"
	assert_profiler_exit 0
)
for native_container_runtime in docker containerd; do
	case $native_container_runtime in
	docker) native_container_image=$NATIVE_CONTAINER_DOCKER_IMAGE ;;
	containerd)
		[[ ${NATIVE_PROFILER_TEST_CONTAINERD:-1} == 1 ]] || continue
		native_container_image=$NATIVE_CONTAINER_CONTAINERD_IMAGE
		;;
	esac
	native_container_runtime_available "$native_container_runtime" "$native_container_image" || continue
	native_container_start "$native_container_runtime" "$native_container_image" "$NATIVE_CONTAINER_FIXTURE"
	if [[ $native_container_runtime == docker ]]; then
		native_container_pid=$(docker inspect --format '{{.State.Pid}}' "$native_container_id")
	else
		native_container_pid=$(ctr --namespace "$NATIVE_CONTAINER_NAMESPACE" tasks list \
			| awk -v id="$native_container_id" '$1 == id && $3 == "RUNNING" { print $2; found = 1 } END { exit !found }') \
			|| fatal "containerd profiling target is not running: $native_container_id"
	fi
	[[ $native_container_pid =~ ^[1-9][0-9]*$ ]] || fatal "invalid profiling target PID: $native_container_pid"
	native_container_start_time=$(process_start_time "$native_container_pid") || fatal "profiling target exited"
	exercise_pid_container_intersection "$native_container_runtime" "$native_container_id" "$native_container_pid" "$native_container_start_time"
	exercise_pid_target_deletion "$native_container_runtime" "$native_container_id" "$native_container_pid"
	# The deletion scenario removed this target in its cleanup subshell.
	native_container_id=""
	native_container_runtimes_tested=$((native_container_runtimes_tested + 1))
done
((native_container_runtimes_tested > 0)) || skip "no docker or containerd runtime was testable"
log_info "native PID/container filtering passed for $native_container_runtimes_tested runtime(s)"
