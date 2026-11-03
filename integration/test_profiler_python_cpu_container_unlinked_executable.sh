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

# Verify container discovery profiles a Python process whose running image was
# unlinked and replaced at the same path.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

readonly PYTHON_TOOL_BIN="${ROOT_DIR}/_output/bin/profiler"
readonly PYTHON_FIXTURE="${ROOT_DIR}/integration/testdata/test_profiler_python_cpu.py"
readonly PYTHON_CONTAINER_IMAGE="${PYTHON_PROFILER_CONTAINER_IMAGE:-python:3.11-slim}"
readonly PYTHON_PROFILER_DURATION=10

require_commands timeout
readonly PYTHON_PYSPY_BIN="${PROFILER_TOOL_DIR}/py-spy"
require_commands "${PYTHON_PYSPY_BIN}"
command -v docker > /dev/null || skip "docker is not installed"

WORK_DIR=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/profiler-python-container-unlinked.XXXXXX")
readonly PYTHON_RUNTIME_DIR="${WORK_DIR}/container"
DOCKER_CONTAINER_ID=""

cleanup() {
	local status=$?
	if [[ -n "${DOCKER_CONTAINER_ID}" ]]; then
		if [[ ${status} -ne 0 && ${status} -ne 77 ]]; then
			docker logs "${DOCKER_CONTAINER_ID}" > "${PYTHON_RUNTIME_DIR}/container.log" 2>&1 || true
		fi
		docker rm -f "${DOCKER_CONTAINER_ID}" > /dev/null 2>&1 || true
	fi
	if [[ ${status} -eq 0 || ${status} -eq 77 ]]; then
		rm -rf -- "${WORK_DIR}"
	else
		log_error "integration test failed; artifacts preserved at ${WORK_DIR}"
	fi
	return "${status}"
}

trap cleanup EXIT

assert_profiler_folded_contains() {
	local folded=$1
	local method=$2
	grep -q "${method}" "${folded}" \
		|| fatal "stack ${method} not found in ${folded}"
}

docker info > /dev/null 2>&1 || skip "docker daemon is unavailable"
if ! docker image inspect "${PYTHON_CONTAINER_IMAGE}" > /dev/null 2>&1; then
	log_info "pulling container image: ${PYTHON_CONTAINER_IMAGE}"
	timeout 5m docker pull "${PYTHON_CONTAINER_IMAGE}" > "${WORK_DIR}/pull.log" 2>&1 \
		|| skip "container image is unavailable: ${PYTHON_CONTAINER_IMAGE}"
fi

mkdir -p "${PYTHON_RUNTIME_DIR}"

# The workload copies the interpreter, deletes it while it runs, and restores
# the path. Both wait loops fail with a message if Python exits early.
# Keep the container after exit so readiness checks and cleanup can save logs.
DOCKER_CONTAINER_ID=$(docker run --detach \
	--volume "${PYTHON_FIXTURE}:/src/test_profiler_python_cpu.py:ro" \
	--volume "${PYTHON_RUNTIME_DIR}:/work" \
	"${PYTHON_CONTAINER_IMAGE}" \
	sh -c '
		set -eu
		python_bin=$(python3 -c "import sys; print(sys.executable)")
		python_home=$(python3 -c "import sys; print(sys.base_prefix)")
		cp "$python_bin" /work/python
		chmod +x /work/python
		PYTHONHOME="$python_home" /work/python /src/test_profiler_python_cpu.py independent &
		pid=$!
		while [ ! -e "/proc/$pid/exe" ]; do
			kill -0 "$pid" 2> /dev/null || {
				echo "python exited before its executable appeared" >&2
				exit 1
			}
			sleep 0.1
		done
		rm -f /work/python
		while true; do
			case "$(readlink "/proc/$pid/exe" 2> /dev/null || true)" in
				*" (deleted)") break ;;
			esac
			kill -0 "$pid" 2> /dev/null || {
				echo "python exited before the image was replaced" >&2
				exit 1
			}
			sleep 0.1
		done
		cp "$python_bin" /work/python
		chmod +x /work/python
		touch /work/python.ready
		wait "$pid"
	')

container_ready() {
	[[ -f "${PYTHON_RUNTIME_DIR}/python.ready" ]] && return 0
	if [[ $(docker inspect --format '{{.State.Running}}' "${DOCKER_CONTAINER_ID}" 2> /dev/null || true) != true ]]; then
		docker logs "${DOCKER_CONTAINER_ID}" > "${PYTHON_RUNTIME_DIR}/container.log" 2>&1 || true
		fatal "container exited before the workload was ready (see ${PYTHON_RUNTIME_DIR}/container.log)"
	fi
	return 1
}

wait_until 30 1 container_ready || fatal "Python replacement container did not become ready: ${DOCKER_CONTAINER_ID}"

log_info "profiling Python container after an in-place replacement id=${DOCKER_CONTAINER_ID}"
if ! "${PYTHON_TOOL_BIN}" \
	--type cpu \
	--language python \
	--container-id "${DOCKER_CONTAINER_ID}" \
	--binary-match-path /work/python \
	--tool-path "${PROFILER_TOOL_DIR}" \
	--duration "${PYTHON_PROFILER_DURATION}" \
	--aggr-interval "${PYTHON_PROFILER_DURATION}" \
	--freq 99 \
	--output-format collapsed \
	--output-path "${PYTHON_RUNTIME_DIR}/container.folded" \
	> "${PYTHON_RUNTIME_DIR}/profiler.out" 2> "${PYTHON_RUNTIME_DIR}/profiler.err"; then
	docker logs "${DOCKER_CONTAINER_ID}" > "${PYTHON_RUNTIME_DIR}/container.log" 2>&1 || true
	fatal "container profiler failed for a replaced Python executable (see ${PYTHON_RUNTIME_DIR})"
fi

assert_profiler_output_file "${PYTHON_RUNTIME_DIR}/container.folded"
assert_profiler_folded_contains "${PYTHON_RUNTIME_DIR}/container.folded" independent_hot_method
log_info "Python container with a replaced executable is discovered and profiled"

docker rm -f "${DOCKER_CONTAINER_ID}" > /dev/null 2>&1 || true
DOCKER_CONTAINER_ID=""
