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

# Verify that tcpshark correlates a TCP data retransmission with the local
# software drop that caused it and reports the drop stack in the same event.

set -euo pipefail

source "$(dirname "$0")/env.sh"
source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_namespace.sh"

bpf_tool_setup tcpshark tcp_retransmit tcp-retrans-dropwatch-correlation

readonly CORR_TCPSHARK_BIN=${TOOL_BIN}
readonly CORR_BPF_DIR="${ROOT_DIR}/_output/bpf"
readonly CORR_DROPWATCH_BPF="${CORR_BPF_DIR}/dropwatch.o"
readonly CORR_OUTPUT=${TOOL_OUT}
readonly CORR_ERROR=${TOOL_ERR}
readonly CORR_MATCHED_EVENT="${TOOL_WORK_DIR}/matched-event.json"
readonly CORR_SERVER_LOG="${TOOL_WORK_DIR}/server.log"
readonly CORR_CLIENT_LOG="${TOOL_WORK_DIR}/client.log"
readonly CORR_PAYLOAD_BYTES=2097152
readonly CORR_SERVER_ADDR="10.99.0.1"
readonly CORR_CLIENT_ADDR="10.99.0.2"

command -v ip > /dev/null 2>&1 || skip "ip command is not installed"
command -v jq > /dev/null 2>&1 || skip "jq command is not installed"
command -v ss > /dev/null 2>&1 || skip "ss command is not installed"
command -v tc > /dev/null 2>&1 || skip "tc command is not installed"
require_python3
[[ -r "${CORR_DROPWATCH_BPF}" ]] \
	|| fatal "dropwatch BPF object is not readable: ${CORR_DROPWATCH_BPF}"

CORR_PORT=$(allocate_available_port) \
	|| fatal "failed to allocate a TCP server port"
readonly CORR_PORT

corr_tcpshark_pid=""
corr_server_pid=""
corr_client_pid=""
corr_netem_active=false

remove_netem_loss() {
	[[ "${corr_netem_active}" == true ]] || return 0
	tc -n "${TCP_NS_SERVER}" qdisc del dev "${TCP_NS_VETH_SERVER}" root \
		2> /dev/null || true
	corr_netem_active=false
}

cleanup() {
	[[ -z "${corr_tcpshark_pid}" ]] \
		|| stop_by_pid "${corr_tcpshark_pid}" 5 || true
	[[ -z "${corr_server_pid}" ]] \
		|| stop_by_pid "${corr_server_pid}" 2 || true
	[[ -z "${corr_client_pid}" ]] \
		|| stop_by_pid "${corr_client_pid}" 2 || true
	remove_netem_loss
	tcp_namespace_cleanup
}
trap cleanup EXIT

tcpshark_running() {
	[[ -n "${corr_tcpshark_pid}" ]] \
		&& kill -0 "${corr_tcpshark_pid}" 2> /dev/null
}

correlated_event_ready() {
	[[ -s "${CORR_OUTPUT}" ]] || return 1
	jq -s -c -e \
		--arg server "${CORR_SERVER_ADDR}" \
		--arg client "${CORR_CLIENT_ADDR}" \
		--argjson port "${CORR_PORT}" '
		first(.[]
		| select(.event_type == "tcp_retransmit_skb")
		| select(.phase == "data")
		| select(.tcp_saddr == $server and .tcp_daddr == $client)
		| select(.tcp_sport == $port)
		| select(.drop_location == "host_software")
		| select((.drop_stack | type) == "string" and (.drop_stack | length) > 0))
	' "${CORR_OUTPUT}" > "${CORR_MATCHED_EVENT}" 2> /dev/null
}

tcp_namespace_setup corr "${CORR_SERVER_ADDR}" "${CORR_CLIENT_ADDR}"

# Keep data segments separate so netem drops expose stable sequence ranges.
ip netns exec "${TCP_NS_SERVER}" \
	ip link set dev "${TCP_NS_VETH_SERVER}" gso_max_segs 1
ip netns exec "${TCP_NS_CLIENT}" \
	ethtool -K "${TCP_NS_VETH_CLIENT}" gro off 2> /dev/null || true

tc -n "${TCP_NS_SERVER}" qdisc replace dev "${TCP_NS_VETH_SERVER}" root \
	netem loss 2%
corr_netem_active=true

"${CORR_TCPSHARK_BIN}" \
	--mode retransmit \
	--with-dropwatch \
	--bpf-path-dir "${CORR_BPF_DIR}" \
	--filter "tcp and port ${CORR_PORT}" \
	--duration 20 \
	--output json \
	> "${CORR_OUTPUT}" 2> "${CORR_ERROR}" &
corr_tcpshark_pid=$!

sleep 1
tcpshark_running || fatal "tcpshark exited before the workload started"

ip netns exec "${TCP_NS_SERVER}" timeout 15 python3 \
	"${ROOT_DIR}/integration/testdata/tcp_server.py" \
	--listen-address "${CORR_SERVER_ADDR}" \
	--port "${CORR_PORT}" \
	--payload-bytes "${CORR_PAYLOAD_BYTES}" \
	> "${CORR_SERVER_LOG}" 2>&1 &
corr_server_pid=$!
sleep 0.5

ip netns exec "${TCP_NS_CLIENT}" timeout 12 bash -c \
	"exec 3<>/dev/tcp/${CORR_SERVER_ADDR}/${CORR_PORT}; cat <&3 >/dev/null" \
	> "${CORR_CLIENT_LOG}" 2>&1 &
corr_client_pid=$!

wait_until 15 1 correlated_event_ready \
	|| fatal "tcpshark did not correlate a retransmission with a software drop"

remove_netem_loss
wait "${corr_server_pid}" 2> /dev/null || true
corr_server_pid=""
wait "${corr_client_pid}" 2> /dev/null || true
corr_client_pid=""

tcpshark_status=0
stop_and_wait_by_pid "${corr_tcpshark_pid}" || tcpshark_status=$?
corr_tcpshark_pid=""
((tcpshark_status == 0)) \
	|| fatal "tcpshark exited with status ${tcpshark_status}"

assert_log_has_no_failure "${CORR_ERROR}" "tcpshark"
log_info "correlated event: $(< "${CORR_MATCHED_EVENT}")"
