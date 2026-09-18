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

# Verify bamai launches tcpshark, persists Toolstream events, and reaps the child.
# Retransmission classification is covered by the standalone tcpshark tests.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"

readonly TCP_RETRANS_SOURCE_IP="192.0.2.1"
readonly TCP_RETRANS_TARGET_IP="192.0.2.2"
readonly TCP_RETRANS_NETNS="huatuo-retrans-$$"
readonly TCP_RETRANS_EVENT="${HUATUO_BAMAI_TEST_TMPDIR}/events/tcp_retransmit"
readonly TCP_RETRANS_SAMPLE="${HUATUO_BAMAI_TEST_TMPDIR}/tcp-retransmit-event.json"
readonly TCP_RETRANS_PARSE_ERROR="${HUATUO_BAMAI_TEST_TMPDIR}/event-parse.err"

command -v ip > /dev/null || skip "ip command is not installed"
command -v jq > /dev/null || skip "jq command is not installed"
command -v ss > /dev/null || skip "ss command is not installed"
command -v curl > /dev/null || skip "curl command is not installed"
command -v pgrep > /dev/null || skip "pgrep command is not installed"
command -v timeout > /dev/null || skip "timeout command is not installed"
command -v unshare > /dev/null || skip "unshare command is not installed"
command -v mount > /dev/null || skip "mount command is not installed"
[[ -x "${HUATUO_BAMAI_BIN}" ]] || fatal "huatuo-bamai binary missing: ${HUATUO_BAMAI_BIN}"
[[ -x "${ROOT_DIR}/_output/bin/tcpshark" ]] || fatal "tcpshark binary missing"
[[ -r "${ROOT_DIR}/_output/bpf/tcp_retransmit.o" ]] || fatal "tcp_retransmit BPF object missing"
tracepoint_available tcp tcp_retransmit_skb || skip "tcp/tcp_retransmit_skb tracepoint is unavailable"
tracepoint_available tcp tcp_retransmit_synack || skip "tcp/tcp_retransmit_synack tracepoint is unavailable"

# A private mount prevents the fixed Toolstream socket and PID file from
# interfering with other daemons or subsequent tests in the runner.
if [[ ${1:-} != --isolated ]]; then
	exec unshare --mount --propagation private bash "$0" --isolated
fi

TCP_RETRANS_HTTP_PORT=$(allocate_available_port) || fatal "failed to allocate bamai HTTP port"
readonly TCP_RETRANS_HTTP_PORT
TCP_RETRANS_TARGET_PORT=$(allocate_available_port) || fatal "failed to allocate TCP target port"
readonly TCP_RETRANS_TARGET_PORT
readonly TCP_RETRANS_FILTER="tcp and dst host ${TCP_RETRANS_TARGET_IP} and dst port ${TCP_RETRANS_TARGET_PORT}"
HUATUO_BAMAI_ADDR="http://127.0.0.1:${TCP_RETRANS_HTTP_PORT}"
HUATUO_BAMAI_METRICS_API="${HUATUO_BAMAI_ADDR}/metrics"

tcp_retrans_child_pid=""
tcp_retrans_workload_pid=""
tcp_retrans_netns_created=false

cleanup() {
	if [[ -n ${tcp_retrans_workload_pid} ]]; then
		stop_and_wait_by_pid "${tcp_retrans_workload_pid}" 2 || true
	fi
	huatuo_bamai_stop || true
	[[ -z ${tcp_retrans_child_pid} ]] || stop_by_pid "${tcp_retrans_child_pid}" 2 || true
	if [[ ${tcp_retrans_netns_created} == true ]]; then
		ip netns del "${TCP_RETRANS_NETNS}" || true
		tcp_retrans_netns_created=false
	fi
}
trap cleanup EXIT

tcp_retransmit_child_started() {
	local bamai_pid child_pid
	bamai_pid=$(< "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")
	child_pid=$(pgrep -P "${bamai_pid}" -x tcpshark) || return 1
	[[ $(readlink "/proc/${child_pid}/exe") == "$(readlink -f "${ROOT_DIR}/_output/bin/tcpshark")" ]] || return 1
	tcp_retrans_child_pid=${child_pid}
}

