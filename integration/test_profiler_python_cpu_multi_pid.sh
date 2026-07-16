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

# Verify explicit Python targets launch py-spy only for unrelated roots and
# retain parent, child, and independent workloads under distinct process roots.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

readonly TOOL_BIN="${ROOT_DIR}/_output/bin/profiler"
readonly FIXTURE="${ROOT_DIR}/integration/testdata/test_profiler_python_cpu.py"
readonly PROFILER_DURATION=10

command -v python3 > /dev/null || skip "python3 is not installed"
readonly PYSPY_BIN="${PYTHON_PROFILER_TOOL_PATH}/py-spy"
[[ -x "${PYSPY_BIN}" ]] || skip "py-spy missing: ${PYSPY_BIN}"
[[ -x "${TOOL_BIN}" ]] || fatal "profiler binary missing: ${TOOL_BIN}"

WORK_DIR=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/profiler-python-multi.XXXXXX")
PROFILER_PROFILER_CHILD_PID_FILE="${WORK_DIR}/child.pid"
PROFILER_PARENT_PID=""
PROFILER_CHILD_PID=""
PROFILER_INDEPENDENT_PID=""

cleanup() {
	[[ -n "${PROFILER_PARENT_PID}" ]] && stop_by_pid "${PROFILER_PARENT_PID}" 5 || true
	[[ -n "${PROFILER_CHILD_PID}" ]] && stop_by_pid "${PROFILER_CHILD_PID}" 5 || true
	[[ -n "${PROFILER_INDEPENDENT_PID}" ]] && stop_by_pid "${PROFILER_INDEPENDENT_PID}" 5 || true
}
trap cleanup EXIT

python3 "${FIXTURE}" parent "${PROFILER_PROFILER_CHILD_PID_FILE}" \
	> "${WORK_DIR}/parent.out" 2> "${WORK_DIR}/parent.err" &
PROFILER_PARENT_PID=$!

wait_until 10 1 test -s "${PROFILER_PROFILER_CHILD_PID_FILE}" || fatal "Python child PID was not published"
PROFILER_CHILD_PID=$(< "${PROFILER_PROFILER_CHILD_PID_FILE}")

python3 "${FIXTURE}" independent \
	> "${WORK_DIR}/independent.out" 2> "${WORK_DIR}/independent.err" &
PROFILER_INDEPENDENT_PID=$!

kill -0 "${PROFILER_PARENT_PID}" || fatal "Python parent exited immediately"
kill -0 "${PROFILER_CHILD_PID}" || fatal "Python child exited immediately"
kill -0 "${PROFILER_INDEPENDENT_PID}" || fatal "independent Python process exited immediately"

log_info "profiling Python pids=${PROFILER_PARENT_PID},${PROFILER_CHILD_PID},${PROFILER_INDEPENDENT_PID}"
if ! "${TOOL_BIN}" \
	--type cpu \
	--language python \
	--pid "${PROFILER_PARENT_PID},${PROFILER_CHILD_PID},${PROFILER_INDEPENDENT_PID}" \
	--tool-path "${PYTHON_PROFILER_TOOL_PATH}" \
	--max-profiler-processes 2 \
	--duration "${PROFILER_DURATION}" \
	--aggr-interval "${PROFILER_DURATION}" \
	--freq 99 \
	--output-format collapsed \
	--output-path "${WORK_DIR}" \
	> "${WORK_DIR}/profiler.out" 2> "${WORK_DIR}/profiler.err"; then
	fatal "profiler exited non-zero (see ${WORK_DIR}/profiler.err)"
fi

mapfile -t FOLDED_FILES < <(find "${WORK_DIR}" -maxdepth 1 -name 'perf_*.folded' -type f)
[[ ${#FOLDED_FILES[@]} -gt 0 ]] || fatal "no perf_*.folded file produced"

grep -qh "process ${PROFILER_PARENT_PID}.*parent_hot_method" "${FOLDED_FILES[@]}" \
	|| fatal "parent workload stack not found for PID ${PROFILER_PARENT_PID}"
grep -qh "process ${PROFILER_CHILD_PID}.*child_hot_method" "${FOLDED_FILES[@]}" \
	|| fatal "child workload stack not found for PID ${PROFILER_CHILD_PID}"
grep -qh "process ${PROFILER_INDEPENDENT_PID}.*independent_hot_method" "${FOLDED_FILES[@]}" \
	|| fatal "independent workload stack not found for PID ${PROFILER_INDEPENDENT_PID}"

log_info "Python parent, child, and independent stacks are correctly attributed"
