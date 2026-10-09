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

# Verify Echo identities through loopback IPv6 drops and the dropwatch writers.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_namespace.sh"

readonly ICMPV6_SERVER_ADDR="2001:db8:1193::1"
readonly ICMPV6_CLIENT_ADDR="2001:db8:1193::2"
readonly ICMPV6_DURATION_SECONDS=5
readonly ICMPV6_TIMEOUT_SECONDS=20

require_commands ip ip6tables python3 jq awk timeout
require_readable /proc/net/if_inet6

icmpv6_dropwatch_pid=""
icmpv6_traffic_pid=""

cleanup() {
	stop_and_wait_by_pid "${icmpv6_traffic_pid}" || true
	stop_and_wait_by_pid "${icmpv6_dropwatch_pid}" || true
	namespace_cleanup
}
trap cleanup EXIT

send_echo() {
	# Keep the background PID attached to the sender so cleanup can terminate it.
	exec ip netns exec "${NETNS}" python3 - "${ICMPV6_CLIENT_ADDR}" "${ICMPV6_SERVER_ADDR}" "$1" "$2" << 'PY'
import signal
import socket
import struct
import sys
import time

signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))
source, destination = sys.argv[1:3]
identifier, sequence = int(sys.argv[3]), int(sys.argv[4])
packet = struct.pack("!BBHHH", 128, 0, 0, identifier, sequence) + b"huatuo"
# Linux computes the mandatory checksum for IPPROTO_ICMPV6 raw sockets.
with socket.socket(socket.AF_INET6, socket.SOCK_RAW, socket.IPPROTO_ICMPV6) as sock:
    sock.bind((source, 0))
    while True:
        sock.sendto(packet, (destination, 0))
        time.sleep(0.1)
PY
}

assert_icmpv6_json() {
	local out=$1 typ=$2 source=$3 destination=$4 identifier=$5 sequence=$6
	jq -ces --arg typ "${typ}" --arg src "${source}" --arg dst "${destination}" \
		--argjson id "${identifier}" --argjson seq "${sequence}" '
	  first(.[] | select(
	    .layers.label == "IPv6/ICMPv6" and .layers.icmp.type == $typ and
	    .layers.ipv6.saddr == $src and .layers.ipv6.daddr == $dst and
	    (if $id == 0 then (.layers.icmp | has("id") | not) else .layers.icmp.id == $id end) and
	    (if $seq == 0 then (.layers.icmp | has("seq") | not) else .layers.icmp.seq == $seq end)))
	' "${out}" || fatal "expected ${typ} ${source} > ${destination} id=${identifier} seq=${sequence} with JSON zero omission; see ${out}"
	assert_kernel_observation_timestamps "${out}"
}

assert_icmpv6_text() {
	local out=$1 typ=$2 source=$3 destination=$4 identifier=$5 sequence=$6
	local expected=" ${source} > ${destination} type=${typ} id=${identifier} seq=${sequence} "
	awk -v expected="${expected}" '
	  index($0, " IPv6/ICMPv6 ") && index($0, expected) { print; found = 1; exit }
	  END { exit !found }
	' "${out}" || fatal "expected ${typ} ${source} > ${destination} id=${identifier} seq=${sequence}; see ${out}"
}

run_case() {
	local name=$1 typ=$2 identifier=$3 sequence=$4 output=$5
	local out="${TOOL_WORK_DIR}/${name}.${output}"
	local err="${TOOL_WORK_DIR}/${name}.err"
	local traffic_log="${TOOL_WORK_DIR}/${name}-traffic.log"
	local source=${ICMPV6_CLIENT_ADDR} destination=${ICMPV6_SERVER_ADDR}
	local rule_type=echo-request
	local dropwatch_status=0 traffic_status=0

	if [[ ${typ} == EchoReply ]]; then
		source=${ICMPV6_SERVER_ADDR}
		destination=${ICMPV6_CLIENT_ADDR}
		rule_type=echo-reply
	fi
	local rule=(-i lo -s "${source}" -d "${destination}" -p ipv6-icmp --icmpv6-type "${rule_type}" -j DROP)
	ip netns exec "${NETNS}" ip6tables -I INPUT 1 "${rule[@]}" \
		|| skip "IPv6 ICMP netfilter rules are unavailable"

	# Select the isolated test flow in the output to exercise the decoder/writer path.
	timeout --kill-after=5s "${ICMPV6_TIMEOUT_SECONDS}s" \
		"${TOOL_BIN}" --bpf-path "${TOOL_BPF}" --duration "${ICMPV6_DURATION_SECONDS}" --output "${output}" \
		> "${out}" 2> "${err}" &
	icmpv6_dropwatch_pid=$!
	# Keep traffic active across BPF startup and the complete capture window.
	send_echo "${identifier}" "${sequence}" > "${traffic_log}" 2>&1 &
	icmpv6_traffic_pid=$!
	wait "${icmpv6_dropwatch_pid}" || dropwatch_status=$?
	icmpv6_dropwatch_pid=""
	stop_and_wait_by_pid "${icmpv6_traffic_pid}" || traffic_status=$?
	icmpv6_traffic_pid=""
	ip netns exec "${NETNS}" ip6tables -D INPUT "${rule[@]}"

	if [[ ${output} == json ]]; then
		assert_icmpv6_json "${out}" "${typ}" "${source}" "${destination}" "${identifier}" "${sequence}"
	else
		assert_icmpv6_text "${out}" "${typ}" "${source}" "${destination}" "${identifier}" "${sequence}"
	fi

	assert_log_has_no_failure "${err}" dropwatch
	log_info "${name}: ${output} Echo identity verified"
}

bpf_tool_setup dropwatch net_dropwatch dropwatch-icmpv6
namespace_setup "dw6_${BASHPID}"
ip -n "${NETNS}" link set lo up
# Both endpoints are local to this namespace, so no neighbor discovery is needed.
ip -n "${NETNS}" -6 addr add "${ICMPV6_SERVER_ADDR}/128" dev lo nodad
ip -n "${NETNS}" -6 addr add "${ICMPV6_CLIENT_ADDR}/128" dev lo nodad

run_case request-json EchoRequest 4660 7 json
run_case reply-json EchoReply 65535 65535 json
run_case zero-json EchoRequest 0 0 json
run_case reply-text EchoReply 4660 7 text
