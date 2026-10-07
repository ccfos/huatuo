#!/usr/bin/env bash

# Copyright 2026 The HuaTuo Authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Verify Echo identities through real IPv6 drops and the dropwatch writers.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_namespace.sh"

require_commands ip ip6tables python3 jq
require_readable /proc/net/if_inet6
bpf_tool_setup dropwatch net_dropwatch dropwatch-icmpv6

readonly SERVER_ADDR="2001:db8:1193::1"
readonly CLIENT_ADDR="2001:db8:1193::2"
DROPWATCH_PID=""
TRAFFIC_PID=""
RULE_NS=""
RULE_TYPE=""

cleanup() {
	[[ -z "${TRAFFIC_PID}" ]] || stop_and_wait_by_pid "${TRAFFIC_PID}" || true
	[[ -z "${DROPWATCH_PID}" ]] || stop_and_wait_by_pid "${DROPWATCH_PID}" || true
	tcp_namespace_cleanup
}
trap cleanup EXIT

tcp_namespace_setup "dw6_${BASHPID}" "${SERVER_ADDR}" "${CLIENT_ADDR}" 64

ipv6_addresses_ready() {
	! ip netns exec "${TCP_NS_SERVER}" ip -6 addr show tentative | grep -q inet6 \
		&& ! ip netns exec "${TCP_NS_CLIENT}" ip -6 addr show tentative | grep -q inet6
}
wait_until 5 0.1 ipv6_addresses_ready || fatal "IPv6 addresses did not finish duplicate-address detection"

send_echo() {
	ip netns exec "${TCP_NS_CLIENT}" python3 - "${SERVER_ADDR}" "$1" "$2" << 'PY'
import socket
import struct
import sys
import time

address, identifier, sequence = sys.argv[1], int(sys.argv[2]), int(sys.argv[3])
packet = struct.pack("!BBHHH", 128, 0, 0, identifier, sequence) + b"huatuo"
# Linux computes the mandatory checksum for IPPROTO_ICMPV6 raw sockets.
with socket.socket(socket.AF_INET6, socket.SOCK_RAW, socket.IPPROTO_ICMPV6) as sock:
    for _ in range(30):
        sock.sendto(packet, (address, 0))
        time.sleep(0.1)
PY
}

run_case() {
	local name=$1 typ=$2 identifier=$3 sequence=$4 output=$5
	local out="${TOOL_WORK_DIR}/${name}.${output}"
	local err="${TOOL_WORK_DIR}/${name}.err"
	if [[ -n "${RULE_NS}" ]]; then
		ip netns exec "${RULE_NS}" ip6tables -D INPUT -p ipv6-icmp --icmpv6-type "${RULE_TYPE}" -j DROP
	fi
	RULE_NS=${TCP_NS_SERVER}
	[[ ${typ} != EchoReply ]] || RULE_NS=${TCP_NS_CLIENT}
	RULE_TYPE=echo-request
	[[ ${typ} != EchoReply ]] || RULE_TYPE=echo-reply
	ip netns exec "${RULE_NS}" ip6tables -I INPUT 1 -p ipv6-icmp --icmpv6-type "${RULE_TYPE}" -j DROP \
		|| skip "IPv6 ICMP netfilter rules are unavailable"
	"${TOOL_BIN}" --bpf-path "${TOOL_BPF}" --filter icmp6 --duration 5 --output "${output}" \
		> "${out}" 2> "${err}" &
	DROPWATCH_PID=$!
	send_echo "${identifier}" "${sequence}" > "${TOOL_WORK_DIR}/${name}-traffic.log" 2>&1 &
	TRAFFIC_PID=$!
	wait "${TRAFFIC_PID}" || fatal "${name}: IPv6 Echo traffic failed"
	TRAFFIC_PID=""
	wait "${DROPWATCH_PID}" || fatal "${name}: dropwatch failed"
	DROPWATCH_PID=""
	assert_log_has_no_failure "${err}" dropwatch
	if [[ ${output} == json ]]; then
		jq -es --arg typ "${typ}" --argjson id "${identifier}" --argjson seq "${sequence}" '
		  any(.[];
		    .layers.label == "IPv6/ICMPv6" and .layers.icmp.type == $typ and
		    (if $id == 0 then (.layers.icmp | has("id") | not) else .layers.icmp.id == $id end) and
		    (if $seq == 0 then (.layers.icmp | has("seq") | not) else .layers.icmp.seq == $seq end))
		' "${out}" > /dev/null || fatal "${name}: JSON Echo identity or zero omission is incorrect"
		assert_kernel_observation_timestamps "${out}"
	else
		grep -E "IPv6/ICMPv6 .*type=${typ} .*id=${identifier} seq=${sequence}" "${out}" > /dev/null \
			|| fatal "${name}: text writer omitted the Echo identity"
	fi
	log_info "${name}: ${output} Echo identity verified"
}

run_case request-json EchoRequest 4660 7 json
run_case reply-json EchoReply 65535 65535 json
run_case zero-json EchoRequest 0 0 json
run_case reply-text EchoReply 4660 7 text
