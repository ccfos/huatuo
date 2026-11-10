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

# Exercise perf's local container CSS reference lookup and BPF sampling.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"

require_commands docker jq timeout
require_readable /sys/kernel/btf/vmlinux
timeout --kill-after=2 10 docker info > /dev/null 2>&1 || skip "Docker daemon is unavailable"
readonly PERF_CONTAINER_IMAGE=${NATIVE_PROFILER_DOCKER_IMAGE:-busybox:1.36.1}
timeout --kill-after=2 10 docker image inspect "${PERF_CONTAINER_IMAGE}" > /dev/null 2>&1 \
	|| skip "perf workload image is unavailable: ${PERF_CONTAINER_IMAGE}"

bpf_tool_setup perf perf perf-container
container_id=""
cleanup() {
	local status=$?
	trap - EXIT
	if [[ -n ${container_id} ]]; then
		timeout --kill-after=2 15 docker rm -f "${container_id}" > /dev/null 2>&1 || {
			log_error "cannot remove owned perf workload container: ${container_id}"
			status=1
		}
	fi
	exit "${status}"
}
trap cleanup EXIT

assert_perf_container_samples() {
	local output=$1
	jq -e 'type == "array" and any(.[]; .label == "workload" and .value > 0)' "${output}" > /dev/null \
		|| fatal "expected perf samples from container workload; actual output: $(< "${output}")"
}

compile_user_fixture "${ROOT_DIR}/integration/testdata/test_profiler_callchain.user.c" "${TOOL_WORK_DIR}/workload" -static
container_id=$(timeout --kill-after=2 15 docker run --detach \
	--volume "${TOOL_WORK_DIR}/workload:/work/workload:ro" \
	--entrypoint /work/workload "${PERF_CONTAINER_IMAGE}") \
	|| fatal "cannot start perf workload container"

timeout --kill-after=2 20 "${TOOL_BIN}" \
	--bpf-path "${TOOL_BPF}" --container-id "${container_id}" \
	--duration 3 \
	> "${TOOL_OUT}" 2> "${TOOL_ERR}" \
	|| fatal "perf container sampling failed: $(< "${TOOL_ERR}")"
assert_perf_container_samples "${TOOL_OUT}"
