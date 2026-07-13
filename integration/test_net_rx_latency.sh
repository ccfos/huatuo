#!/usr/bin/env bash

# Copyright 2026 The HuaTuo Authors.
#
# Authors:
# Tonghao Zhang <tonghao@bamaicloud.com>
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

# Verify net_rx_latency BPF program detects TCP receive-path latency events.
# Uses a veth pair (has NAPI, so skb->tstamp is set) with a slow TCP server
# that delays recv() to guarantee latency exceeds the 1ms threshold.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"

[[ $EUID -eq 0 ]] || skip "requires root"

VETH_HOST="veth-rxlat-h"
VETH_PEER="veth-rxlat-p"
VETH_HOST_IP="10.200.1.1"
VETH_PEER_IP="10.200.1.2"
TEST_PORT=19876

ip link add "${VETH_HOST}" type veth peer name "${VETH_PEER}" 2> /dev/null || skip "veth creation failed"
ip addr add "${VETH_HOST_IP}/24" dev "${VETH_HOST}" 2> /dev/null || true
ip addr add "${VETH_PEER_IP}/24" dev "${VETH_PEER}" 2> /dev/null || true
ip link set "${VETH_HOST}" up 2> /dev/null || true
ip link set "${VETH_PEER}" up 2> /dev/null || true
sleep 0.5

_server_pid=""
WORK_DIR=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/net-rx-latency.XXXXXX")
cleanup_all() {
	[[ -n "${_server_pid}" ]] && stop_by_pid "${_server_pid}" 2 || true
	ip link del "${VETH_HOST}" 2> /dev/null || true
}
trap cleanup_all EXIT

integration_huatuo_bamai_start \
	write_net_rx_latency_config \
	--region dev \
	--procfs-prefix "${HUATUO_BAMAI_TEST_FIXTURES}" \
	--disable-kubelet

SLOW_TCP_SERVER="${WORK_DIR}/slow-tcp-server"
compile_user_fixture \
	"${ROOT_DIR}/integration/testdata/test_net_rx_latency_user.c" \
	"${SLOW_TCP_SERVER}"

"${SLOW_TCP_SERVER}" \
	> "${WORK_DIR}/testserver.log" 2>&1 &
server_pid=$!
_server_pid="${server_pid}"
sleep 0.5

for i in $(seq 1 5); do
	log_info "curl request #${i} to ${VETH_PEER_IP}:${TEST_PORT}"
	curl -s --connect-timeout 1 --max-time 2 \
		--interface "${VETH_HOST_IP}" \
		http://${VETH_PEER_IP}:${TEST_PORT}/ \
		>> "${WORK_DIR}/curl.log" 2>&1 || true
done

sleep 5

EVENTS_FILE="${HUATUO_BAMAI_TEST_TMPDIR}/events/net_rx_latency"
[[ -f "${EVENTS_FILE}" ]] || fatal "no events file: ${EVENTS_FILE}"

# Filter events matching our veth IP pair, then validate.
MATCHED=$(jq -s --arg saddr "${VETH_HOST_IP}" --arg daddr "${VETH_PEER_IP}" \
	'[.[] | select(.tracer_data.tcp_saddr == $saddr and .tracer_data.tcp_daddr == $daddr)]' \
	"${EVENTS_FILE}" 2> /dev/null)

event_count=$(echo "${MATCHED}" | jq 'length' 2> /dev/null || echo 0)
event_count=$(echo "${event_count}" | tr -d '[:space:]')
log_info "net_rx_latency events (${VETH_HOST_IP} -> ${VETH_PEER_IP}): ${event_count}"

if [[ "${event_count}" -eq 0 ]]; then
	fatal "no matching net_rx_latency events found"
fi

log_info "net_rx_latency integration test passed: ${event_count} events"
log_info "event details:"
echo "${MATCHED}" | jq '.' 2> /dev/null || echo "${MATCHED}"
