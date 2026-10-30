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

DROPWATCH_PID=""
TRAP_NAME=""
TRAP_GROUP=""

cleanup() {
	local status=0
	if [[ -n ${DROPWATCH_PID} ]]; then
		stop_by_pid "${DROPWATCH_PID}" 2 || status=1
		if ! wait_until 2 0.1 process_exited "${DROPWATCH_PID}"; then
			log_error "dropwatch pid=${DROPWATCH_PID} is still running; retaining test device"
			return 1
		fi
	fi
	netdevsim_destroy || status=1
	return "${status}"
}
trap 'dropwatch_exit_status=$?
if ! cleanup && [[ ${dropwatch_exit_status} == 0 || ${dropwatch_exit_status} == 77 ]]; then
	dropwatch_exit_status=1
fi
exit "${dropwatch_exit_status}"' EXIT

require_commands ip
tracepoint_available devlink devlink_trap_report \
	|| skip "devlink/devlink_trap_report tracepoint is not available"

bpf_tool_setup dropwatch net_dropwatch
netdevsim_create "${TOOL_WORK_DIR}"

devlink -j trap show > "${TOOL_WORK_DIR}/devlink-traps.json" \
	2> "${TOOL_WORK_DIR}/devlink-traps.err" \
	|| fatal "cannot query devlink traps; see ${TOOL_WORK_DIR}/devlink-traps.err"
trap_fields=$(jq -r --arg device "${NETDEVSIM_DEVICE}" '
  if (.trap | type) != "object" or ((.trap[$device] // []) | type) != "array" then
    error("invalid devlink trap response")
  else
    [.trap[$device][]? | select(.type == "drop") | [.name, .group] | @tsv][0] // empty
  end
' "${TOOL_WORK_DIR}/devlink-traps.json") \
	|| fatal "invalid devlink traps; see ${TOOL_WORK_DIR}/devlink-traps.json"
[[ -n "${trap_fields}" ]] || skip "netdevsim registered no drop trap"
IFS=$'\t' read -r TRAP_NAME TRAP_GROUP <<< "${trap_fields}"
[[ -n "${TRAP_NAME}" && -n "${TRAP_GROUP}" ]] \
	|| fatal "netdevsim drop trap is missing its name or group"

"${TOOL_BIN}" \
	--bpf-path "${TOOL_BPF}" \
	--duration "${DROPWATCH_DURATION}" \
	--output json \
	> "${TOOL_OUT}" 2> "${TOOL_ERR}" &
DROPWATCH_PID=$!

# Reports repeat while the port is up; capture polling also covers BPF startup.
devlink trap set "${NETDEVSIM_DEVICE}" trap "${TRAP_NAME}" action trap
ip link set dev "${NETDEVSIM_NETDEV}" up

hardware_event_ready() {
	kill -0 "${DROPWATCH_PID}" 2> /dev/null \
		|| fatal "dropwatch exited before capturing the netdevsim hardware drop"
	jq -e -s --arg trap "${TRAP_NAME}" --arg group "${TRAP_GROUP}" --arg dev "${NETDEVSIM_NETDEV}" '
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
wait_until 8 0.5 hardware_event_ready \
	|| fatal "dropwatch did not capture a valid netdevsim hardware drop"

kill -TERM "${DROPWATCH_PID}" 2> /dev/null || true
if ! wait "${DROPWATCH_PID}"; then
	DROPWATCH_PID=""
	fatal "dropwatch failed while capturing the netdevsim hardware drop"
fi
DROPWATCH_PID=""

assert_kernel_observation_timestamps "${TOOL_OUT}"
assert_log_has_no_failure "${TOOL_ERR}" "dropwatch"
log_info "dropwatch captured netdevsim hardware trap: ${TRAP_GROUP}/${TRAP_NAME} on ${NETDEVSIM_NETDEV}"
