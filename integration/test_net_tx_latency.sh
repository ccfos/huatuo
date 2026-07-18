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

# Verify net_tx_latency BPF program detects TCP send-path latency events.
#
# The peer veth end lives in its own network namespace. This is essential:
# if both ends share the host netns the peer IP is "local" and the kernel
# routes host->peer over loopback, so the packets never traverse the veth and
# net_dev_queue / net_dev_xmit never fire. A netem egress delay on the host
# side inflates the net_dev_queue -> net_dev_xmit (qdisc/driver) latency above
# the 1ms threshold so TX_STAGE_NIC events fire reliably.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"

[[ $EUID -eq 0 ]] || skip "requires root"

VETH_HOST="veth-txlat-h"
VETH_PEER="veth-txlat-p"
NS_PEER="txlat-ns"
VETH_HOST_IP="10.200.2.1"
VETH_PEER_IP="10.200.2.2"
TEST_PORT=19877

ip netns add "${NS_PEER}" 2> /dev/null || skip "netns creation failed"
ip link add "${VETH_HOST}" type veth peer name "${VETH_PEER}" 2> /dev/null || {
	ip netns del "${NS_PEER}" 2> /dev/null || true
	skip "veth creation failed"
}
ip link set "${VETH_PEER}" netns "${NS_PEER}"
ip addr add "${VETH_HOST_IP}/24" dev "${VETH_HOST}" 2> /dev/null || true
ip link set "${VETH_HOST}" up 2> /dev/null || true
ip netns exec "${NS_PEER}" ip addr add "${VETH_PEER_IP}/24" dev "${VETH_PEER}" 2> /dev/null || true
ip netns exec "${NS_PEER}" ip link set "${VETH_PEER}" up 2> /dev/null || true
ip netns exec "${NS_PEER}" ip link set lo up 2> /dev/null || true
sleep 0.5

# Inflate TX (qdisc/driver) latency so it exceeds the 1ms threshold.
modprobe sch_netem 2> /dev/null || true
tc qdisc add dev "${VETH_HOST}" root netem delay 2ms 2> /dev/null || {
	ip link del "${VETH_HOST}" 2> /dev/null || true
	ip netns del "${NS_PEER}" 2> /dev/null || true
	skip "netem qdisc unavailable, cannot inflate TX latency"
}

_original_args_str="${HUATUO_BAMAI_INTEGRATION_ARGS_STR}"
_sink_pid=""
cleanup_all() {
	if [[ -n "${_sink_pid}" ]]; then
		kill "${_sink_pid}" 2> /dev/null || true
		wait "${_sink_pid}" 2> /dev/null || true
	fi
	huatuo_bamai_stop $? 2> /dev/null || true
	tc qdisc del dev "${VETH_HOST}" root 2> /dev/null || true
	ip link del "${VETH_HOST}" 2> /dev/null || true
	ip netns del "${NS_PEER}" 2> /dev/null || true
	export HUATUO_BAMAI_INTEGRATION_ARGS_STR="${_original_args_str}"
	HUATUO_BAMAI_ARGS_INTEGRATION=""
}
trap cleanup_all EXIT

HUATUO_BAMAI_ARGS_INTEGRATION=(
	"--config-dir" "${HUATUO_BAMAI_TEST_TMPDIR}"
	"--config" "bamai.conf"
	"--region" "dev"
	"--procfs-prefix" "${HUATUO_BAMAI_TEST_FIXTURES}"
	"--disable-kubelet"
)
HUATUO_BAMAI_INTEGRATION_ARGS_STR="${HUATUO_BAMAI_ARGS_INTEGRATION[*]}"
export HUATUO_BAMAI_INTEGRATION_ARGS_STR

write_net_tx_latency_config() {
	cat > "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" << EOF
BlackList = ["metax_gpu", "ascend_npu", "softlockup", "ethtool", "netstat_hw", "iolatency", "memory_free", "memory_reclaim", "reschedipi", "softirq", "iotracing", "dropwatch", "net_rx_latency"]

[EventTracing.NetTxLatency]
    Sendmsg2Qdisc = 1
    Qdisc2Nic = 1
    ExcludedHostNetnamespace = false

[Storage.LocalFile]
    Path = "${HUATUO_BAMAI_TEST_TMPDIR}/events"
EOF
}

# Pass HUATUO_BAMAI_ARGS_INTEGRATION explicitly so integration_huatuo_bamai_start
# takes its non-default branch — otherwise the helper defaults to
# --disable-storage and the [Storage.LocalFile] events this test asserts on are
# never written.
integration_huatuo_bamai_start write_net_tx_latency_config "${HUATUO_BAMAI_ARGS_INTEGRATION[@]}"
trap cleanup_all EXIT

TCP_SINK="${HUATUO_BAMAI_TEST_TMPDIR}/tx-sink"
cc -O2 -Wall -Wextra -o "${TCP_SINK}" \
	"${ROOT_DIR}/integration/testdata/test_net_tx_latency_sink.c" \
	|| skip "failed to compile tx sink"

ip netns exec "${NS_PEER}" "${TCP_SINK}" \
	> "${HUATUO_BAMAI_TEST_TMPDIR}/sink.log" 2>&1 &
_sink_pid=$!
sleep 0.5

# Push a few MB of TX through the netem-delayed veth so the net_dev_queue ->
# net_dev_xmit latency reliably exceeds the 1ms threshold.
for i in $(seq 1 3); do
	log_info "tx burst #${i} -> ${VETH_PEER_IP}:${TEST_PORT}"
	dd if=/dev/zero bs=1024 count=2048 2>/dev/null | \
		curl -s --connect-timeout 1 --max-time 5 \
			--interface "${VETH_HOST_IP}" \
			--data-binary @- \
			"http://${VETH_PEER_IP}:${TEST_PORT}/" \
			>> "${HUATUO_BAMAI_TEST_TMPDIR}/curl.log" 2>&1 || true
done

sleep 5

EVENTS_FILE="${HUATUO_BAMAI_TEST_TMPDIR}/events/net_tx_latency"
[[ -f "${EVENTS_FILE}" ]] || {
	dump_file "HUATUO" "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log"
	fatal "no events file: ${EVENTS_FILE}"
}

# TX direction: saddr = host, daddr = peer.
MATCHED=$(jq -s --arg saddr "${VETH_HOST_IP}" --arg daddr "${VETH_PEER_IP}" \
	'[.[] | select(.tracer_data.tcp_saddr == $saddr and .tracer_data.tcp_daddr == $daddr)]' \
	"${EVENTS_FILE}" 2> /dev/null)

event_count=$(echo "${MATCHED}" | jq 'length' 2> /dev/null || echo 0)
event_count=$(echo "${event_count}" | tr -d '[:space:]')
log_info "net_tx_latency events (${VETH_HOST_IP} -> ${VETH_PEER_IP}): ${event_count}"

if [[ "${event_count}" -eq 0 ]]; then
	dump_file "EVENTS" "${EVENTS_FILE}"
	dump_file "SINK" "${HUATUO_BAMAI_TEST_TMPDIR}/sink.log"
	dump_file "HUATUO" "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log"
	fatal "no matching net_tx_latency events found"
fi

log_info "net_tx_latency integration test passed: ${event_count} events"
log_info "event details:"
echo "${MATCHED}" | jq '.' 2> /dev/null || echo "${MATCHED}"
