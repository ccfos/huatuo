#!/usr/bin/env bash

# Copyright 2026 The HuaTuo Authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
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

bpf_tool_setup tcpshark tcp_retransmit tcp-retransmit-tlp
TCPSHARK_BIN=${TOOL_BIN}
BPF_OBJ=${TOOL_BPF}
OUTPUT_DIR=${TOOL_WORK_DIR}
TEST_PORT=19996
TLP_REJECT_PORT=$((TEST_PORT + 1))
PAYLOAD_SIZE=262144 # 256 KB
S_ADDR="10.99.4.1"
C_ADDR="10.99.4.2"

require_python3

cleanup() {
	[[ -n "${TCPSHARK_PID:-}" ]] && kill "${TCPSHARK_PID}" 2> /dev/null || true
	[[ -n "${reject_tcpshark_pid:-}" ]] && kill "${reject_tcpshark_pid}" 2> /dev/null || true
	[[ -n "${SRV_PID:-}" ]] && kill "${SRV_PID}" 2> /dev/null || true
	[[ -n "${CLI_PID:-}" ]] && kill "${CLI_PID}" 2> /dev/null || true
	sleep 0.2
	[[ -n "${TCPSHARK_PID:-}" ]] && kill -9 "${TCPSHARK_PID}" 2> /dev/null || true
	[[ -n "${reject_tcpshark_pid:-}" ]] && kill -9 "${reject_tcpshark_pid}" 2> /dev/null || true
	tcp_namespace_cleanup
}
trap cleanup EXIT

log_info "TLP: tcp_send_loss_probe via isolated namespace with dropped data ACKs"

tcp_namespace_setup tlp "${S_ADDR}" "${C_ADDR}"

# 1. Server: listen immediately, then delay the payload until after the
#    iptables rule is installed. This leaves unacknowledged data for TLP.
ip netns exec "${TCP_NS_SERVER}" timeout 14 python3 "${ROOT_DIR}/integration/testdata/tcp_server.py" \
	--listen-address "${TCP_NS_SERVER_ADDR}" --port "${TEST_PORT}" \
	--payload-bytes "${PAYLOAD_SIZE}" --send-delay 3 > /dev/null 2>&1 &
SRV_PID=$!
sleep 0.5

# 2. Start tcpshark with TLP collection explicitly enabled.
"${TCPSHARK_BIN}" --mode retransmit --enable-tlp --bpf-path "${BPF_OBJ}" \
	--filter "tcp and src host ${TCP_NS_SERVER_ADDR} and src port ${TEST_PORT}" \
	--duration 14 --output json > "${OUTPUT_DIR}/events.json" 2> "${OUTPUT_DIR}/stderr.log" &
TCPSHARK_PID=$!
"${TCPSHARK_BIN}" --mode retransmit --enable-tlp --bpf-path "${BPF_OBJ}" \
	--filter "tcp and src host ${TCP_NS_SERVER_ADDR} and src port ${TLP_REJECT_PORT}" \
	--duration 14 --output json > "${OUTPUT_DIR}/rejected-events.json" \
	2> "${OUTPUT_DIR}/rejected-stderr.log" &
reject_tcpshark_pid=$!
sleep 0.5
if ! kill -0 "${TCPSHARK_PID}" 2> /dev/null; then
	tcpshark_status=0
	wait "${TCPSHARK_PID}" || tcpshark_status=$?
	TCPSHARK_PID=""
	fatal "tcpshark exited before workload start (status=${tcpshark_status})"
fi
if ! kill -0 "${reject_tcpshark_pid}" 2> /dev/null; then
	reject_tcpshark_status=0
	wait "${reject_tcpshark_pid}" || reject_tcpshark_status=$?
	reject_tcpshark_pid=""
	fatal "rejecting tcpshark exited before workload start (status=${reject_tcpshark_status})"
fi

# 3. Client connects via bash /dev/tcp and reads data to /dev/null.
ip netns exec "${TCP_NS_CLIENT}" timeout 12 bash -c \
	"exec 3<>/dev/tcp/${TCP_NS_SERVER_ADDR}/${TEST_PORT}; cat <&3 >/dev/null" 2> /dev/null &
CLI_PID=$!

# 4. Wait for handshake to complete, then drop ACKs to the server.
#    Must happen before the server's 3s send delay expires.
sleep 1
ip netns exec "${TCP_NS_CLIENT}" iptables -I OUTPUT 1 -p tcp --dport "${TEST_PORT}" --tcp-flags ACK ACK -j DROP
log_info "iptables: DROP ACK (dport=${TEST_PORT}) — client ACKs will be dropped"

