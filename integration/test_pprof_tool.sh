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

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

require_commands python3
require_readable "${PPROF_TOOL_BIN}"

readonly TOOL_WORK_DIR="$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/pprof-tool.XXXXXX")"
readonly SERVER_PORT_FILE="${TOOL_WORK_DIR}/pprof-server.port"
readonly SERVER_LOG="${TOOL_WORK_DIR}/pprof-server.log"
server_pid=""

cleanup() {
	if [[ -n "${server_pid}" ]]; then
		kill "${server_pid}" 2> /dev/null || true
		wait "${server_pid}" 2> /dev/null || true
	fi
}
trap cleanup EXIT

python3 "${ROOT_DIR}/integration/testdata/pprof_tool_server.py" \
	--port-file "${SERVER_PORT_FILE}" > "${SERVER_LOG}" 2>&1 &
server_pid=$!
wait_until 5 0.1 test -s "${SERVER_PORT_FILE}" \
	|| fatal "pprof test server did not publish a port"
readonly SERVER_URL="http://127.0.0.1:$(< "${SERVER_PORT_FILE}")/debug/pprof"

failed_dir="${TOOL_WORK_DIR}/retry-capture"
failed_out="${TOOL_WORK_DIR}/retry-capture.out"
failed_err="${TOOL_WORK_DIR}/retry-capture.err"
failed_status=0
"${PPROF_TOOL_BIN}" capture \
	--url "${SERVER_URL}" \
	--endpoint heap \
	--endpoint fail \
	--max-bytes 1024 \
	--output-dir "${failed_dir}" \
	> "${failed_out}" 2> "${failed_err}" || failed_status=$?
((failed_status != 0)) || fatal "failed capture unexpectedly succeeded"
[[ ! -e "${failed_dir}" ]] || fatal "failed capture directory was not removed"
grep -q "HTTP status 502" "${failed_err}" \
	|| fatal "failed capture did not report the HTTP status"

# The same path must be reusable after a later endpoint fails.
"${PPROF_TOOL_BIN}" capture \
	--url "${SERVER_URL}" \
	--endpoint heap \
	--output-dir "${failed_dir}" \
	> "${TOOL_WORK_DIR}/retry-success.out" \
	2> "${TOOL_WORK_DIR}/retry-success.err" \
	|| fatal "capture retry failed"
[[ -f "${failed_dir}/heap.pprof" ]] || fatal "retry did not publish heap.pprof"
[[ -f "${failed_dir}/capture.json" ]] || fatal "retry did not publish capture.json"
if compgen -G "${failed_dir}/*.partial" > /dev/null; then
	fatal "retry left a partial file"
fi

exact_dir="${TOOL_WORK_DIR}/exact-limit"
"${PPROF_TOOL_BIN}" capture \
	--url "${SERVER_URL}" \
	--endpoint exact \
	--max-bytes 4 \
	--output-dir "${exact_dir}" \
	> "${TOOL_WORK_DIR}/exact.out" 2> "${TOOL_WORK_DIR}/exact.err" \
	|| fatal "exact-limit capture failed"
exact_size=$(wc -c < "${exact_dir}/exact.pprof")
assert_eq "${exact_size}" 4 "exact-limit response size" \
	|| fatal "exact-limit response was not preserved"

over_dir="${TOOL_WORK_DIR}/over-limit"
over_err="${TOOL_WORK_DIR}/over-limit.err"
over_status=0
"${PPROF_TOOL_BIN}" capture \
	--url "${SERVER_URL}" \
	--endpoint over \
	--max-bytes 4 \
	--output-dir "${over_dir}" \
	> "${TOOL_WORK_DIR}/over-limit.out" 2> "${over_err}" || over_status=$?
((over_status != 0)) || fatal "over-limit capture unexpectedly succeeded"
[[ ! -e "${over_dir}" ]] || fatal "over-limit capture directory was not removed"
grep -q "response exceeds 4 bytes" "${over_err}" \
	|| fatal "over-limit capture did not report the size error"
