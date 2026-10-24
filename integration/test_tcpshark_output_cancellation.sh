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

# A healthy production-protocol receiver must retain pending retransmissions
# and End when the built tcpshark command is stopped during continuous traffic.
set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

require_commands python3 jq tc ip unshare go capnp ss
if [[ ${1:-} != --isolated ]]; then
	exec unshare --net bash "$0" --isolated
fi
ip link set lo up

bpf_tool_setup tcpshark tcp_retransmit tcp-output
readonly TCP_OUTPUT_SOCKET="${TOOL_WORK_DIR}/events.sock"
readonly TCP_OUTPUT_FRAMES="${TOOL_WORK_DIR}/frames.jsonl"
readonly TCP_OUTPUT_CONNECTED="${TOOL_WORK_DIR}/connected"
readonly TCP_OUTPUT_ADDRESS="127.0.0.99"
TCP_OUTPUT_PORT=$(allocate_available_port) || fatal "cannot allocate the retransmit traffic port"
readonly TCP_OUTPUT_PORT
tcp_output_pid=""
tcp_output_peer_pid=""
tcp_output_traffic_pid=""

cleanup() {
	stop_and_wait_by_pid "${tcp_output_traffic_pid}" 1 || true
	stop_and_wait_by_pid "${tcp_output_pid}" 7 || true
	stop_and_wait_by_pid "${tcp_output_peer_pid}" 7 || true
}
trap cleanup EXIT

tcp_output_events_ready() {
	jq -s -e --arg address "${TCP_OUTPUT_ADDRESS}" --argjson port "${TCP_OUTPUT_PORT}" '
		[.[] | select(.event.tcp_daddr == $address and .event.tcp_dport == $port)] | length >= 10
	' "${TCP_OUTPUT_FRAMES}" > /dev/null 2>&1
}

tcp_output_peer_exited() {
	! kill -0 "${tcp_output_peer_pid}" 2> /dev/null
}

assert_tcp_output_finalization() {
	jq -s -e --arg address "${TCP_OUTPUT_ADDRESS}" --argjson port "${TCP_OUTPUT_PORT}" '
		.[0].connect.serviceName == "tcpshark"
		and ([.[] | select(.end == true)] | length) == 1 and .[-1].end == true
		and ([.[] | select(.event.tcp_daddr == $address and .event.tcp_dport == $port)
			| .event | select(.correlation_reason == "interrupted")] | length) > 0
	' "${TCP_OUTPUT_FRAMES}" > /dev/null \
		|| fatal "tcpshark lost queued interrupted events or the final End frame"
	assert_log_has_no_failure "${TOOL_ERR}" "tcpshark output finalization"
	log_info "tcpshark final frames: $(jq -s -c '{events: map(select(.event != null)) | length, interrupted: map(select(.event.correlation_reason == "interrupted")) | length, end: map(select(.end == true)) | length}' "${TCP_OUTPUT_FRAMES}")"
}

assert_tcp_output_cancellation() {
	stop_gracefully_by_pid "${tcp_output_pid}" 7 || fatal "tcpshark did not finish its bounded output grace"
	tcp_output_pid=""
	wait_until 5 0.1 tcp_output_peer_exited || fatal "toolstream receiver did not finish after command exit"
	wait "${tcp_output_peer_pid}" || fatal "toolstream receiver did not receive End cleanly"
	tcp_output_peer_pid=""
}

# Reuse the schema/import location used by the production protocol generator.
TCP_OUTPUT_SCHEMA_IMPORT=$(GOFLAGS=-mod=mod go list -m -f '{{.Dir}}' capnproto.org/go/capnp/v3)
readonly TCP_OUTPUT_SCHEMA_IMPORT
python3 "${ROOT_DIR}/integration/testdata/tools/test_toolstream_peer.py" \
	--socket "${TCP_OUTPUT_SOCKET}" --connected "${TCP_OUTPUT_CONNECTED}" \
	--output "${TCP_OUTPUT_FRAMES}" \
	--schema "${ROOT_DIR}/internal/toolstream/transport/event.capnp" \
	--schema-import "${TCP_OUTPUT_SCHEMA_IMPORT}/std" \
	> "${TOOL_WORK_DIR}/peer.out" 2> "${TOOL_WORK_DIR}/peer.err" &
tcp_output_peer_pid=$!
wait_until 5 0.1 test -S "${TCP_OUTPUT_SOCKET}" || fatal "toolstream receiver did not start"

# Excluding loopback from dropwatch leaves retransmissions pending; the isolated
# namespace and destination select only this workload's observations.
tc qdisc add dev lo root netem loss 100%
"${TOOL_BIN}" --mode retransmit --with-dropwatch --device-excluded lo \
	--bpf-path-dir "${ROOT_DIR}/_output/bpf" --duration 60 \
	--max-events-per-second 0 --output-storage "${TCP_OUTPUT_SOCKET}" \
	> "${TOOL_OUT}" 2> "${TOOL_ERR}" &
tcp_output_pid=$!

python3 - "${TCP_OUTPUT_ADDRESS}" "${TCP_OUTPUT_PORT}" << 'PY' &
import socket
import sys
import time

connections = []
for _ in range(512):
    connection = socket.socket()
    connection.setblocking(False)
    connection.connect_ex((sys.argv[1], int(sys.argv[2])))
    connections.append(connection)
    # Stagger retry timers so cancellation always overlaps pending events.
    time.sleep(0.01)
while True:
    time.sleep(1)
PY
tcp_output_traffic_pid=$!

wait_until 25 0.1 tcp_output_events_ready || fatal "tcpshark did not observe retransmit traffic"
assert_tcp_output_cancellation
assert_tcp_output_finalization
