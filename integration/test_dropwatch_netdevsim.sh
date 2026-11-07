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

# Verify hardware drop metadata and packet decoding with netdevsim. A running
# netdevsim port reports synthetic IPv4/UDP packets for traps whose action is
# "trap", which makes the event source deterministic without physical NICs.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_net.sh"

readonly DROPWATCH_DURATION=10

require_commands ip
tracepoint_available devlink devlink_trap_report \
	|| skip "devlink/devlink_trap_report tracepoint is not available"

dropwatch_pid=""
dropwatch_trap_name=""
dropwatch_trap_group=""

cleanup() {
	local status=0
	if [[ -n ${dropwatch_pid} ]]; then
		stop_by_pid "${dropwatch_pid}" 2 || status=1
		if ! wait_until 2 0.1 process_exited "${dropwatch_pid}"; then
			log_error "dropwatch pid=${dropwatch_pid} is still running; retaining test device"
			return 1
		fi
		dropwatch_pid=""
	fi
	netdevsim_destroy || status=1
	return "${status}"
}
trap 'dropwatch_exit_status=$?
if ! cleanup && [[ ${dropwatch_exit_status} == 0 || ${dropwatch_exit_status} == 77 ]]; then
	dropwatch_exit_status=1
fi
exit "${dropwatch_exit_status}"' EXIT

dropwatch_event_ready() {
	kill -0 "${dropwatch_pid}" 2> /dev/null \
		|| fatal "dropwatch exited before capturing the netdevsim hardware drop"
	jq -e -s --arg trap "${dropwatch_trap_name}" --arg group "${dropwatch_trap_group}" --arg dev "${NETDEVSIM_NETDEV}" '
    any(.[];
      .drop_source == "hardware" and
      .drop_reason == $trap and
      .drop_reason_group == $group and
      .drop_location == null and
      .netdev_name == $dev and
      .netdev_ifindex > 0 and
      (.packet_skb_addr | startswith("0x")) and
      .packet_eth_proto == "0x800" and
      .packet_len_bytes > 0 and
      .layers.label == "IPv4/UDP" and
      .layers.ipv4.saddr == "192.0.2.1" and
      .layers.ipv4.daddr == "198.51.100.1")
  ' "${TOOL_OUT}" > /dev/null 2>&1
}

bpf_tool_setup dropwatch net_dropwatch
netdevsim_create "${TOOL_WORK_DIR}"

devlink -j trap show > "${TOOL_WORK_DIR}/devlink-traps.json" \
	2> "${TOOL_WORK_DIR}/devlink-traps.err" \
	|| fatal "cannot query devlink drop traps; see ${TOOL_WORK_DIR}/devlink-traps.err"
dropwatch_trap_fields=$(jq -r --arg device "${NETDEVSIM_DEVICE}" '
	if (.trap | type) != "object" or ((.trap[$device] // []) | type) != "array" then
		error("invalid devlink trap response")
	else
		first(.trap[$device][]? | select(.type == "drop") | [.name, .group] | @tsv) // empty
	end
' "${TOOL_WORK_DIR}/devlink-traps.json" 2>> "${TOOL_WORK_DIR}/devlink-traps.err") \
	|| fatal "cannot parse devlink drop traps; see ${TOOL_WORK_DIR}/devlink-traps.json and devlink-traps.err"

[[ -n "${dropwatch_trap_fields}" ]] || skip "netdevsim registered no drop trap"
IFS=$'\t' read -r dropwatch_trap_name dropwatch_trap_group <<< "${dropwatch_trap_fields}"
[[ -n "${dropwatch_trap_name}" && -n "${dropwatch_trap_group}" ]] \
	|| fatal "netdevsim drop trap is missing its name or group"

"${TOOL_BIN}" \
	--bpf-path "${TOOL_BPF}" \
	--duration "${DROPWATCH_DURATION}" \
	--output json \
	> "${TOOL_OUT}" 2> "${TOOL_ERR}" &
dropwatch_pid=$!

# Reports repeat while the port is up; capture polling also covers BPF startup.
devlink trap set "${NETDEVSIM_DEVICE}" trap "${dropwatch_trap_name}" action trap
ip link set dev "${NETDEVSIM_NETDEV}" up

wait_until 8 0.5 dropwatch_event_ready \
	|| fatal "dropwatch did not capture a valid netdevsim hardware drop"

assert_process_shutdown_by_pid "${dropwatch_pid}" 2 \
	|| fatal "dropwatch failed to stop after capturing the netdevsim hardware drop"
dropwatch_pid=""

assert_kernel_observation_timestamps "${TOOL_OUT}"
assert_log_has_no_failure "${TOOL_ERR}" "dropwatch"
log_info "dropwatch captured netdevsim hardware trap: ${dropwatch_trap_group}/${dropwatch_trap_name} on ${NETDEVSIM_NETDEV}"
