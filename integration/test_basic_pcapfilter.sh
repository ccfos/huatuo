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

# Exercise the shared compiler through built dropwatch and tcpshark commands.
# Positive controls bracket exclusions so silence alone cannot pass.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_namespace.sh"

readonly PCAPFILTER_SOURCE="127.0.0.2"
readonly PCAPFILTER_DESTINATION="127.0.0.1"
readonly PCAPFILTER_IPV6_SOURCE="2001:db8:465::2"
readonly PCAPFILTER_IPV6_DESTINATION="2001:db8:465::1"
readonly PCAPFILTER_DURATION=6
readonly PCAPFILTER_TIMEOUT=20

require_commands ip iptables ip6tables python3 jq timeout ss
require_readable /proc/net/if_inet6

pcapfilter_port=$(allocate_available_port) || fatal "cannot allocate destination port"
pcapfilter_source_port=$(allocate_available_port) || fatal "cannot allocate source port"
pcapfilter_capture_pid=""
pcapfilter_packet_pid=""
pcapfilter_connect_pid=""
pcapfilter_source=${PCAPFILTER_SOURCE}
pcapfilter_destination=${PCAPFILTER_DESTINATION}

cleanup() {
	stop_and_wait_by_pid "${pcapfilter_capture_pid}" || true
	stop_and_wait_by_pid "${pcapfilter_packet_pid}" || true
	stop_and_wait_by_pid "${pcapfilter_connect_pid}" || true
	namespace_cleanup
}
trap cleanup EXIT

