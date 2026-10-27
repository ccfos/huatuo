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

# Exercise real tcpshark -> bamai output under receiver suspension and shutdown.
# Protocol frame ordering and End counts remain covered by component tests.
set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"

require_commands python3 jq tc ip unshare mount ss curl
if [[ ${1:-} != --isolated ]]; then
	for scenario in blocked healthy timeout; do
		log_info "toolstream shutdown scenario: ${scenario}"
		unshare --net --mount --propagation private bash "$0" --isolated "${scenario}"
		log_info "toolstream shutdown scenario passed: ${scenario}"
	done
	exit 0
fi
readonly TOOLSTREAM_SCENARIO=$2
HUATUO_BAMAI_TEST_TMPDIR=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/${TOOLSTREAM_SCENARIO}.XXXXXX")
export HUATUO_BAMAI_TEST_TMPDIR
ip link set lo up

bpf_tool_setup tcpshark tcp_retransmit "toolstream-${TOOLSTREAM_SCENARIO}"
readonly TOOLSTREAM_SOCKET="/var/run/huatuo-toolstream.sock"
readonly TOOLSTREAM_EVENTS="${HUATUO_BAMAI_TEST_TMPDIR}/events/tcp_retransmit"
readonly TOOLSTREAM_LOG="${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log"
readonly TOOLSTREAM_TASK="output-${TOOLSTREAM_SCENARIO}"
readonly TOOLSTREAM_STATE="${TOOL_WORK_DIR}/receiver-state.txt"
readonly TOOLSTREAM_QUEUES="${TOOL_WORK_DIR}/socket-queues.txt"
readonly TOOLSTREAM_ADDRESS="127.0.0.99"
TOOLSTREAM_PORT=$(allocate_available_port) || fatal "cannot allocate the retransmit traffic port"
readonly TOOLSTREAM_PORT
TOOLSTREAM_HTTP_PORT=$(allocate_available_port) || fatal "cannot allocate the bamai HTTP port"
readonly TOOLSTREAM_HTTP_PORT
HUATUO_BAMAI_ADDR="http://127.0.0.1:${TOOLSTREAM_HTTP_PORT}"
HUATUO_BAMAI_METRICS_API="${HUATUO_BAMAI_ADDR}/metrics"
toolstream_pid=""
toolstream_traffic_pid=""
toolstream_full_since=-1

cleanup() {
	stop_and_wait_by_pid "${toolstream_traffic_pid}" 1 || true
	toolstream_traffic_pid=""
	stop_and_wait_by_pid "${toolstream_pid}" 7 || true
	toolstream_pid=""
	# Restore the receiver before shared shutdown, including failed assertions.
	if [[ -r "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid" ]]; then
		kill -CONT "$(< "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")" 2> /dev/null || true
	fi
	huatuo_bamai_stop || true
}
trap cleanup EXIT

toolstream_events_ready() {
	jq -s -e --arg address "${TOOLSTREAM_ADDRESS}" --argjson port "${TOOLSTREAM_PORT}" '
		[.[] | select(.tracer_data.tcp_daddr == $address and .tracer_data.tcp_dport == $port)] | length >= 10
	' "${TOOLSTREAM_EVENTS}" > /dev/null 2>&1
}

