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
OUTPUT_DIR=$(mktemp -d /tmp/tcp_retrans_ratelimit.XXXXXX)
RATE=1
DURATION=8
CONNECTIONS=20
CONNECT_TIMEOUT=4
EXPECTED_MAX=$((RATE * (DURATION + 1)))

[[ -x "${TCPSHARK_BIN}" ]] || fatal "tcpshark binary not found: ${TCPSHARK_BIN}"
[[ -f "${BPF_OBJ}" ]] || fatal "BPF object not found: ${BPF_OBJ}"

cleanup() {
	[[ -n "${TCPSHARK_PID:-}" ]] && kill "${TCPSHARK_PID}" 2> /dev/null || true
	sleep 0.2
	[[ -n "${TCPSHARK_PID:-}" ]] && kill -9 "${TCPSHARK_PID}" 2> /dev/null || true
	rm -rf "${OUTPUT_DIR}"
}
trap cleanup EXIT

log_info "tcp retrans rate limit: rate=${RATE}/s, duration=${DURATION}s"

"${TCPSHARK_BIN}" --mode retransmit --bpf-path "${BPF_OBJ}" \
	--max-events-per-second "${RATE}" \
	--duration "${DURATION}" --output json \
	> "${OUTPUT_DIR}/events.json" 2> "${OUTPUT_DIR}/stderr.log" &
TCPSHARK_PID=$!
sleep 1

connect_pids=()
for port in $(seq 20000 $((20000 + CONNECTIONS - 1))); do
	timeout "${CONNECT_TIMEOUT}" bash -c \
		"exec 3<>/dev/tcp/192.0.2.1/${port}" 2> /dev/null &
	connect_pids+=("$!")
done
for pid in "${connect_pids[@]}"; do
	wait "${pid}" || true
done

wait "${TCPSHARK_PID}" || true
TCPSHARK_PID=""

events=$(grep -c '"event_type":"tcp_retransmit_skb"' "${OUTPUT_DIR}/events.json" 2> /dev/null || true)
events=${events:-0}
warns=$(grep -h "rate limit hit" \
	"${OUTPUT_DIR}/events.json" "${OUTPUT_DIR}/stderr.log" 2> /dev/null \
	| wc -l || true)
warns=${warns:-0}

log_info "events=${events} (cap=${EXPECTED_MAX}), rate-limit warnings=${warns}"

((events >= 1)) || fatal "expected at least one admitted retransmit event"
((events <= EXPECTED_MAX)) || fatal "events ${events} exceed cap ${EXPECTED_MAX}"
((warns >= 1)) || fatal "expected at least one rate-limit warning under flood"
