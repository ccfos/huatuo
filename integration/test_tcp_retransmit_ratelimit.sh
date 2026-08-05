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

TCPSHARK_BIN="${ROOT_DIR}/_output/bin/tcpshark"
BPF_OBJ="${ROOT_DIR}/_output/bpf/tcp_retransmit.o"
OUTPUT_DIR=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/tcp-retransmit-ratelimit.XXXXXX")
RATE=1
DURATION=8
EXPECTED_MAX=$((RATE * (DURATION + 1)))
TEST_PORT=19997
PAYLOAD_SIZE=2097152

NS_S="ts_rl"
NS_C="tc_rl"
VETH_S="vs_rl"
VETH_C="vc_rl"
S_ADDR="10.99.1.1"
C_ADDR="10.99.1.2"
NET_MASK="24"

[[ -x "${TCPSHARK_BIN}" ]] || fatal "tcpshark binary not found: ${TCPSHARK_BIN}"
[[ -f "${BPF_OBJ}" ]] || fatal "BPF object not found: ${BPF_OBJ}"

if ! iptables -m connbytes -h 2>&1 | grep -q connbytes; then
	log_info "SKIP: iptables connbytes module not available on this kernel"
	exit 0
fi

require_python3

cleanup() {
	[[ -n "${TCPSHARK_PID:-}" ]] && kill "${TCPSHARK_PID}" 2>/dev/null || true
	[[ -n "${SRV_PID:-}" ]] && kill "${SRV_PID}" 2>/dev/null || true
	[[ -n "${CLI_PID:-}" ]] && kill "${CLI_PID}" 2>/dev/null || true
	sleep 0.2
	[[ -n "${TCPSHARK_PID:-}" ]] && kill -9 "${TCPSHARK_PID}" 2>/dev/null || true
	ip netns del "${NS_S}" 2>/dev/null || true
	ip netns del "${NS_C}" 2>/dev/null || true
	rm -rf "${OUTPUT_DIR}"
}
trap cleanup EXIT

log_info "tcp retrans rate limit: rate=${RATE}/s, duration=${DURATION}s, deterministic netns loss"

ip netns add "${NS_S}" || fatal "failed to create server netns"
ip netns add "${NS_C}" || fatal "failed to create client netns"
ip link add "${VETH_S}" type veth peer name "${VETH_C}"
ip link set "${VETH_S}" netns "${NS_S}"
ip link set "${VETH_C}" netns "${NS_C}"
ip netns exec "${NS_S}" ip addr add "${S_ADDR}/${NET_MASK}" dev "${VETH_S}"
ip netns exec "${NS_S}" ip link set "${VETH_S}" up
ip netns exec "${NS_S}" ip link set lo up
ip netns exec "${NS_S}" ip link set dev "${VETH_S}" gso_max_segs 1
ip netns exec "${NS_C}" ip addr add "${C_ADDR}/${NET_MASK}" dev "${VETH_C}"
ip netns exec "${NS_C}" ip link set "${VETH_C}" up
ip netns exec "${NS_C}" ip link set lo up
ip netns exec "${NS_C}" iptables -I INPUT 1 -p tcp --sport "${TEST_PORT}" \
	-m connbytes --connbytes 30:60 --connbytes-dir reply \
	--connbytes-mode packets -j DROP

"${TCPSHARK_BIN}" --mode retransmit --bpf-path "${BPF_OBJ}" \
	--max-events-per-second "${RATE}" \
	--duration "${DURATION}" --output json \
	>"${OUTPUT_DIR}/events.json" 2>"${OUTPUT_DIR}/stderr.log" &
TCPSHARK_PID=$!
sleep 1

ip netns exec "${NS_S}" timeout 10 python3 "${ROOT_DIR}/integration/testdata/tcp_server.py" \
	--listen-address "${S_ADDR}" --port "${TEST_PORT}" \
	--payload-bytes "${PAYLOAD_SIZE}" >/dev/null 2>&1 &
SRV_PID=$!
sleep 0.5

ip netns exec "${NS_C}" timeout 8 bash -c \
	"exec 3<>/dev/tcp/${S_ADDR}/${TEST_PORT}; cat <&3 >/dev/null" 2>/dev/null &
CLI_PID=$!

sleep 6

wait "${TCPSHARK_PID}" || true
TCPSHARK_PID=""

events=$(grep -c '"event_type":"tcp_retransmit_' "${OUTPUT_DIR}/events.json" 2>/dev/null || true)
events=${events:-0}
warns=$(grep -h "rate limit hit" \
	"${OUTPUT_DIR}/events.json" "${OUTPUT_DIR}/stderr.log" 2>/dev/null |
	wc -l || true)
warns=${warns:-0}

log_info "events=${events} (cap=${EXPECTED_MAX}), rate-limit warnings=${warns}"

((events >= 1)) || fatal "expected at least one admitted retransmit event"
((events <= EXPECTED_MAX)) || fatal "events ${events} exceed cap ${EXPECTED_MAX}"
((warns >= 1)) || fatal "expected at least one rate-limit warning under flood"
