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

source "$(dirname "$0")/env.sh"
source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_namespace.sh"

bpf_tool_setup tcpshark tcp_retransmit tcp-retransmit-synack
TCPSHARK_BIN=${TOOL_BIN}
BPF_OBJ=${TOOL_BPF}
OUTPUT_DIR=${TOOL_WORK_DIR}
TEST_PORT=19994
S_ADDR="10.99.3.1"
C_ADDR="10.99.3.2"

require_python3

cleanup() {
	[[ -n "${TCPSHARK_PID:-}" ]] && kill "${TCPSHARK_PID}" 2> /dev/null || true
	[[ -n "${SRV_PID:-}" ]] && kill "${SRV_PID}" 2> /dev/null || true
	sleep 0.2
	[[ -n "${TCPSHARK_PID:-}" ]] && kill -9 "${TCPSHARK_PID}" 2> /dev/null || true
	tcp_namespace_cleanup
	rm -rf "${OUTPUT_DIR}"
}
trap cleanup EXIT

log_info "SYNACK retrans: drop client's final ACK in an isolated namespace"

tcp_namespace_setup synack "${S_ADDR}" "${C_ADDR}"

# 1. Hold a listening socket while the kernel handles the 3-way handshake.
ip netns exec "${TCP_NS_SERVER}" timeout 8 python3 "${ROOT_DIR}/integration/testdata/tcp_server.py" \
	--listen-address "${TCP_NS_SERVER_ADDR}" --port "${TEST_PORT}" > /dev/null 2>&1 &
SRV_PID=$!
sleep 0.5

# 2. Drop the client's pure ACK (handshake completion) to the server.
#    --tcp-flags SYN,ACK ACK = ACK set, SYN NOT set → matches pure ACK, not SYNACK.
#    The server never sees the final ACK → its retransmission timer fires
#    inet_rtx_synack → tcp_retransmit_synack tracepoint fires.
ip netns exec "${TCP_NS_CLIENT}" iptables -I OUTPUT 1 -p tcp --dport "${TEST_PORT}" --tcp-flags SYN,ACK ACK -j DROP
log_info "iptables: DROP pure ACK (dport=${TEST_PORT})"

# 3. Start tcpshark in retransmit mode.
"${TCPSHARK_BIN}" --mode retransmit --bpf-path "${BPF_OBJ}" --duration 8 --output json > "${OUTPUT_DIR}/events.json" 2> "${OUTPUT_DIR}/stderr.log" &
TCPSHARK_PID=$!
sleep 1

# 4. Client connects: SYN → server, SYNACK → client, ACK → dropped.
timeout 3 ip netns exec "${TCP_NS_CLIENT}" bash -c \
	"exec 3<>/dev/tcp/${TCP_NS_SERVER_ADDR}/${TEST_PORT}" 2> /dev/null || true

# 5. Wait for SYNACK retransmissions (initial RTO ~1s, exponential backoff).
sleep 5

kill "${TCPSHARK_PID}" 2> /dev/null || true
sleep 0.3
TCPSHARK_PID=""

# Filter events for our test port (server-side tcp_sport).
grep "\"tcp_sport\":${TEST_PORT}" "${OUTPUT_DIR}/events.json" > "${OUTPUT_DIR}/filtered.json" 2> /dev/null || true

RAW_COUNT=$(grep -c '"event_type":' "${OUTPUT_DIR}/events.json" 2> /dev/null || true)
RAW_COUNT=${RAW_COUNT:-0}
PORT_COUNT=$(grep -c '"event_type":' "${OUTPUT_DIR}/filtered.json" 2> /dev/null || true)
PORT_COUNT=${PORT_COUNT:-0}

SYNACK_COUNT=$(grep -c '"event_type":"tcp_retransmit_synack"' "${OUTPUT_DIR}/filtered.json" 2> /dev/null || true)
SYNACK_COUNT=${SYNACK_COUNT:-0}

log_info "captured retransmit events: raw=${RAW_COUNT}, tcp_sport=${TEST_PORT}: ${PORT_COUNT}"
log_info "matching tcp_retransmit_synack events: ${SYNACK_COUNT}"

if ((SYNACK_COUNT >= 1)); then
	log_info "PASS: SYNACK retrans events detected"
elif ((RAW_COUNT == 0)); then
	log_error "FAIL: tcpshark produced no retransmit events"
elif ((PORT_COUNT == 0)); then
	log_error "FAIL: raw events exist but none matched tcp_sport=${TEST_PORT}"
else
	log_error "FAIL: matching-port events have no tcp_retransmit_synack event"
fi

if ((SYNACK_COUNT == 0)); then
	cat "${OUTPUT_DIR}/events.json" 2> /dev/null || true
	cat "${OUTPUT_DIR}/stderr.log" 2> /dev/null || true
	fatal "synack test failed"
fi