assert_pcapfilter_capture() {
	local tool=$1 expected=$2 output=$3 count
	# Normalize only public tuple/protocol fields from the two command outputs.
	jq -s --arg tool "${tool}" --arg source "${pcapfilter_source}" \
		--arg destination "${pcapfilter_destination}" --argjson port "${pcapfilter_port}" '
		[.[] | if $tool == "dropwatch" then
			{source: (.layers.ipv4.saddr // .layers.ipv6.saddr),
			 destination: (.layers.ipv4.daddr // .layers.ipv6.daddr),
			 port: (.layers.tcp.dport // .layers.udp.dport),
			 protocol: (.layers.ipv4.protocol // .layers.ipv6.next_header)}
		else
			{source: .tcp_saddr, destination: .tcp_daddr, port: .tcp_dport, protocol: "TCP"}
		end | select(.source == $source and .destination == $destination and .port == $port)]
	' "${output}" > "${output}.tuples.json" || fatal "invalid ${tool} JSON: ${output}"
	count=$(jq length "${output}.tuples.json")
	if [[ ${expected} == none ]]; then
		assert_eq "${count}" 0 "${tool} excluded tuple count" || fatal "unexpected matching events: ${output}"
	else
		jq -e --arg expected "${expected}" 'length > 0 and all(.[]; .protocol == $expected)' \
			"${output}.tuples.json" > /dev/null \
			|| fatal "expected only ${expected} events from ${tool}, observed $(< "${output}.tuples.json")"
	fi
}

capture_case() {
	local tool=$1 name=$2 expression=$3 expected=$4 status=0
	local output="${TOOL_WORK_DIR}/${tool}-${name}.json"
	local error="${TOOL_WORK_DIR}/${tool}-${name}.err"
	local arguments=()
	if [[ ${tool} == tcpshark ]]; then
		arguments=(--mode retransmit)
	fi

	# Continuous traffic spans attach and the complete bounded capture window.
	timeout --kill-after=5s "${PCAPFILTER_TIMEOUT}s" "${TOOL_BIN}" "${arguments[@]}" \
		--bpf-path "${TOOL_BPF}" --filter "${expression}" \
		--duration "${PCAPFILTER_DURATION}" --output json > "${output}" 2> "${error}" &
	pcapfilter_capture_pid=$!
	wait "${pcapfilter_capture_pid}" || status=$?
	pcapfilter_capture_pid=""
	assert_eq "${status}" 0 "${tool} ${name} exit status" || fatal "capture failed: ${error}"
	assert_pcapfilter_capture "${tool}" "${expected}" "${output}"
	assert_log_has_no_failure "${error}" "${tool} ${name}"
	log_info "${tool} ${name}: filter=${expression}, expected=${expected}"
}

namespace_setup "pcap_${BASHPID}"
ip -n "${NETNS}" link set lo up
for protocol in tcp udp; do
	ip netns exec "${NETNS}" iptables -A INPUT -i lo \
		-s "${PCAPFILTER_SOURCE}" -d "${PCAPFILTER_DESTINATION}" \
		-p "${protocol}" --dport "${pcapfilter_port}" -j DROP \
		|| skip "IPv4 ${protocol} netfilter DROP rules are unavailable"
done

ip netns exec "${NETNS}" python3 "${ROOT_DIR}/integration/testdata/test_packet_sender.py" ipv4-options \
	--source "${PCAPFILTER_SOURCE}" --warmup-source "${PCAPFILTER_SOURCE}" \
	--destination "${PCAPFILTER_DESTINATION}" --source-port "${pcapfilter_source_port}" \
	--destination-port "${pcapfilter_port}" > "${HUATUO_BAMAI_TEST_TMPDIR}/packets.log" 2>&1 &
pcapfilter_packet_pid=$!
ip netns exec "${NETNS}" python3 "${ROOT_DIR}/integration/testdata/test_packet_sender.py" tcp-connect \
	--source "${PCAPFILTER_SOURCE}" --destination "${PCAPFILTER_DESTINATION}" \
	--destination-port "${pcapfilter_port}" > "${HUATUO_BAMAI_TEST_TMPDIR}/connect.log" 2>&1 &
pcapfilter_connect_pid=$!

for tool in dropwatch tcpshark; do
	if [[ ${tool} == dropwatch ]]; then
		bpf_tool_setup dropwatch net_dropwatch pcapfilter-dropwatch
	else
		bpf_tool_setup tcpshark tcp_retransmit pcapfilter-tcpshark
	fi
	capture_case "${tool}" positive "tcp and dst port ${pcapfilter_port}" TCP
	capture_case "${tool}" qualified-host "tcp src host ${PCAPFILTER_SOURCE}" TCP
	capture_case "${tool}" qualified-net "tcp src net 127.0.0.0/8" TCP
	if [[ ${tool} == dropwatch ]]; then
		capture_case "${tool}" qualified-udp-host "udp src host ${PCAPFILTER_SOURCE}" UDP
		capture_case "${tool}" qualified-udp-net "udp src net 127.0.0.0/8" UDP
	fi

	capture_case "${tool}" grouped "not arp and tcp and (dst port ${pcapfilter_port})" TCP
	capture_case "${tool}" grouped-host "(tcp and src host ${PCAPFILTER_SOURCE} and dst port ${pcapfilter_port}) or (tcp and dst port 443)" TCP
	capture_case "${tool}" negated-alternatives "tcp and not (port 80 or port 443)" TCP
	capture_case "${tool}" excluded "tcp and not (dst port ${pcapfilter_port} or port 443)" none
	capture_case "${tool}" conjunction "arp and tcp" none
	capture_case "${tool}" grouped-conjunction "(arp or tcp) and udp" none
	capture_case "${tool}" recovery "tcp and dst port ${pcapfilter_port}" TCP
done

pcapfilter_status=0
stop_and_wait_by_pid "${pcapfilter_packet_pid}" || pcapfilter_status=$?
pcapfilter_packet_pid=""
assert_eq "${pcapfilter_status}" 0 "packet workload exit status" || fatal "packet workload failed"
pcapfilter_status=0
stop_and_wait_by_pid "${pcapfilter_connect_pid}" || pcapfilter_status=$?
pcapfilter_connect_pid=""
assert_eq "${pcapfilter_status}" 0 "TCP workload exit status" || fatal "TCP workload failed"

# A dedicated IPv6 flow exercises native raw-IP port offsets in both commands.
pcapfilter_source=${PCAPFILTER_IPV6_SOURCE}
pcapfilter_destination=${PCAPFILTER_IPV6_DESTINATION}
ip -n "${NETNS}" -6 addr add "${pcapfilter_source}/128" dev lo nodad
ip -n "${NETNS}" -6 addr add "${pcapfilter_destination}/128" dev lo nodad
ip netns exec "${NETNS}" ip6tables -A INPUT -i lo -p tcp \
	-s "${pcapfilter_source}" -d "${pcapfilter_destination}" --dport "${pcapfilter_port}" -j DROP \
	|| skip "IPv6 TCP netfilter DROP rules are unavailable"
ip netns exec "${NETNS}" python3 "${ROOT_DIR}/integration/testdata/test_packet_sender.py" tcp-connect \
	--source "${pcapfilter_source}" --destination "${pcapfilter_destination}" \
	--destination-port "${pcapfilter_port}" > "${HUATUO_BAMAI_TEST_TMPDIR}/connect6.log" 2>&1 &
pcapfilter_connect_pid=$!
for tool in dropwatch tcpshark; do
	if [[ ${tool} == dropwatch ]]; then
		bpf_tool_setup dropwatch net_dropwatch pcapfilter-dropwatch6
	else
		bpf_tool_setup tcpshark tcp_retransmit pcapfilter-tcpshark6
	fi
	capture_case "${tool}" ipv6 "ip6 and tcp port ${pcapfilter_port}" TCP
	capture_case "${tool}" ipv6-qualified-host "tcp src host ${pcapfilter_source}" TCP
	capture_case "${tool}" ipv6-qualified-net "tcp src net 2001:db8:465::/64" TCP
	capture_case "${tool}" ipv4-excluded "ip and tcp port ${pcapfilter_port}" none
	capture_case "${tool}" ipv6-recovery "ip6 and tcp port ${pcapfilter_port}" TCP
done
pcapfilter_status=0
stop_and_wait_by_pid "${pcapfilter_connect_pid}" || pcapfilter_status=$?
pcapfilter_connect_pid=""
assert_eq "${pcapfilter_status}" 0 "IPv6 TCP workload exit status" || fatal "IPv6 TCP workload failed"