toolstream_is_backpressured() {
	ss -x -n -m -p -H -O > "${TOOLSTREAM_QUEUES}"
	# One record per socket keeps process identity and memory limits together.
	if ! awk -F '[(),[:space:]]+' -v pid="pid=${toolstream_pid}," '
		index($0, pid) {
			allocated = capacity = 0
			for (i = 1; i <= NF; i++) {
				if ($i ~ /^t[0-9]+$/) allocated = substr($i, 2) + 0
				if ($i ~ /^tb[0-9]+$/) capacity = substr($i, 3) + 0
			}
			if (capacity > 0 && allocated >= capacity) full = 1
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

toolstream_connection_logged() {
	local state=$1
	grep -Eq "(^|[[:space:]\"])${state} service=tcpshark .*task_id=${TOOLSTREAM_TASK}(\"|[[:space:]]|$)" "${TOOLSTREAM_LOG}"
}

toolstream_receiver_stopped() {
	local pid
	pid=$(< "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")
	cat "/proc/${pid}/status" > "${TOOLSTREAM_STATE}"
	grep -qE '^State:[[:space:]]+T' "${TOOLSTREAM_STATE}"
}

assert_toolstream_finalization() {
	wait_until 5 0.1 toolstream_connection_logged disconnected \
		|| fatal "bamai did not finish toolstream session ${TOOLSTREAM_TASK}"
	jq -s -e --arg address "${TOOLSTREAM_ADDRESS}" --argjson port "${TOOLSTREAM_PORT}" '
		[.[] | select(.tracer_name == "tcp_retransmit") | .tracer_data
			| select(.tcp_daddr == $address and .tcp_dport == $port)
			| select(.correlation_reason == "interrupted")] | length > 0
	' "${TOOLSTREAM_EVENTS}" > /dev/null \
		|| fatal "bamai stored no interrupted tail event for ${TOOLSTREAM_ADDRESS}:${TOOLSTREAM_PORT}; see ${TOOLSTREAM_EVENTS}"
	log_info "bamai stored tail event: $(jq -sc '[.[] | select(.tracer_data.correlation_reason == "interrupted")][0]' "${TOOLSTREAM_EVENTS}")"
}

assert_toolstream_exit() {
	local status=0
	if [[ ${TOOLSTREAM_SCENARIO} != timeout ]]; then
		kill -TERM "${toolstream_pid}" || fatal "cannot signal tcpshark"
	fi
	wait_until 7 0.1 process_exited "${toolstream_pid}" \
		|| fatal "tcpshark did not exit within 7 seconds in ${TOOLSTREAM_SCENARIO} scenario"
	wait "${toolstream_pid}" || status=$?
	toolstream_pid=""

	if [[ ${TOOLSTREAM_SCENARIO} == timeout ]]; then
		[[ ${status} -ne 0 ]] || fatal "tcpshark ignored the active send timeout"
		grep -q 'toolstream: output timed out' "${TOOL_ERR}" || fatal "tcpshark did not report the output timeout"
	else
		[[ ${status} -eq 0 ]] || fatal "tcpshark shutdown failed: status=${status}; expected 0"
	fi
}

# Keep bamai's fixed runtime socket and PID file private to this scenario.
mkdir -p "${TOOL_WORK_DIR}/run"
mount --bind "${TOOL_WORK_DIR}/run" /var/run
integration_huatuo_bamai_start write_config_bamai_toolstream_receiver \
	--region dev --disable-kubelet --procfs-prefix "${HUATUO_BAMAI_TEST_FIXTURES}" \
	--bpf-dir "${ROOT_DIR}/_output/bpf" --tools-bin-dir "${ROOT_DIR}/_output/bin"

# Excluding loopback from dropwatch leaves retransmissions pending; the isolated
# namespace and destination select only this workload's observations.
tc qdisc add dev lo root netem loss 100%
"${TOOL_BIN}" --mode retransmit --with-dropwatch --device-excluded lo \
	--bpf-path-dir "${ROOT_DIR}/_output/bpf" --duration 60 \
	--max-events-per-second 0 --output-storage "${TOOLSTREAM_SOCKET}" --task-id "${TOOLSTREAM_TASK}" \
	> "${TOOL_OUT}" 2> "${TOOL_ERR}" &
toolstream_pid=$!
wait_until 10 0.1 toolstream_connection_logged connected || fatal "bamai did not accept tcpshark session ${TOOLSTREAM_TASK}"

if [[ ${TOOLSTREAM_SCENARIO} != healthy ]]; then
	kill -STOP "$(< "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")" || fatal "cannot suspend bamai"
	wait_until 5 0.1 toolstream_receiver_stopped || fatal "bamai did not stop: $(< "${TOOLSTREAM_STATE}")"
fi

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

if [[ ${TOOLSTREAM_SCENARIO} != healthy ]]; then
	wait_until 30 0.1 toolstream_is_backpressured \
		|| fatal "toolstream send memory did not stay saturated: $(< "${TOOLSTREAM_QUEUES}")"
	log_info "toolstream backlog: $(< "${TOOLSTREAM_QUEUES}")"
else
	wait_until 25 0.1 toolstream_events_ready || fatal "tcpshark did not observe retransmit traffic"
fi

assert_toolstream_exit

if [[ ${TOOLSTREAM_SCENARIO} == healthy ]]; then
	assert_toolstream_finalization
	assert_log_has_no_failure "${TOOLSTREAM_LOG}" "healthy bamai receiver"
fi

if [[ ${TOOLSTREAM_SCENARIO} != timeout ]]; then
	assert_log_has_no_failure "${TOOL_ERR}" "toolstream ${TOOLSTREAM_SCENARIO} shutdown"
fi
kill -CONT "$(< "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")" || fatal "cannot resume bamai"
assert_huatuo_bamai_shutdown
