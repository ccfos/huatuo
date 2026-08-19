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

readonly NETDEVSIM_BUS="/sys/bus/netdevsim"
readonly DROPWATCH_DURATION=10

DROPWATCH_PID=""
NETDEVSIM_ID=""
DEVLINK_DEVICE=""
NETDEV=""
TRAP_NAME=""
TRAP_GROUP=""

cleanup() {
	[[ -n "${DROPWATCH_PID}" ]] && stop_by_pid "${DROPWATCH_PID}" 2 || true
	[[ -n "${NETDEV}" ]] && ip link set dev "${NETDEV}" down 2> /dev/null || true
	if [[ -n "${NETDEVSIM_ID}" && -w "${NETDEVSIM_BUS}/del_device" ]]; then
		printf '%s\n' "${NETDEVSIM_ID}" > "${NETDEVSIM_BUS}/del_device" 2> /dev/null || true
	fi
}
trap cleanup EXIT

for command in devlink ip jq modprobe; do
	command -v "${command}" > /dev/null 2>&1 || skip "${command} command is not installed"
done
tracepoint_available devlink devlink_trap_report \
	|| skip "devlink/devlink_trap_report tracepoint is not available"

bpf_tool_setup dropwatch
modprobe netdevsim > "${TOOL_WORK_DIR}/modprobe.out" 2> "${TOOL_WORK_DIR}/modprobe.err" \
	|| skip "netdevsim kernel module is unavailable"
[[ -w "${NETDEVSIM_BUS}/new_device" ]] || skip "netdevsim new_device is not writable"

for ((attempt = 0; attempt < 20; attempt++)); do
	candidate=$((10000 + RANDOM % 50000))
	if [[ ! -e "${NETDEVSIM_BUS}/devices/netdevsim${candidate}" ]]; then
		NETDEVSIM_ID=${candidate}
		break
	fi
done
[[ -n "${NETDEVSIM_ID}" ]] || fatal "failed to allocate a netdevsim device ID"

create_log="${TOOL_WORK_DIR}/netdevsim-create.err"
if ! { printf '%s 1 1\n' "${NETDEVSIM_ID}" > "${NETDEVSIM_BUS}/new_device"; } 2> "${create_log}"; then
	if ! { printf '%s 1\n' "${NETDEVSIM_ID}" > "${NETDEVSIM_BUS}/new_device"; } 2>> "${create_log}"; then
		skip "kernel rejected netdevsim device creation"
	fi
fi

DEVLINK_DEVICE="netdevsim/netdevsim${NETDEVSIM_ID}"
devlink_device_ready() {
	devlink dev show "${DEVLINK_DEVICE}" > /dev/null 2>&1
}
wait_until 5 1 devlink_device_ready \
	|| fatal "netdevsim devlink device did not appear: ${DEVLINK_DEVICE}"

NETDEV=$(devlink -j port show | jq -er --arg prefix "${DEVLINK_DEVICE}/" '
  [.. | objects | to_entries[]?
   | select(.key | startswith($prefix))
   | .value.netdev?][0]
') \
	|| fatal "netdevsim port has no associated netdev"

trap_fields=$(devlink -j trap show "${DEVLINK_DEVICE}" | jq -er '
  [.. | objects | to_entries[]?
   | select(.value.type? == "drop")
   | [.key, .value.group] | @tsv][0]
') || skip "netdevsim registered no drop trap"
IFS=$'\t' read -r TRAP_NAME TRAP_GROUP <<< "${trap_fields}"
[[ -n "${TRAP_NAME}" && -n "${TRAP_GROUP}" ]] \
	|| fatal "netdevsim drop trap is missing its name or group"

"${TOOL_BIN}" \
	--bpf-path "${TOOL_BPF}" \
	--duration "${DROPWATCH_DURATION}" \
	--output json \
	> "${TOOL_OUT}" 2> "${TOOL_ERR}" &
DROPWATCH_PID=$!
sleep 0.5
kill -0 "${DROPWATCH_PID}" 2> /dev/null \
	|| fatal "dropwatch exited before netdevsim trap configuration"

devlink trap set "${DEVLINK_DEVICE}" trap "${TRAP_NAME}" action trap
ip link set dev "${NETDEV}" up

hardware_event_ready() {
	kill -0 "${DROPWATCH_PID}" 2> /dev/null \
		|| fatal "dropwatch exited before capturing the netdevsim hardware drop"
	jq -e -s --arg trap "${TRAP_NAME}" --arg group "${TRAP_GROUP}" --arg dev "${NETDEV}" '
    any(.[];
      .drop_source == "hardware" and
      .drop_reason == $trap and
      .drop_reason_group == $group and
      .drop_location == null and
      .netdev_name == $dev and
      .netdev_ifindex > 0 and
      (.packet_skb_addr | startswith("0x")) and
      .packet_eth_proto == "0x800" and
      .packet_len > 0 and
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

assert_log_has_no_failure "${TOOL_ERR}" "dropwatch"
log_info "dropwatch captured netdevsim hardware trap: ${TRAP_GROUP}/${TRAP_NAME} on ${NETDEV}"