tcp_retransmit_event_is_stored() {
	[[ -s ${TCP_RETRANS_EVENT} ]] || return 1
	jq -s -e --arg source "${TCP_RETRANS_SOURCE_IP}" \
		--arg target "${TCP_RETRANS_TARGET_IP}" --argjson port "${TCP_RETRANS_TARGET_PORT}" '
		first(.[] | select(
			.tracer_name == "tcp_retransmit"
			and .tracer_type == "event"
			and (.tracer_id | type == "string" and length > 0)
			and .tracer_data.source == "events"
			and .tracer_data.event_type == "tcp_retransmit_skb"
			and .tracer_data.tcp_saddr == $source
			and .tracer_data.tcp_daddr == $target
			and .tracer_data.tcp_dport == $port
		))
	' "${TCP_RETRANS_EVENT}" > "${TCP_RETRANS_SAMPLE}" 2> "${TCP_RETRANS_PARSE_ERROR}"
}

assert_tcp_retransmit_shutdown() {
	local bamai_pid
	bamai_pid=$(< "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")
	huatuo_bamai_stop
	wait "${bamai_pid}" || fatal "huatuo-bamai did not exit successfully"
	wait_until 5 0.1 test ! -d "/proc/${tcp_retrans_child_pid}" || fatal "tcpshark survived bamai shutdown"
	tcp_retrans_child_pid=""
}

mkdir -p "${HUATUO_BAMAI_TEST_TMPDIR}/run"
mount --bind "${HUATUO_BAMAI_TEST_TMPDIR}/run" /var/run
ip netns add "${TCP_RETRANS_NETNS}" || skip "network namespace creation is unavailable"
tcp_retrans_netns_created=true
ip -n "${TCP_RETRANS_NETNS}" link add retransmit0 type dummy || skip "dummy network device is unavailable"
ip -n "${TCP_RETRANS_NETNS}" addr add "${TCP_RETRANS_SOURCE_IP}/24" dev retransmit0
ip -n "${TCP_RETRANS_NETNS}" link set retransmit0 up
ip -n "${TCP_RETRANS_NETNS}" link set lo up

integration_huatuo_bamai_start write_tcp_events_config \
	--region dev --disable-kubelet --log-debug
wait_until 15 0.2 tcp_retransmit_child_started || fatal "bamai did not launch the tcpshark binary"

# The NOARP dummy device discards SYNs without sending a reset or requiring a
# remote host, firewall rules or neighbor discovery to keep the connection open.
ip netns exec "${TCP_RETRANS_NETNS}" timeout --kill-after=2 15 \
	bash -c 'printf x > "/dev/tcp/$1/$2"' _ "${TCP_RETRANS_TARGET_IP}" "${TCP_RETRANS_TARGET_PORT}" \
	> "${HUATUO_BAMAI_TEST_TMPDIR}/connect.log" 2>&1 &
tcp_retrans_workload_pid=$!

wait_until 30 0.5 tcp_retransmit_event_is_stored || fatal \
	"no matching TCP retransmit event persisted; parser: $(cat "${TCP_RETRANS_PARSE_ERROR}" 2> /dev/null || true)"
tcp_retrans_connect_status=0
wait "${tcp_retrans_workload_pid}" || tcp_retrans_connect_status=$?
tcp_retrans_workload_pid=""
[[ ${tcp_retrans_connect_status} -eq 124 ]] || fatal \
	"TCP connect exited ${tcp_retrans_connect_status}, expected timeout (124)"

assert_tcp_retransmit_shutdown
# Parse again after shutdown so a partially appended JSON object cannot pass.
tcp_retransmit_event_is_stored || fatal "stored TCP retransmit event is invalid"
assert_kernel_observation_timestamps "${TCP_RETRANS_SAMPLE}"
assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "huatuo-bamai"
log_info "valid TCP retransmit event: $(jq -c . "${TCP_RETRANS_SAMPLE}")"
log_info "bamai TCP retransmit lifecycle and persistence passed"
