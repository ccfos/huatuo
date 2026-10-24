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

# Exercise the built command: a peer stops reading, UDP drops fill its Unix
# socket, and SIGTERM must stop dropwatch without the peer resuming reads.
set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

require_commands python3 ss ip unshare
if [[ ${1:-} != --isolated ]]; then
	exec unshare --net bash "$0" --isolated
fi
ip link set lo up

bpf_tool_setup dropwatch net_dropwatch dw-output
readonly DROPWATCH_OUTPUT_SOCKET="${TOOL_WORK_DIR}/events.sock"
readonly DROPWATCH_OUTPUT_CONNECTED="${TOOL_WORK_DIR}/connected"
readonly DROPWATCH_OUTPUT_QUEUES="${TOOL_WORK_DIR}/socket-queues.txt"
readonly DROPWATCH_OUTPUT_TARGET_IP="127.0.0.99"
DROPWATCH_OUTPUT_TARGET_PORT=$(allocate_available_port udp) \
	|| fatal "cannot allocate the dropwatch traffic port"
readonly DROPWATCH_OUTPUT_TARGET_PORT
readonly DROPWATCH_OUTPUT_MIN_BACKLOG=131072
dropwatch_output_pid=""
dropwatch_output_peer_pid=""
dropwatch_output_traffic_pid=""

cleanup() {
	stop_and_wait_by_pid "${dropwatch_output_traffic_pid}" 1 || true
	stop_and_wait_by_pid "${dropwatch_output_pid}" 7 || true
	stop_and_wait_by_pid "${dropwatch_output_peer_pid}" 1 || true
}
trap cleanup EXIT

dropwatch_output_is_backpressured() {
	ss -x -n -p > "${DROPWATCH_OUTPUT_QUEUES}"
	awk -v pid="pid=${dropwatch_output_pid}," -v minimum="${DROPWATCH_OUTPUT_MIN_BACKLOG}" '
		index($0, pid) && $4 >= minimum { found = 1 }
		END { exit !found }
	' "${DROPWATCH_OUTPUT_QUEUES}"
}

assert_dropwatch_output_cancellation() {
	stop_gracefully_by_pid "${dropwatch_output_pid}" 7 \
		|| fatal "dropwatch exceeded its 5-second output grace and process cleanup allowance"
	dropwatch_output_pid=""
	assert_log_has_no_failure "${TOOL_ERR}" "dropwatch output cancellation"
}

python3 "${ROOT_DIR}/integration/testdata/tools/test_toolstream_peer.py" \
	--socket "${DROPWATCH_OUTPUT_SOCKET}" \
	--connected "${DROPWATCH_OUTPUT_CONNECTED}" \
	> "${TOOL_WORK_DIR}/peer.out" 2> "${TOOL_WORK_DIR}/peer.err" &
dropwatch_output_peer_pid=$!
wait_until 5 0.1 test -S "${DROPWATCH_OUTPUT_SOCKET}" \
	|| fatal "toolstream peer did not create its socket"

"${TOOL_BIN}" --bpf-path "${TOOL_BPF}" \
	--filter "udp and dst host ${DROPWATCH_OUTPUT_TARGET_IP} and dst port ${DROPWATCH_OUTPUT_TARGET_PORT}" \
	--max-events-per-second 0 --duration 60 \
	--output-storage "${DROPWATCH_OUTPUT_SOCKET}" \
	> "${TOOL_OUT}" 2> "${TOOL_ERR}" &
dropwatch_output_pid=$!
wait_until 10 0.1 test -f "${DROPWATCH_OUTPUT_CONNECTED}" \
	|| fatal "dropwatch did not connect to the toolstream peer"

(
	while kill -0 "${dropwatch_output_pid}" 2> /dev/null; do
		printf x > "/dev/udp/${DROPWATCH_OUTPUT_TARGET_IP}/${DROPWATCH_OUTPUT_TARGET_PORT}" 2> /dev/null || true
	done
) &
dropwatch_output_traffic_pid=$!

wait_until 15 0.1 dropwatch_output_is_backpressured \
	|| fatal "dropwatch socket did not reach ${DROPWATCH_OUTPUT_MIN_BACKLOG} queued bytes: $(< "${DROPWATCH_OUTPUT_QUEUES}")"
log_info "dropwatch socket backlog before SIGTERM: $(< "${DROPWATCH_OUTPUT_QUEUES}")"
assert_dropwatch_output_cancellation
