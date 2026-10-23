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
require_commands python3 jq

assert_process_shutdown() (
	local mode=$1 expected_status=$2 signal=${3:-TERM} pid="" status=0
	local ready="${HUATUO_BAMAI_TEST_TMPDIR}/${mode}.ready"
	trap 'stop_and_wait_by_pid "${pid}" 1 || true' EXIT
	python3 - "${ready}" "${mode}" << 'PY' &
import pathlib
import signal
import sys
import time

signal.signal(signal.SIGTERM, signal.SIG_IGN if sys.argv[2] == "forced" else lambda *_: sys.exit(7))
signal.signal(signal.SIGUSR2, lambda *_: sys.exit(0))
pathlib.Path(sys.argv[1]).touch()
while True:
    time.sleep(0.01)
PY
	pid=$!
	wait_until 5 0.05 test -f "${ready}" || fatal "${mode}: child did not become ready"
	stop_and_wait_by_pid "${pid}" 1 "${signal}" || status=$?
	assert_eq "${status}" "${expected_status}" "${mode}: child exit status"
	! kill -0 "${pid}" 2> /dev/null || fatal "${mode}: child survived shutdown"
	pid=""
)

assert_streaming_result() {
	local actual_status=$1 expected_error=$2 log_file=$3
	if [[ -z ${expected_error} ]]; then
		assert_eq "${actual_status}" 0 "streaming failed; see ${log_file}"
	else
		[[ ${actual_status} != 0 ]] || fatal "invalid streaming session passed; see ${log_file}"
		grep -Fq "${expected_error}" "${log_file}" \
			|| fatal "expected streaming failure missing: ${expected_error}; see ${log_file}"
	fi
}

stop_and_wait_by_pid ""
assert_process_shutdown graceful_failure 7
assert_process_shutdown forced 137
assert_process_shutdown custom_signal 0 USR2

cat > "${HUATUO_BAMAI_TEST_TMPDIR}/profiler.py" << 'PY'
import json
import signal
import sys
import time

def finish(*_):
    print(json.dumps({"final": True, "profile": {"sample": []}}), flush=True)
    sys.exit(int(sys.argv[2]))

signal.signal(signal.SIGINT, finish)
for _ in range(int(sys.argv[1])):
    print(json.dumps({"profile": {"sample": [1]}}), flush=True)
    time.sleep(0.05)
while True:
    time.sleep(0.01)
PY

for helper_case in complete shutdown_failure missing_window; do
	helper_windows=2 helper_exit=0 helper_error=""
	case ${helper_case} in
	shutdown_failure) helper_exit=7 helper_error='exited 7' ;;
	missing_window) helper_windows=1 helper_error='two Java windows not published' ;;
	esac
	helper_status=0
	bash -euc '
source "$ROOT_DIR/integration/lib.sh"
# Bound the missing-window fixture without changing its completion predicate.
wait_until() {
 shift 2
 local i
 for ((i=0; i<20; i++)); do
  if "$@"; then return 0; fi
  sleep 0.05
 done
 return 1
}
assert_java_profiler_streams "$1" python3 "$2" "$3" "$4"
jq -se "last.final == true" "$1/windows.json"
' bash "${HUATUO_BAMAI_TEST_TMPDIR}/${helper_case}" \
		"${HUATUO_BAMAI_TEST_TMPDIR}/profiler.py" "${helper_windows}" "${helper_exit}" \
		> "${HUATUO_BAMAI_TEST_TMPDIR}/${helper_case}.log" 2>&1 || helper_status=$?
	assert_streaming_result "${helper_status}" "${helper_error}" "${HUATUO_BAMAI_TEST_TMPDIR}/${helper_case}.log"
done

log_info "process shutdown and Java streaming completion checks passed"