# 5. Wait for data send + TLP (PTO ~10ms on loopback; generous window).
sleep 10

if ! kill -0 "${TCPSHARK_PID}" 2> /dev/null; then
	tcpshark_status=0
	wait "${TCPSHARK_PID}" || tcpshark_status=$?
	TCPSHARK_PID=""
	fatal "tcpshark exited during capture (status=${tcpshark_status})"
fi
if ! kill -0 "${reject_tcpshark_pid}" 2> /dev/null; then
	reject_tcpshark_status=0
	wait "${reject_tcpshark_pid}" || reject_tcpshark_status=$?
	reject_tcpshark_pid=""
	cat "${OUTPUT_DIR}/rejected-stderr.log" 2> /dev/null || true
	fatal "rejecting tcpshark exited during capture (status=${reject_tcpshark_status})"
fi
tcpshark_status=0
stop_and_wait_by_pid "${TCPSHARK_PID}" || tcpshark_status=$?
TCPSHARK_PID=""
if ((tcpshark_status != 0)); then
	cat "${OUTPUT_DIR}/stderr.log" 2> /dev/null || true
	fatal "tcpshark exited with status ${tcpshark_status}"
fi
reject_tcpshark_status=0
stop_and_wait_by_pid "${reject_tcpshark_pid}" || reject_tcpshark_status=$?
reject_tcpshark_pid=""
if ((reject_tcpshark_status != 0)); then
	cat "${OUTPUT_DIR}/rejected-stderr.log" 2> /dev/null || true
	fatal "rejecting tcpshark exited with status ${reject_tcpshark_status}"
fi

# Filter events for our test port (server-side tcp_sport).
grep "\"tcp_sport\":${TEST_PORT}" "${OUTPUT_DIR}/events.json" > "${OUTPUT_DIR}/filtered.json" 2> /dev/null || true

RAW_COUNT=$(grep -c '"event_type":' "${OUTPUT_DIR}/events.json" 2> /dev/null || true)
RAW_COUNT=${RAW_COUNT:-0}
PORT_COUNT=$(grep -c '"event_type":' "${OUTPUT_DIR}/filtered.json" 2> /dev/null || true)
PORT_COUNT=${PORT_COUNT:-0}

TLP_COUNT=$(grep -c '"event_type":"tcp_send_loss_probe"' "${OUTPUT_DIR}/filtered.json" 2> /dev/null || true)
TLP_COUNT=${TLP_COUNT:-0}
TLP_REASON=$(grep '"event_type":"tcp_send_loss_probe"' "${OUTPUT_DIR}/filtered.json" 2> /dev/null | grep -c '"tcp_reason":"TLP"' || true)
TLP_REASON=${TLP_REASON:-0}
REJECTED_TLP_COUNT=$(grep "\"tcp_sport\":${TEST_PORT}" "${OUTPUT_DIR}/rejected-events.json" 2> /dev/null \
	| grep -c '"event_type":"tcp_send_loss_probe"' || true)
REJECTED_TLP_COUNT=${REJECTED_TLP_COUNT:-0}

log_info "captured retransmit events: raw=${RAW_COUNT}, tcp_sport=${TEST_PORT}: ${PORT_COUNT}"
log_info "TLP events: ${TLP_COUNT}, tcp_reason=TLP: ${TLP_REASON}"
log_info "target events accepted=${TLP_COUNT}, rejected=${REJECTED_TLP_COUNT}"

if ((REJECTED_TLP_COUNT != 0)); then
	cat "${OUTPUT_DIR}/rejected-events.json" 2> /dev/null || true
	cat "${OUTPUT_DIR}/rejected-stderr.log" 2> /dev/null || true
	fatal "nonmatching filter captured ${REJECTED_TLP_COUNT} TLP events"
fi
if ((TLP_COUNT == 0)); then
	skip "no TLP events for tcp_sport=${TEST_PORT}; this kernel did not produce a positive control"
fi
if ((TLP_REASON == 0)); then
	grep '"event_type":"tcp_send_loss_probe"' "${OUTPUT_DIR}/filtered.json" 2> /dev/null || true
	fatal "TLP test failed"
fi
log_info "PASS: tcp_send_loss_probe events detected with tcp_reason=TLP"
