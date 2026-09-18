#!/usr/bin/env bash

# Copyright 2026 The HuaTuo Authors
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

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"
source "${ROOT_DIR}/integration/lib_namespace.sh"

[[ $EUID -eq 0 ]] || skip "requires root"
command -v tc > /dev/null || skip "requires tc"

SERVER_IP="10.200.1.2"
CLIENT_IP="10.200.1.1"
TEST_PORT=19876

server_pid=""
WORK_DIR=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/net-tx-latency.XXXXXX")
cleanup_all() {
	[[ -z "${server_pid}" ]] || stop_by_pid "${server_pid}" 2 || true
	tcp_namespace_cleanup
}
trap cleanup_all EXIT

tcp_namespace_setup txlat "${SERVER_IP}" "${CLIENT_IP}"
ip netns exec "${TCP_NS_CLIENT}" tc qdisc add \
	dev "${TCP_NS_VETH_CLIENT}" root netem delay 20ms

integration_huatuo_bamai_start \
	write_net_tx_latency_config \
	--region dev \
	--procfs-prefix "${HUATUO_BAMAI_TEST_FIXTURES}" \
	--disable-kubelet

TCP_SERVER="${WORK_DIR}/tcp-server"
compile_user_fixture \
	"${ROOT_DIR}/integration/testdata/test_net_rx_latency_user.c" \
	"${TCP_SERVER}"

ip netns exec "${TCP_NS_SERVER}" "${TCP_SERVER}" \
	> "${WORK_DIR}/server.log" 2>&1 &
server_pid=$!
sleep 0.5

for i in $(seq 1 5); do
	log_info "TX latency request #${i}"
	ip netns exec "${TCP_NS_CLIENT}" curl -s --connect-timeout 1 --max-time 3 \
		http://${TCP_NS_SERVER_ADDR}:${TEST_PORT}/ \
		>> "${WORK_DIR}/curl.log" 2>&1 || true
done

sleep 5

EVENTS_FILE="${HUATUO_BAMAI_TEST_TMPDIR}/events/net_tx_latency"
[[ -f "${EVENTS_FILE}" ]] || fatal "no events file: ${EVENTS_FILE}"

MATCHED=$(jq -s --arg saddr "${TCP_NS_CLIENT_ADDR}" --arg daddr "${TCP_NS_SERVER_ADDR}" \
	'[.[] | select(.tracer_data.tcp_saddr == $saddr and .tracer_data.tcp_daddr == $daddr)]' \
	"${EVENTS_FILE}" 2> /dev/null)

event_count=$(echo "${MATCHED}" | jq 'length' | tr -d '[:space:]')
[[ "${event_count}" -gt 0 ]] || fatal "no matching net_tx_latency events found"

qdisc_count=$(echo "${MATCHED}" | jq \
	'[.[] | select(.tracer_data.latency_stage == "TX_STAGE_QDISC_DEV")] | length' \
	| tr -d '[:space:]')
[[ "${qdisc_count}" -gt 0 ]] || fatal "no qdisc-stage TX latency event found"

huatuo_bamai_await_metrics
check_metrics "TX latency" \
	"net_tx_latency_events_total" \
	"net_tx_latency_seconds_total" \
	"net_tx_latency_last_seconds"

log_info "net_tx_latency integration test passed: ${event_count} events"
