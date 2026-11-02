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

# Verify Python discovery and sampling when the executable path ends in the
# kernel's deleted marker, including an in-place upgrade and container lookup.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

readonly TOOL_BIN="${ROOT_DIR}/_output/bin/profiler"
readonly FIXTURE="${ROOT_DIR}/integration/testdata/test_profiler_python_cpu.py"
readonly PROFILER_DURATION=10
readonly DEFAULT_CONTAINER_IMAGE="${PYTHON_PROFILER_CONTAINER_IMAGE:-python:3.11-slim}"

require_commands python3
readonly PYSPY_BIN="${PROFILER_TOOL_DIR}/py-spy"
require_commands "${PYSPY_BIN}"

WORK_DIR=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/profiler-python-unlinked.XXXXXX")
readonly PYTHON_BIN="$(python3 -c 'import sys; print(sys.executable)')"
readonly PYTHON_HOME="$(python3 -c 'import sys; print(sys.base_prefix)')"
LITERAL_PID=""
REPLACED_PID=""
DOCKER_CONTAINER_ID=""

cleanup() {
	local status=$?
	[[ -n "${LITERAL_PID}" ]] && stop_by_pid "${LITERAL_PID}" 5 || true
	[[ -n "${REPLACED_PID}" ]] && stop_by_pid "${REPLACED_PID}" 5 || true
	if [[ -n "${DOCKER_CONTAINER_ID}" ]]; then
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

assert_profiler_output_file() {
	[[ -s "$1" && -f "$1" ]] || fatal "expected a nonempty profile file at $1"
}

assert_profiler_stack_contains() {
	local folded=$1
	local pid=$2
	local method=$3
	grep -qh "pid=\"${pid}\";.*${method}" "${folded}" \
		|| fatal "stack ${method} not found for PID ${pid} in ${folded}"
}

assert_folded_contains() {
	local folded=$1
	local method=$2
	grep -q "${method}" "${folded}" \
		|| fatal "stack ${method} not found in ${folded}"
}

py_spy_supports_target() {
	timeout 2 "${PYSPY_BIN}" dump --pid "$1" \
		> "${WORK_DIR}/py-spy-probe.out" 2> "${WORK_DIR}/py-spy-probe.err"
}

profile_explicit_pid() {
	local pid=$1
	local binary_path=$2
	local output=$3

	if ! "${TOOL_BIN}" \
		--type cpu \
		--language python \
		--pid "${pid}" \
		--binary-match-path "${binary_path}" \
		--tool-path "${PROFILER_TOOL_DIR}" \
		--duration "${PROFILER_DURATION}" \
		--aggr-interval "${PROFILER_DURATION}" \
		--freq 99 \
		--output-format collapsed \
		--output-path "${output}" \
		> "${output}.out" 2> "${output}.err"; then
		fatal "profiler failed for an unlinked Python executable (see ${output}.err)"
	fi
	assert_profiler_output_file "${output}"
	assert_profiler_stack_contains "${output}" "${pid}" independent_hot_method
}

# A literal filename may end in the same text as the kernel marker. The path
# must remain unchanged when it identifies the running inode.
readonly LITERAL_PYTHON="${WORK_DIR}/python (deleted)"
cp "${PYTHON_BIN}" "${LITERAL_PYTHON}"
chmod +x "${LITERAL_PYTHON}"
PYTHONHOME="${PYTHON_HOME}" "${LITERAL_PYTHON}" "${FIXTURE}" independent \
	> "${WORK_DIR}/literal.out" 2> "${WORK_DIR}/literal.err" &
LITERAL_PID=$!
wait_until 10 1 test -s "${WORK_DIR}/literal.out" || fatal "literal-suffix Python did not start"
kill -0 "${LITERAL_PID}" || fatal "literal-suffix Python exited immediately"
py_spy_supports_target "${LITERAL_PID}" \
	|| skip "py-spy does not support $(python3 --version 2>&1)"

log_info "profiling Python with a literal deleted suffix pid=${LITERAL_PID}"
profile_explicit_pid "${LITERAL_PID}" "${LITERAL_PYTHON}" "${WORK_DIR}/literal.folded"
log_info "Python with a literal deleted suffix is still discovered and profiled"

# An in-place upgrade unlinks the running image while the original path is
# replaced. The kernel marker must not prevent the original path from matching.
readonly REPLACED_PYTHON="${WORK_DIR}/python-replaced"
cp "${PYTHON_BIN}" "${REPLACED_PYTHON}"
chmod +x "${REPLACED_PYTHON}"
PYTHONHOME="${PYTHON_HOME}" "${REPLACED_PYTHON}" "${FIXTURE}" independent \
	> "${WORK_DIR}/replaced.out" 2> "${WORK_DIR}/replaced.err" &
REPLACED_PID=$!
wait_until 10 1 test -s "${WORK_DIR}/replaced.out" || fatal "replaced Python did not start"
kill -0 "${REPLACED_PID}" || fatal "replaced Python exited immediately"

rm -f "${REPLACED_PYTHON}"
wait_until 5 1 sh -c '[[ "$(readlink "/proc/$1/exe")" == *" (deleted)" ]]' sh "${REPLACED_PID}" \
	|| fatal "/proc/${REPLACED_PID}/exe is not marked deleted"
cp "${PYTHON_BIN}" "${REPLACED_PYTHON}"
chmod +x "${REPLACED_PYTHON}"

log_info "profiling Python after an in-place executable replacement pid=${REPLACED_PID}"
profile_explicit_pid "${REPLACED_PID}" "${REPLACED_PYTHON}" "${WORK_DIR}/replaced.folded"
log_info "Python with a replaced executable is still discovered and profiled"

test_docker_container() {
	local runtime_dir="${WORK_DIR}/container"
	local folded
	local container_id

	if ! command -v docker > /dev/null; then
		log_warn "skipping container discovery: docker is not installed"
		return
	fi
	if ! docker info > /dev/null 2>&1; then
		log_warn "skipping container discovery: docker daemon is unavailable"
		return
	fi
	if ! docker image inspect "${DEFAULT_CONTAINER_IMAGE}" > /dev/null 2>&1; then
		log_warn "skipping container discovery: image is unavailable: ${DEFAULT_CONTAINER_IMAGE}"
		return
	fi

	mkdir -p "${runtime_dir}"
	container_id=$(docker run --detach --rm \
		--volume "${FIXTURE}:/src/test_profiler_python_cpu.py:ro" \
		--volume "${runtime_dir}:/work" \
		"${DEFAULT_CONTAINER_IMAGE}" \
		sh -c '
			set -eu
			python_bin=$(python3 -c "import sys; print(sys.executable)")
			python_home=$(python3 -c "import sys; print(sys.base_prefix)")
			cp "$python_bin" /work/python
			chmod +x /work/python
			PYTHONHOME="$python_home" /work/python /src/test_profiler_python_cpu.py independent &
			pid=$!
			while [ ! -e "/proc/$pid/exe" ]; do sleep 0.1; done
			rm -f /work/python
			while true; do
				case "$(readlink "/proc/$pid/exe" 2>/dev/null || true)" in
					*" (deleted)") break ;;
					*) sleep 0.1 ;;
				esac
			done
			cp "$python_bin" /work/python
			chmod +x /work/python
			touch /work/python.ready
			wait "$pid"
		')
	DOCKER_CONTAINER_ID="${container_id}"
	wait_until 30 1 test -f "${runtime_dir}/python.ready" \
		|| fatal "Python replacement container did not become ready: ${container_id}"
	docker inspect --format '{{.State.Running}}' "${container_id}" | grep -qx true \
		|| fatal "Python replacement container exited before profiling: ${container_id}"

	log_info "profiling Python container after an in-place replacement id=${container_id}"
	if ! "${TOOL_BIN}" \
		--type cpu \
		--language python \
		--container-id "${container_id}" \
		--binary-match-path /work/python \
		--tool-path "${PROFILER_TOOL_DIR}" \
		--duration "${PROFILER_DURATION}" \
		--aggr-interval "${PROFILER_DURATION}" \
		--freq 99 \
		--output-format collapsed \
		--output-path "${runtime_dir}/container.folded" \
		> "${runtime_dir}/profiler.out" 2> "${runtime_dir}/profiler.err"; then
		fatal "container profiler failed for a replaced Python executable (see ${runtime_dir}/profiler.err)"
	fi
	assert_profiler_output_file "${runtime_dir}/container.folded"
	folded="${runtime_dir}/container.folded"
	assert_folded_contains "${folded}" independent_hot_method
	log_info "Python container with a replaced executable is discovered and profiled"
	docker rm -f "${container_id}" > /dev/null 2>&1 || true
	DOCKER_CONTAINER_ID=""
}

test_docker_container
