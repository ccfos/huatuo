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

bpf_tool_setup tcpshark tcp_retransmit tcp-retransmit-ratelimit
TCPSHARK_BIN=${TOOL_BIN}
BPF_OBJ=${TOOL_BPF}
OUTPUT_DIR=${TOOL_WORK_DIR}
RATE=1
DURATION=8
EXPECTED_MAX=$((RATE * (DURATION + 1)))
TEST_PORT=19997
PAYLOAD_SIZE=2097152

S_ADDR="10.99.1.1"
C_ADDR="10.99.1.2"

if ! iptables -m connbytes -h 2>&1 | grep -q connbytes; then
	skip "iptables connbytes module not available on this kernel"
fi

require_commands python3 ss

cleanup() {
	local status=$?
	[[ -n "${TCPSHARK_PID:-}" ]] && kill "${TCPSHARK_PID}" 2> /dev/null || true
	[[ -n "${SRV_PID:-}" ]] && kill "${SRV_PID}" 2> /dev/null || true
	[[ -n "${CLI_PID:-}" ]] && kill "${CLI_PID}" 2> /dev/null || true
	sleep 0.2
	[[ -n "${TCPSHARK_PID:-}" ]] && kill -9 "${TCPSHARK_PID}" 2> /dev/null || true
	tcp_namespace_cleanup
	if ((status != 0)); then
		dump_text_files "${OUTPUT_DIR}" || true
	else
		rm -rf "${OUTPUT_DIR}"
	fi
}
trap cleanup EXIT

tcp_server_is_listening() {
	ip netns exec "${TCP_NS_SERVER}" ss -ltnH "sport = :${TEST_PORT}" | grep -q .
}

log_info "tcp retrans rate limit: rate=${RATE}/s, duration=${DURATION}s, deterministic netns loss"

tcp_namespace_setup rl "${S_ADDR}" "${C_ADDR}"
ip netns exec "${TCP_NS_SERVER}" ip link set dev "${TCP_NS_VETH_SERVER}" gso_max_segs 1
ip netns exec "${TCP_NS_CLIENT}" iptables -I INPUT 1 -p tcp --sport "${TEST_PORT}" \
	-m connbytes --connbytes 30:60 --connbytes-dir reply \
	--connbytes-mode packets -j DROP

# A fixed delay can start the client before Python binds under VM load.
ip netns exec "${TCP_NS_SERVER}" timeout 15 python3 "${ROOT_DIR}/integration/testdata/tcp_server.py" \
	--listen-address "${TCP_NS_SERVER_ADDR}" --port "${TEST_PORT}" \
	--payload-bytes "${PAYLOAD_SIZE}" > "${OUTPUT_DIR}/server.log" 2>&1 &
SRV_PID=$!
wait_until 5 0.1 tcp_server_is_listening || fatal "TCP fixture did not start listening"

"${TCPSHARK_BIN}" --mode retransmit --bpf-path "${BPF_OBJ}" \
	--max-events-per-second "${RATE}" \
	--duration "${DURATION}" --output json \
	> "${OUTPUT_DIR}/events.json" 2> "${OUTPUT_DIR}/stderr.log" &
TCPSHARK_PID=$!
sleep 1
if ! kill -0 "${TCPSHARK_PID}" 2> /dev/null; then
	tcpshark_status=0
	wait "${TCPSHARK_PID}" || tcpshark_status=$?
	TCPSHARK_PID=""
	fatal "tcpshark exited before workload start (status=${tcpshark_status})"
fi

ip netns exec "${TCP_NS_CLIENT}" timeout 8 bash -c \
	"exec 3<>/dev/tcp/${TCP_NS_SERVER_ADDR}/${TEST_PORT}; cat <&3 >/dev/null" \
	2> "${OUTPUT_DIR}/client.log" &
CLI_PID=$!

sleep 6

tcpshark_status=0
wait "${TCPSHARK_PID}" || tcpshark_status=$?
TCPSHARK_PID=""
((tcpshark_status == 0)) || fatal "tcpshark exited with status ${tcpshark_status}"

events=$(grep -c '"event_type":"tcp_retransmit_' "${OUTPUT_DIR}/events.json" 2> /dev/null || true)
events=${events:-0}
warns=$(grep -h "rate limit hit" \
	"${OUTPUT_DIR}/events.json" "${OUTPUT_DIR}/stderr.log" 2> /dev/null \
	| wc -l || true)
warns=${warns:-0}

log_info "events=${events} (cap=${EXPECTED_MAX}), rate-limit warnings=${warns}"

((events >= 1)) || fatal "expected at least one admitted retransmit event"
((events <= EXPECTED_MAX)) || fatal "events ${events} exceed cap ${EXPECTED_MAX}"
((warns >= 1)) || fatal "expected at least one rate-limit warning under flood"
