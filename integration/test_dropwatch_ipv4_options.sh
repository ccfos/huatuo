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

# Verify IPv4 options through real netfilter drops and dropwatch JSON output.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_namespace.sh"

readonly IPV4_OPTIONS_SOURCE="127.0.0.2"
readonly IPV4_OPTIONS_WARMUP_SOURCE="127.0.0.3"
readonly IPV4_OPTIONS_DESTINATION="127.0.0.1"
readonly IPV4_OPTIONS_DURATION_SECONDS=5
readonly IPV4_OPTIONS_TIMEOUT_SECONDS=20

require_commands ip iptables python3 jq timeout ss

ipv4_options_source_port=$(allocate_available_port) || fatal "cannot allocate source port"
ipv4_options_destination_port=$(allocate_available_port) || fatal "cannot allocate destination port"
ipv4_options_dropwatch_pid=""
ipv4_options_traffic_pid=""

cleanup() {
	stop_and_wait_by_pid "${ipv4_options_traffic_pid}" || true
	stop_and_wait_by_pid "${ipv4_options_dropwatch_pid}" || true
	namespace_cleanup
}
trap cleanup EXIT

# JSON exposes IHL, not option bytes. Check the option-bearing header length
# and decoded transport fields to catch early returns at EOL in the decoder.
assert_ipv4_options_packet() {
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
 ' "${TOOL_OUT}" || fatal "expected IPv4/${protocol} IHL=${ihl} TTL=64 and matching addresses/ports; see ${TOOL_OUT}"
}

bpf_tool_setup dropwatch net_dropwatch dropwatch-ipv4-options
namespace_setup "dw4_${BASHPID}"
ip -n "${NETNS}" link set lo up
for protocol in tcp udp; do
	ip netns exec "${NETNS}" iptables -A INPUT -i lo \
		-d "${IPV4_OPTIONS_DESTINATION}" -p "${protocol}" \
		--dport "${ipv4_options_destination_port}" -j DROP \
		|| skip "IPv4 ${protocol} netfilter DROP rules are unavailable"
done

# Traffic spans BPF startup; no fixed startup delay is needed. The capture filter
# includes warm-up frames so decoder reuse cannot hide the original EOL defect.
timeout --kill-after=5s "${IPV4_OPTIONS_TIMEOUT_SECONDS}s" \
	"${TOOL_BIN}" --bpf-path "${TOOL_BPF}" \
	--filter "ip and dst host ${IPV4_OPTIONS_DESTINATION} and dst port ${ipv4_options_destination_port}" \
	--duration "${IPV4_OPTIONS_DURATION_SECONDS}" --output json \
	> "${TOOL_OUT}" 2> "${TOOL_ERR}" &
ipv4_options_dropwatch_pid=$!

ip netns exec "${NETNS}" python3 "${ROOT_DIR}/integration/testdata/test_packet_sender.py" ipv4-options \
	--source "${IPV4_OPTIONS_SOURCE}" --warmup-source "${IPV4_OPTIONS_WARMUP_SOURCE}" \
	--destination "${IPV4_OPTIONS_DESTINATION}" \
	--source-port "${ipv4_options_source_port}" --destination-port "${ipv4_options_destination_port}" \
	> "${TOOL_WORK_DIR}/traffic.log" 2>&1 &
ipv4_options_traffic_pid=$!

ipv4_options_capture_status=0
wait "${ipv4_options_dropwatch_pid}" || ipv4_options_capture_status=$?
ipv4_options_dropwatch_pid=""
ipv4_options_traffic_status=0
stop_and_wait_by_pid "${ipv4_options_traffic_pid}" || ipv4_options_traffic_status=$?
ipv4_options_traffic_pid=""
assert_eq "${ipv4_options_capture_status}" 0 "dropwatch exit status" || fatal "dropwatch failed; see ${TOOL_ERR}"
assert_eq "${ipv4_options_traffic_status}" 0 "traffic generator exit status" || fatal "traffic generator failed; see ${TOOL_WORK_DIR}/traffic.log"

# IHL counts 32-bit words: 6 includes 4 option bytes; 15 includes 40.
assert_ipv4_options_packet TCP 6 44
assert_ipv4_options_packet UDP 6 32
assert_ipv4_options_packet TCP 15 80
assert_kernel_observation_timestamps "${TOOL_OUT}"
assert_log_has_no_failure "${TOOL_ERR}" dropwatch
log_info "IPv4 EOL options preserve fixed headers and TCP/UDP layers, including maximum IHL"
