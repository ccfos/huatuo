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
source "${ROOT_DIR}/integration/lib_namespace.sh"

SERVER_IP="10.200.1.2"
CLIENT_IP="10.200.1.1"
TEST_PORT=19876

_server_pid=""
WORK_DIR=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/net-rx-latency.XXXXXX")
cleanup_all() {
	[[ -n "${_server_pid}" ]] && stop_by_pid "${_server_pid}" 2 || true
	tcp_namespace_cleanup
}
trap cleanup_all EXIT

tcp_namespace_setup rxlat "${SERVER_IP}" "${CLIENT_IP}"
sleep 0.5

integration_huatuo_bamai_start \
	write_net_rx_latency_config \
	--region dev \
	--procfs-prefix "${HUATUO_BAMAI_TEST_FIXTURES}" \
	--disable-kubelet

# The tcp_v4_rcv hook has an fentry and a kprobe entry point. The daemon must
# report which one it selected: without that line an operator cannot tell a
# kernel that fell back from a tracer that silently stopped covering the hook.
#
# Which one it selects is the kernel's decision, so it is not predicted here -
# the tracing variant test proves that with a real attempt. This checks that the
# daemon reported a decision, that it is one of the two entry points, and that a
# fallback carries the reason it happened.
assert_tracing_entry_point() {
	local log="${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log"
	local selection selected reason

	grep -q 'msg="loaded BPF with a selected tracing entry point"' "${log}" \
		|| fatal "the daemon did not report the selected tracing entry point"

	selection=$(grep 'msg="loaded BPF with a selected tracing entry point"' "${log}" | tail -1)
	selected=$(grep -o 'mode="[a-z]*"' <<< "${selection}" | tr -d '"' | cut -d= -f2)
	reason=$(grep -o 'reason="[^"]*"' <<< "${selection}" | tr -d '"' | cut -d= -f2)

	case "${selected}" in
	fentry)
		log_info "net_rx_latency selected the fentry entry point (tcp_v4_rcv_fentry_prog)"
		;;
	kprobe)
		# Both kprobe paths have to say why they are kprobe, and they are
		# different paths: a kernel the probe rules out selects kprobe without
		# an attempt, a kernel whose fentry attempt failed falls back to it.
		# Which one happens is the kernel's decision, so neither is predicted.
		[[ -n "${reason}" ]] \
			|| fatal "net_rx_latency selected the kprobe entry point without a reason"
		if grep -q 'msg="attached kprobe entry point after fentry fallback"' "${log}"; then
			log_info "net_rx_latency fell back to the kprobe entry point (tcp_v4_rcv_prog): ${reason}"
		else
			log_info "net_rx_latency selected the kprobe entry point without an fentry attempt: ${reason}"
		fi
		;;
	*)
		fatal "net_rx_latency reported an unknown entry point: ${selected:-none}"
		;;
	esac
}

assert_tracing_entry_point

SLOW_TCP_SERVER="${WORK_DIR}/slow-tcp-server"
compile_user_fixture \
	"${ROOT_DIR}/integration/testdata/test_net_rx_latency_user.c" \
	"${SLOW_TCP_SERVER}"

ip netns exec "${TCP_NS_SERVER}" "${SLOW_TCP_SERVER}" \
	> "${WORK_DIR}/testserver.log" 2>&1 &
server_pid=$!
_server_pid="${server_pid}"
sleep 0.5

for i in $(seq 1 5); do
	log_info "curl request #${i} to ${TCP_NS_SERVER_ADDR}:${TEST_PORT}"
	# --noproxy: an http_proxy in the environment sends curl to the proxy
	# instead of the peer, which fails instantly and generates no packet at all.
	ip netns exec "${TCP_NS_CLIENT}" curl -s --noproxy '*' \
		--connect-timeout 1 --max-time 2 \
		http://${TCP_NS_SERVER_ADDR}:${TEST_PORT}/ \
		>> "${WORK_DIR}/curl.log" 2>&1 || true
done

sleep 5

EVENTS_FILE="${HUATUO_BAMAI_TEST_TMPDIR}/events/net_rx_latency"
[[ -f "${EVENTS_FILE}" ]] || fatal "no events file: ${EVENTS_FILE}"

# Filter events matching our veth IP pair, then validate.
MATCHED=$(jq -s --arg saddr "${TCP_NS_CLIENT_ADDR}" --arg daddr "${TCP_NS_SERVER_ADDR}" \
	'[.[] | select(
		(.tracer_data.tcp_saddr == $saddr)
		and (.tracer_data.tcp_daddr == $daddr)
		and (.tracer_data.latency_stage | type == "string")
		and (.tracer_data.latency_ms | type == "number")
		and (.tracer_data.latency_threshold_ms | type == "number")
		and (.tracer_data.packet_len_bytes | type == "number")
		and (.tracer_data.net_namespace_inum | type == "number")
		and (.tracer_data.net_namespace_cookie | type == "number")
		and (.tracer_data | has("lat_stage") | not)
		and (.tracer_data | has("lat_ms") | not)
		and (.tracer_data | has("lat_thresholds") | not)
		and (.tracer_data | has("pkt_len") | not)
	)]' \
	"${EVENTS_FILE}" 2> /dev/null)

event_count=$(echo "${MATCHED}" | jq 'length' 2> /dev/null || echo 0)
event_count=$(echo "${event_count}" | tr -d '[:space:]')
log_info "net_rx_latency events (${TCP_NS_CLIENT_ADDR} -> ${TCP_NS_SERVER_ADDR}): ${event_count}"

if [[ "${event_count}" -eq 0 ]]; then
	fatal "no matching net_rx_latency events found"
fi

log_info "net_rx_latency integration test passed: ${event_count} events"
log_info "valid event:"
echo "${MATCHED}" | jq '.[0]' 2> /dev/null || echo "${MATCHED}"
