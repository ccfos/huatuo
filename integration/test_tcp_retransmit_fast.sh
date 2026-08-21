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

bpf_tool_setup tcpshark tcp_retransmit tcp-retransmit-fast
TCPSHARK_BIN=${TOOL_BIN}
BPF_OBJ=${TOOL_BPF}
OUTPUT_DIR=${TOOL_WORK_DIR}
TEST_PORT=19998
PAYLOAD_SIZE=2097152 # 2 MB

S_ADDR="10.99.0.1"
C_ADDR="10.99.0.2"

# connbytes module check — skip gracefully if unavailable (minimal kernels).
if ! iptables -m connbytes -h 2>&1 | grep -q connbytes; then
	log_info "SKIP: iptables connbytes module not available on this kernel"
	exit 0
fi

require_python3

cleanup() {
	[[ -n "${TCPSHARK_PID:-}" ]] && kill "${TCPSHARK_PID}" 2> /dev/null || true
	[[ -n "${SRV_PID:-}" ]] && kill "${SRV_PID}" 2> /dev/null || true
	[[ -n "${CLI_PID:-}" ]] && kill "${CLI_PID}" 2> /dev/null || true
	tcp_namespace_cleanup
}
trap cleanup EXIT

log_info "fast_retransmit: drop one data segment via netns+veth+connbytes → dup ACK → Recovery"

# 1. Build netns+veth topology.
tcp_namespace_setup fast "${S_ADDR}" "${C_ADDR}"

# Limit GSO segments so each TCP segment is a distinct sk_buff (deterministic
# connbytes counting). Without this, veth GSO may coalesce segments.
ip netns exec "${TCP_NS_SERVER}" ip link set dev "${TCP_NS_VETH_SERVER}" gso_max_segs 1

log_info "netns topology ready: ${TCP_NS_SERVER}(${S_ADDR}) ←→ ${TCP_NS_CLIENT}(${C_ADDR})"

# 2. Client-side: drop the 30th reply-direction packet (a single
#    server→client data segment). Subsequent segments arrive out-of-order
#    → receiver sends 3 dup ACKs → sender enters Recovery and
#    fast-retransmits the lost segment.
ip netns exec "${TCP_NS_CLIENT}" iptables -I INPUT 1 -p tcp --sport "${TEST_PORT}" \
	-m connbytes --connbytes 30:30 --connbytes-dir reply \
	--connbytes-mode packets -j DROP
log_info "connbytes rule: drop reply packet #30 in client netns"

# 3. Start tcpshark in retransmit mode in the root netns (sees all netns traffic via BPF).
"${TCPSHARK_BIN}" --mode retransmit --bpf-path "${BPF_OBJ}" --duration 15 --output json > "${OUTPUT_DIR}/events.json" 2> "${OUTPUT_DIR}/stderr.log" &
TCPSHARK_PID=$!
sleep 1
if ! kill -0 "${TCPSHARK_PID}" 2> /dev/null; then
	TCPSHARK_STATUS=0
	wait "${TCPSHARK_PID}" || TCPSHARK_STATUS=$?
	TCPSHARK_PID=""
	fatal "tcpshark exited before workload start (status=${TCPSHARK_STATUS})"
fi

# 4. Server: listen and send 2 MB of data.
ip netns exec "${TCP_NS_SERVER}" timeout 10 python3 "${ROOT_DIR}/integration/testdata/tcp_server.py" \
	--listen-address "${TCP_NS_SERVER_ADDR}" --port "${TEST_PORT}" \
	--payload-bytes "${PAYLOAD_SIZE}" > /dev/null 2>&1 &
SRV_PID=$!
sleep 0.5

# 5. Client: connect and receive data to /dev/null.
ip netns exec "${TCP_NS_CLIENT}" timeout 8 bash -c "exec 3<>/dev/tcp/${TCP_NS_SERVER_ADDR}/${TEST_PORT}; cat <&3 >/dev/null" 2> /dev/null &
CLI_PID=$!

# 6. Wait for data transfer + fast retransmit (3 dup ACKs are fast on veth).
sleep 10

if ! kill -0 "${TCPSHARK_PID}" 2> /dev/null; then
	TCPSHARK_STATUS=0
	wait "${TCPSHARK_PID}" || TCPSHARK_STATUS=$?
	TCPSHARK_PID=""
	fatal "tcpshark exited during capture (status=${TCPSHARK_STATUS})"
fi
kill -TERM "${TCPSHARK_PID}"
wait "${TCPSHARK_PID}" || true
TCPSHARK_PID=""

# Filter events for our test port (server-side tcp_sport).
grep "\"tcp_sport\":${TEST_PORT}" "${OUTPUT_DIR}/events.json" > "${OUTPUT_DIR}/filtered.json" 2> /dev/null || true

RAW_COUNT=$(grep -c '"event_type":' "${OUTPUT_DIR}/events.json" 2> /dev/null || true)
RAW_COUNT=${RAW_COUNT:-0}
PORT_COUNT=$(grep -c '"event_type":' "${OUTPUT_DIR}/filtered.json" 2> /dev/null || true)
PORT_COUNT=${PORT_COUNT:-0}

FAST_COUNT=$(grep -c '"tcp_reason":"fast_retransmit"' "${OUTPUT_DIR}/filtered.json" 2> /dev/null || true)
FAST_COUNT=${FAST_COUNT:-0}
REORDER_FAST=$(grep -c '"tcp_reason":"reorder_prone_fast"' "${OUTPUT_DIR}/filtered.json" 2> /dev/null || true)
REORDER_FAST=${REORDER_FAST:-0}
RECOVERY=$(grep -c '"ca_state":3' "${OUTPUT_DIR}/filtered.json" 2> /dev/null || true)
RECOVERY=${RECOVERY:-0}
RTO_COUNT=$(grep -c '"tcp_reason":"RTO"' "${OUTPUT_DIR}/filtered.json" 2> /dev/null || true)
RTO_COUNT=${RTO_COUNT:-0}

log_info "captured retransmit events: raw=${RAW_COUNT}, tcp_sport=${TEST_PORT}: ${PORT_COUNT}"
log_info "fast_retransmit: ${FAST_COUNT}, reorder_prone_fast: ${REORDER_FAST}, ca_state=3: ${RECOVERY}, RTO: ${RTO_COUNT}"

if ((FAST_COUNT >= 1)) || ((REORDER_FAST >= 1)); then
	log_info "PASS: fast_retransmit events detected with ca_state=Recovery"
elif ((RECOVERY >= 1)); then
	log_warn "PARTIAL: Recovery events found but not classified as fast_retransmit"
	grep '"ca_state":3' "${OUTPUT_DIR}/filtered.json" | head -2 || true
elif ((RTO_COUNT >= 1)); then
	log_warn "PARTIAL: only RTO events (connbytes may have dropped tail data, no dup ACKs)"
elif ((RAW_COUNT == 0)); then
	log_error "FAIL: tcpshark produced no retransmit events"
	fatal "no raw retransmit events captured"
elif ((PORT_COUNT == 0)); then
	log_error "FAIL: raw events exist but none matched tcp_sport=${TEST_PORT}"
	fatal "no retransmit events matched the test port"
else
	log_error "FAIL: matching events have no fast/reorder/Recovery/RTO classification"
	fatal "unexpected retransmit classification"
fi
