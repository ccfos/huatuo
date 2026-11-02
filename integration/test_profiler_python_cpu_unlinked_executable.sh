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
# kernel's deleted marker, including a literal suffix and an in-place upgrade.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

readonly PYTHON_TOOL_BIN="${ROOT_DIR}/_output/bin/profiler"
readonly PYTHON_FIXTURE="${ROOT_DIR}/integration/testdata/test_profiler_python_cpu.py"
readonly PYTHON_PROFILER_DURATION=10

require_commands python3
readonly PYTHON_PYSPY_BIN="${PROFILER_TOOL_DIR}/py-spy"
require_commands "${PYTHON_PYSPY_BIN}"

WORK_DIR=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/profiler-python-unlinked.XXXXXX")
readonly PYTHON_BIN="$(python3 -c 'import sys; print(sys.executable)')"
readonly PYTHON_HOME="$(python3 -c 'import sys; print(sys.base_prefix)')"
LITERAL_PID=""
REPLACED_PID=""

cleanup() {
	local status=$?
	[[ -n "${LITERAL_PID}" ]] && stop_by_pid "${LITERAL_PID}" 5 || true
	[[ -n "${REPLACED_PID}" ]] && stop_by_pid "${REPLACED_PID}" 5 || true
	if [[ ${status} -eq 0 || ${status} -eq 77 ]]; then
		rm -rf -- "${WORK_DIR}"
	else
		log_error "integration test failed; artifacts preserved at ${WORK_DIR}"
	fi
	return "${status}"
}

trap cleanup EXIT

py_spy_supports_target() {
	timeout 2 "${PYTHON_PYSPY_BIN}" dump --pid "$1" \
		> "${WORK_DIR}/py-spy-probe.out" 2> "${WORK_DIR}/py-spy-probe.err"
}

profile_explicit_pid() {
	local pid=$1
	local binary_path=$2
	local output=$3

	if ! "${PYTHON_TOOL_BIN}" \
		--type cpu \
		--language python \
		--pid "${pid}" \
		--binary-match-path "${binary_path}" \
		--tool-path "${PROFILER_TOOL_DIR}" \
		--duration "${PYTHON_PROFILER_DURATION}" \
		--aggr-interval "${PYTHON_PROFILER_DURATION}" \
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
readonly PYTHON_LITERAL="${WORK_DIR}/python (deleted)"
cp "${PYTHON_BIN}" "${PYTHON_LITERAL}"
chmod +x "${PYTHON_LITERAL}"
PYTHONHOME="${PYTHON_HOME}" "${PYTHON_LITERAL}" "${PYTHON_FIXTURE}" independent \
	> "${WORK_DIR}/literal.out" 2> "${WORK_DIR}/literal.err" &
LITERAL_PID=$!
wait_until 10 1 test -s "${WORK_DIR}/literal.out" || fatal "literal-suffix Python did not start"
kill -0 "${LITERAL_PID}" || fatal "literal-suffix Python exited immediately"
py_spy_supports_target "${LITERAL_PID}" \
	|| skip "py-spy does not support $(python3 --version 2>&1)"

log_info "profiling Python with a literal deleted suffix pid=${LITERAL_PID}"
profile_explicit_pid "${LITERAL_PID}" "${PYTHON_LITERAL}" "${WORK_DIR}/literal.folded"
log_info "Python with a literal deleted suffix is still discovered and profiled"

# An in-place upgrade unlinks the running image while the original path is
# replaced. The kernel marker must not prevent the original path from matching.
readonly PYTHON_REPLACED="${WORK_DIR}/python-replaced"
cp "${PYTHON_BIN}" "${PYTHON_REPLACED}"
chmod +x "${PYTHON_REPLACED}"
PYTHONHOME="${PYTHON_HOME}" "${PYTHON_REPLACED}" "${PYTHON_FIXTURE}" independent \
	> "${WORK_DIR}/replaced.out" 2> "${WORK_DIR}/replaced.err" &
REPLACED_PID=$!
wait_until 10 1 test -s "${WORK_DIR}/replaced.out" || fatal "replaced Python did not start"
kill -0 "${REPLACED_PID}" || fatal "replaced Python exited immediately"

rm -f "${PYTHON_REPLACED}"
wait_until 5 1 bash -c '[[ "$(readlink "/proc/$1/exe")" == *" (deleted)" ]]' bash "${REPLACED_PID}" \
	|| fatal "/proc/${REPLACED_PID}/exe is not marked deleted"
cp "${PYTHON_BIN}" "${PYTHON_REPLACED}"
chmod +x "${PYTHON_REPLACED}"

log_info "profiling Python after an in-place executable replacement pid=${REPLACED_PID}"
profile_explicit_pid "${REPLACED_PID}" "${PYTHON_REPLACED}" "${WORK_DIR}/replaced.folded"
log_info "Python with a replaced executable is still discovered and profiled"
