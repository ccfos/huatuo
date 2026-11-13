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

# Completion predicates shared by live captures and local regression fixtures.
# Polling must tolerate incomplete captures while events are still arriving.
# Final assertions reuse the predicates after shutdown and report failures.

ipv4_options_packet_matches() {
	local protocol=$1 ihl=$2 length=$3
	jq -ces \
		--arg source "${IPV4_OPTIONS_SOURCE}" \
		--arg destination "${IPV4_OPTIONS_DESTINATION}" \
		--arg protocol "${protocol}" \
		--argjson source_port "${ipv4_options_source_port}" \
		--argjson destination_port "${ipv4_options_destination_port}" \
		--argjson ihl "${ihl}" --argjson length "${length}" '
  first(.[]
    | select(.drop_source == "software" and .netdev_name == "lo")
    | select(.layers.ipv4 |
        .version == 4 and .ihl == $ihl and .len == $length and
        .tos == 16 and .id == 4885 and .flags == "DF" and .ttl == 64 and
        .saddr == $source and .daddr == $destination and .protocol == $protocol)
    | select(.layers.label == ("IPv4/" + $protocol))
    | select(.layers[$protocol | ascii_downcase] |
        .sport == $source_port and .dport == $destination_port)
    | select(if $protocol == "TCP" then
        .layers.tcp |
        .flags == "SYN" and .seq == 1 and .data_offset == 5
      else
        .layers.udp.len == 8
      end))
 ' "${TOOL_OUT}"
}

ipv4_options_capture_is_complete() {
	ipv4_options_packet_matches TCP 6 44 > /dev/null 2>&1 \
		&& ipv4_options_packet_matches UDP 6 32 > /dev/null 2>&1 \
		&& ipv4_options_packet_matches TCP 15 80 > /dev/null 2>&1
}

assert_ipv4_options_capture() {
	ipv4_options_capture_is_complete || fatal "invalid final IPv4 option layouts; see ${TOOL_OUT}"
}

icmpv6_json_matches() {
	local out=$1 typ=$2 source=$3 destination=$4 identifier=$5 sequence=$6
	jq -ces --arg typ "${typ}" --arg src "${source}" --arg dst "${destination}" \
		--argjson id "${identifier}" --argjson seq "${sequence}" '
	  first(.[] | select(
	    .layers.label == "IPv6/ICMPv6" and .layers.icmp.type == $typ and
	    .layers.ipv6.saddr == $src and .layers.ipv6.daddr == $dst and
	    (if $id == 0 then (.layers.icmp | has("id") | not) else .layers.icmp.id == $id end) and
	    (if $seq == 0 then (.layers.icmp | has("seq") | not) else .layers.icmp.seq == $seq end)))
	' "${out}"
}

icmpv6_text_matches() {
	local out=$1 typ=$2 source=$3 destination=$4 identifier=$5 sequence=$6
	local expected=" ${source} > ${destination} type=${typ} id=${identifier} seq=${sequence} "
	awk -v expected="${expected}" '
	  index($0, " IPv6/ICMPv6 ") && index($0, expected) { print; found = 1; exit }
	  END { exit !found }
	' "${out}"
}

assert_icmpv6_echo_identity() {
	local output=$1 out=$2
	shift 2
	"icmpv6_${output}_matches" "${out}" "$@" \
		|| fatal "invalid final ${output} Echo identity; see ${out}"
}

fast_retransmit_is_stored() {
	jq -se --argjson port "${TEST_PORT}" '
		any(.[]; .tcp_sport == $port and .tcp_reason == "fast_retransmit")
	' "${OUTPUT_DIR}/events.json" > /dev/null 2>&1
}

net_rx_latency_event_is_stored() {
	[[ -s "${EVENTS_FILE}" ]] || return 1
	MATCHED=$(jq -s --arg saddr "${NETNS_CLIENT_ADDR}" --arg daddr "${NETNS_SERVER_ADDR}" \
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
		"${EVENTS_FILE}" 2> /dev/null) || return 1
	jq -e 'length > 0' <<< "${MATCHED}" > /dev/null
}
