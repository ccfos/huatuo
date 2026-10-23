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

# Exercise toolstream shutdown through the built tcpshark command: a stalled
# receiver must not prevent exit; a healthy receiver retains pending events and End.
set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

require_commands python3 jq tc ip unshare go capnp ss
if [[ ${1:-} != --isolated ]]; then
	for scenario in blocked healthy; do
		log_info "toolstream shutdown scenario: ${scenario}"
		unshare --net bash "$0" --isolated "${scenario}"
		log_info "toolstream shutdown scenario passed: ${scenario}"
	done
	exit 0
fi
readonly TOOLSTREAM_SCENARIO=$2
ip link set lo up

bpf_tool_setup tcpshark tcp_retransmit "toolstream-${TOOLSTREAM_SCENARIO}"
readonly TOOLSTREAM_SOCKET="${TOOL_WORK_DIR}/events.sock"
readonly TOOLSTREAM_FRAMES="${TOOL_WORK_DIR}/frames.jsonl"
readonly TOOLSTREAM_CONNECTED="${TOOL_WORK_DIR}/connected"
readonly TOOLSTREAM_QUEUES="${TOOL_WORK_DIR}/socket-queues.txt"
readonly TOOLSTREAM_ADDRESS="127.0.0.99"
TOOLSTREAM_PORT=$(allocate_available_port) || fatal "cannot allocate the retransmit traffic port"
readonly TOOLSTREAM_PORT
toolstream_pid=""
toolstream_peer_pid=""
toolstream_traffic_pid=""
toolstream_full_since=-1

cleanup() {
	stop_and_wait_by_pid "${toolstream_traffic_pid}" 1 || true
	stop_and_wait_by_pid "${toolstream_pid}" 7 || true
	stop_and_wait_by_pid "${toolstream_peer_pid}" 7 || true
}
trap cleanup EXIT

toolstream_events_ready() {
	jq -s -e --arg address "${TOOLSTREAM_ADDRESS}" --argjson port "${TOOLSTREAM_PORT}" '
		[.[] | select(.event.tcp_daddr == $address and .event.tcp_dport == $port)] | length >= 10
	' "${TOOLSTREAM_FRAMES}" > /dev/null 2>&1
}

toolstream_is_backpressured() {
	ss -x -n -m -p > "${TOOLSTREAM_QUEUES}"
	# Compare allocated send memory with this socket's actual buffer limit.
	if ! awk -v pid="pid=${toolstream_pid}," '
		index($0, pid) { sender = 1 }
		sender && /skmem:/ {
			memory = $0
			sub(/^.*skmem:\(/, "", memory)
			sub(/\).*$/, "", memory)
			allocated = 0
			capacity = 0
			split(memory, fields, ",")
			for (i in fields) {
				if (fields[i] ~ /^t[0-9]/) allocated = substr(fields[i], 2) + 0
				if (fields[i] ~ /^tb[0-9]/) capacity = substr(fields[i], 3) + 0
			}
			if (capacity > 0 && allocated >= capacity) full = 1
			sender = 0
		}
		END { exit !full }
	' "${TOOLSTREAM_QUEUES}"; then
		toolstream_full_since=-1
		return 1
	fi

	if ((toolstream_full_since < 0)); then
		toolstream_full_since=${SECONDS}
	fi
	# Keep generating retransmissions while the full queue cannot advance,
	# so cancellation interrupts a waiting write, not merely queued output.
	((SECONDS - toolstream_full_since >= 2))
}

toolstream_peer_exited() {
	! kill -0 "${toolstream_peer_pid}" 2> /dev/null
}

assert_toolstream_finalization() {
	jq -s -e --arg address "${TOOLSTREAM_ADDRESS}" --argjson port "${TOOLSTREAM_PORT}" '
		.[0].connect.serviceName == "tcpshark"
		and ([.[] | select(.end == true)] | length) == 1 and .[-1].end == true
		and ([.[] | select(.event.tcp_daddr == $address and .event.tcp_dport == $port)
			| .event | select(.correlation_reason == "interrupted")] | length) > 0
	' "${TOOLSTREAM_FRAMES}" > /dev/null \
		|| fatal "tcpshark lost queued interrupted events or the final End frame"
	log_info "tcpshark final frames: $(jq -s -c '{events: map(select(.event != null)) | length, interrupted: map(select(.event.correlation_reason == "interrupted")) | length, end: map(select(.end == true)) | length}' "${TOOLSTREAM_FRAMES}")"
}

