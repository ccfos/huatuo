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

bpf_tool_setup tcpshark tcp_retransmit tcp-retransmit-syn
TCPSHARK_BIN=${TOOL_BIN}
BPF_OBJ=${TOOL_BPF}
OUTPUT_DIR=${TOOL_WORK_DIR}
TEST_PORT=19991
SYN_REJECT_PORT=$((TEST_PORT + 1))
S_ADDR="10.99.2.1"
C_ADDR="10.99.2.2"

cleanup() {
	[[ -n "${TCPSHARK_PID:-}" ]] && kill "${TCPSHARK_PID}" 2> /dev/null || true
	[[ -n "${reject_tcpshark_pid:-}" ]] && kill "${reject_tcpshark_pid}" 2> /dev/null || true
	sleep 0.2
	[[ -n "${TCPSHARK_PID:-}" ]] && kill -9 "${TCPSHARK_PID}" 2> /dev/null || true
	[[ -n "${reject_tcpshark_pid:-}" ]] && kill -9 "${reject_tcpshark_pid}" 2> /dev/null || true
	tcp_namespace_cleanup
	rm -rf "${OUTPUT_DIR}"
}
trap cleanup EXIT

log_info "S0/EXP1: SYN retrans (connect/RTO) via isolated netns drop"

tcp_namespace_setup syn "${S_ADDR}" "${C_ADDR}"

# Drop SYN packets in the peer namespace so the client enters TCP RTO retry.
ip netns exec "${TCP_NS_SERVER}" iptables -I INPUT 1 -p tcp --dport "${TEST_PORT}" -j DROP

"${TCPSHARK_BIN}" --mode retransmit --bpf-path "${BPF_OBJ}" \
	--filter "tcp and dst host ${TCP_NS_SERVER_ADDR} and dst port ${TEST_PORT}" \
	--duration 6 --output json > "${OUTPUT_DIR}/events.json" 2> "${OUTPUT_DIR}/stderr.log" &
TCPSHARK_PID=$!
"${TCPSHARK_BIN}" --mode retransmit --bpf-path "${BPF_OBJ}" \
	--filter "tcp and dst host ${TCP_NS_SERVER_ADDR} and dst port ${SYN_REJECT_PORT}" \
	--duration 6 --output json > "${OUTPUT_DIR}/rejected-events.json" \
	2> "${OUTPUT_DIR}/rejected-stderr.log" &
reject_tcpshark_pid=$!
sleep 1

timeout 7 ip netns exec "${TCP_NS_CLIENT}" bash -c \
	"exec 3<>/dev/tcp/${TCP_NS_SERVER_ADDR}/${TEST_PORT}" 2> /dev/null || true
sleep 1

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

# Filter events for our test port (active-open destination tcp_dport).
grep "\"tcp_dport\":${TEST_PORT}" "${OUTPUT_DIR}/events.json" > "${OUTPUT_DIR}/filtered.json" 2> /dev/null || true

RAW_COUNT=$(grep -c '"event_type":' "${OUTPUT_DIR}/events.json" 2> /dev/null || true)
RAW_COUNT=${RAW_COUNT:-0}
PORT_COUNT=$(grep -c '"event_type":' "${OUTPUT_DIR}/filtered.json" 2> /dev/null || true)
PORT_COUNT=${PORT_COUNT:-0}

SYN_COUNT=$(grep '"phase":"connect"' "${OUTPUT_DIR}/filtered.json" 2> /dev/null \
	| grep '"tcp_reason":"RTO"' \
	| grep '"tcp_flags":"SYN"' \
	| grep -c '"event_type":"tcp_retransmit_skb"' || true)
SYN_COUNT=${SYN_COUNT:-0}
REJECTED_SYN_COUNT=$(grep "\"tcp_dport\":${TEST_PORT}" "${OUTPUT_DIR}/rejected-events.json" 2> /dev/null \
	| grep -c '"event_type":"tcp_retransmit_skb"' || true)
REJECTED_SYN_COUNT=${REJECTED_SYN_COUNT:-0}

log_info "captured retransmit events: raw=${RAW_COUNT}, tcp_dport=${TEST_PORT}: ${PORT_COUNT}"
log_info "matching connect/RTO/SYN tcp_retransmit_skb events: ${SYN_COUNT}"
log_info "target events accepted=${SYN_COUNT}, rejected=${REJECTED_SYN_COUNT}"

if ((SYN_COUNT >= 1)); then
	log_info "EXP1 PASS: SYN retrans events detected with phase=connect, tcp_reason=RTO"
elif ((RAW_COUNT == 0)); then
	log_error "EXP1 FAIL: tcpshark produced no retransmit events"
elif ((PORT_COUNT == 0)); then
	log_error "EXP1 FAIL: raw events exist but none matched tcp_dport=${TEST_PORT}"
else
	log_error "EXP1 FAIL: matching-port events have no connect/RTO/tcp_retransmit_skb event"
fi

if ((SYN_COUNT == 0)); then
	cat "${OUTPUT_DIR}/events.json" 2> /dev/null || true
	cat "${OUTPUT_DIR}/stderr.log" 2> /dev/null || true
	fatal "EXP1 failed"
fi

if ((REJECTED_SYN_COUNT != 0)); then
	cat "${OUTPUT_DIR}/rejected-events.json" 2> /dev/null || true
	cat "${OUTPUT_DIR}/rejected-stderr.log" 2> /dev/null || true
	fatal "nonmatching filter captured ${REJECTED_SYN_COUNT} SYN retransmit events"
fi