assert_toolstream_shutdown() {
	local status=0
	stop_gracefully_by_pid "${toolstream_pid}" 7 || status=$?
	[[ ${status} -eq 0 ]] || fatal "tcpshark shutdown failed: status=${status}; expected exit within output grace and cleanup allowance"
	toolstream_pid=""
	assert_log_has_no_failure "${TOOL_ERR}" "toolstream ${TOOLSTREAM_SCENARIO} shutdown"
}

assert_toolstream_peer_exit() {
	wait_until 5 0.1 toolstream_peer_exited || fatal "toolstream receiver did not finish after command exit"
	local status=0
	wait "${toolstream_peer_pid}" || status=$?
	[[ ${status} -eq 0 ]] || fatal "toolstream receiver failed: status=${status}; expected clean End and EOF"
	toolstream_peer_pid=""
}

# Both scenarios use the same peer; only the healthy peer reads wire frames.
toolstream_peer_args=(--socket "${TOOLSTREAM_SOCKET}" --connected "${TOOLSTREAM_CONNECTED}")
if [[ ${TOOLSTREAM_SCENARIO} == healthy ]]; then
	TOOLSTREAM_SCHEMA_IMPORT=$(GOFLAGS=-mod=mod go list -m -f '{{.Dir}}' capnproto.org/go/capnp/v3)
	readonly TOOLSTREAM_SCHEMA_IMPORT
	toolstream_peer_args+=(
		--output "${TOOLSTREAM_FRAMES}"
		--schema "${ROOT_DIR}/internal/toolstream/transport/event.capnp"
		--schema-import "${TOOLSTREAM_SCHEMA_IMPORT}/std"
	)
fi
python3 "${ROOT_DIR}/integration/testdata/tools/test_toolstream_peer.py" \
	"${toolstream_peer_args[@]}" \
	> "${TOOL_WORK_DIR}/peer.out" 2> "${TOOL_WORK_DIR}/peer.err" &
toolstream_peer_pid=$!
wait_until 5 0.1 test -S "${TOOLSTREAM_SOCKET}" || fatal "toolstream receiver did not start"

# Excluding loopback from dropwatch leaves retransmissions pending; the isolated
# namespace and destination select only this workload's observations.
tc qdisc add dev lo root netem loss 100%
"${TOOL_BIN}" --mode retransmit --with-dropwatch --device-excluded lo \
	--bpf-path-dir "${ROOT_DIR}/_output/bpf" --duration 60 \
	--max-events-per-second 0 --output-storage "${TOOLSTREAM_SOCKET}" \
	> "${TOOL_OUT}" 2> "${TOOL_ERR}" &
toolstream_pid=$!
wait_until 10 0.1 test -f "${TOOLSTREAM_CONNECTED}" || fatal "tcpshark did not connect to toolstream receiver"

python3 - "${TOOLSTREAM_ADDRESS}" "${TOOLSTREAM_PORT}" << 'PY' &
import socket
import sys
import time

from collections import deque

connections = deque()
while True:
    now = time.monotonic()
    while connections and now - connections[0][0] >= 2:
        connections.popleft()[1].close()
    connection = socket.socket()
    connection.setblocking(False)
    connection.connect_ex((sys.argv[1], int(sys.argv[2])))
    connections.append((now, connection))
    # Refresh a bounded set of connections for continuous SYN retransmissions.
    time.sleep(0.005)
PY
toolstream_traffic_pid=$!

if [[ ${TOOLSTREAM_SCENARIO} == blocked ]]; then
	wait_until 30 0.1 toolstream_is_backpressured \
		|| fatal "toolstream send memory did not stay saturated: $(< "${TOOLSTREAM_QUEUES}")"
	log_info "toolstream backlog before SIGTERM: $(< "${TOOLSTREAM_QUEUES}")"
else
	wait_until 25 0.1 toolstream_events_ready || fatal "tcpshark did not observe retransmit traffic"
fi

assert_toolstream_shutdown

if [[ ${TOOLSTREAM_SCENARIO} == healthy ]]; then
	assert_toolstream_peer_exit
	assert_toolstream_finalization
fi
