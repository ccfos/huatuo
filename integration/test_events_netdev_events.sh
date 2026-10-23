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

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"

require_commands ip jq curl awk ethtool
[[ ${EUID} -eq 0 ]] || skip "veth creation requires root and CAP_NET_ADMIN"

NETDEV_TEST_DEVICE="ht_netdev_evt_t"
NETDEV_TEST_PEER="ht_netdev_p_t"
EVENTS_FILE="${HUATUO_BAMAI_TEST_TMPDIR}/events/netdev_events"
METRICS_FILE="${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt"
pair_created=false

cleanup() {
	if [[ ${pair_created} == true ]]; then
		ip link delete dev "${NETDEV_TEST_DEVICE}"
	fi
}
trap cleanup EXIT

for device in "${NETDEV_TEST_DEVICE}" "${NETDEV_TEST_PEER}"; do
	((${#device} <= 15)) || fatal "interface name ${device} exceeds the 15-character limit"
	if ip link show dev "${device}" > /dev/null 2>&1; then
		fatal "interface ${device} already exists; remove the conflict or stop the concurrent test"
	fi
done

if ! ip link add "${NETDEV_TEST_DEVICE}" type veth peer name "${NETDEV_TEST_PEER}" \
	2> "${HUATUO_BAMAI_TEST_TMPDIR}/veth-create.err"; then
	skip "cannot create veth pair; check CAP_NET_ADMIN and kernel veth support: $(< "${HUATUO_BAMAI_TEST_TMPDIR}/veth-create.err")"
fi
pair_created=true
# NetworkManager may otherwise activate the new veth and replace its MAC.
# These devices belong to this test and disappear during cleanup.
if command -v nmcli > /dev/null 2>&1 \
	&& nmcli -t -f RUNNING general 2> /dev/null | grep -qx running; then
	for device in "${NETDEV_TEST_DEVICE}" "${NETDEV_TEST_PEER}"; do
		wait_until 10 0.5 nmcli device set "${device}" managed no \
			|| fatal "cannot disable NetworkManager management for ${device}"
	done
fi
ethtool -i "${NETDEV_TEST_DEVICE}" > "${HUATUO_BAMAI_TEST_TMPDIR}/driver.txt" \
	|| skip "ethtool driver information is unavailable for ${NETDEV_TEST_DEVICE}"
ip link set dev "${NETDEV_TEST_DEVICE}" up
ip link set dev "${NETDEV_TEST_PEER}" up
ip -j link show dev "${NETDEV_TEST_DEVICE}" > "${HUATUO_BAMAI_TEST_TMPDIR}/interface.json"
device_index=$(jq -r '.[0].ifindex' "${HUATUO_BAMAI_TEST_TMPDIR}/interface.json")
device_mac=$(jq -r '.[0].address' "${HUATUO_BAMAI_TEST_TMPDIR}/interface.json")

integration_huatuo_bamai_start write_netdev_events_config \
	--region dev \
	--procfs-prefix "${HUATUO_BAMAI_TEST_FIXTURES}" \
	--disable-kubelet

# HTTP readiness precedes netlink subscription; probe only our pair until an event arrives.
netdev_subscription_ready() {
	if [[ -s "${EVENTS_FILE}" ]] && jq -se --arg device "${NETDEV_TEST_DEVICE}" '
		any(.[]; .tracer_data.ifname == $device
			and .tracer_data.linkstatus == "linkstatus_carrierup")
	' "${EVENTS_FILE}" > /dev/null; then
		return 0
	fi
	ip link set dev "${NETDEV_TEST_PEER}" down
	ip link set dev "${NETDEV_TEST_PEER}" up
	return 1
}
wait_until 30 0.5 netdev_subscription_ready \
	|| fatal "netdev_events did not observe ${NETDEV_TEST_DEVICE}; see huatuo.log and driver.txt"

netdev_has_event() {
	local offset=$1 status=$2 carrier_only=$3
	jq -se --argjson offset "${offset}" --arg status "${status}" \
		--argjson carrier_only "${carrier_only}" '
		.[$offset:] | any(.[];
			(.tracer_data.linkstatus | split(", ")) as $statuses
			| ($statuses | index($status)) != null)
		and (if $carrier_only then
			all(.[]; (.tracer_data.linkstatus | split(", ")) == [$status])
		else true end)
	' "${EVENTS_FILE}" > /dev/null 2>&1
}

netdev_metric_value() {
	local status=$1
	awk -v name="huatuo_bamai_netdev_events_${status}_total" \
		-v device="device=\"${NETDEV_TEST_DEVICE}\"" '
		index($1, name "{") == 1 && index($1, device) { value = $2 }
		END { print value + 0 }
	' "${METRICS_FILE}"
}

netdev_metric_increased() {
	local status=$1 previous=$2 current
	huatuo_bamai_collect_metrics || return 1
	current=$(netdev_metric_value "${status}")
	awk -v current="${current}" -v previous="${previous}" 'BEGIN { exit !(current > previous) }'
}

netdev_transition() {
	local device=$1 state=$2 status=$3 carrier_only=${4:-false}
	local offset previous carrier_status
	offset=$(jq -s 'length' "${EVENTS_FILE}")
	huatuo_bamai_collect_metrics
	previous=$(netdev_metric_value "${status}")
	ip link set dev "${device}" "${state}"
	wait_until 30 0.5 netdev_has_event "${offset}" "${status}" "${carrier_only}" \
		|| fatal "${device} ${state}: no new ${status} event in ${EVENTS_FILE}"
	# Veth carrier notification may arrive separately from the admin notification.
	# Drain both before the next step so it cannot consume the previous transition.
	if [[ ${device} == "${NETDEV_TEST_DEVICE}" ]]; then
		case ${state} in
		up) carrier_status=linkstatus_carrierup ;;
		down) carrier_status=linkstatus_carrierdown ;;
		esac
		wait_until 30 0.5 netdev_has_event "${offset}" "${carrier_status}" false \
			|| fatal "${device} ${state}: no new ${carrier_status} event in ${EVENTS_FILE}"
	fi
	wait_until 30 0.5 netdev_metric_increased "${status}" "${previous}" \
		|| fatal "${device} ${state}: ${status} counter did not increase from ${previous}"
}

netdev_transition "${NETDEV_TEST_DEVICE}" down linkstatus_admindown
netdev_transition "${NETDEV_TEST_DEVICE}" up linkstatus_adminup
netdev_transition "${NETDEV_TEST_PEER}" down linkstatus_carrierdown true
netdev_transition "${NETDEV_TEST_PEER}" up linkstatus_carrierup true

assert_huatuo_bamai_shutdown

jq -se --arg device "${NETDEV_TEST_DEVICE}" --argjson index "${device_index}" \
	--arg mac "${device_mac}" '
	length > 0 and all(.[];
		.tracer_name == "netdev_events"
		and (.tracer_data |
			.ifname == $device and .index == $index and .mac == $mac
			and .start == false and .driver == "veth"
			and (.driver_version | type == "string")
			and (.firmware_version | type == "string")
			and (.linkstatus | split(", ") | length > 0 and all(.[];
				. == "linkstatus_adminup" or . == "linkstatus_admindown"
				or . == "linkstatus_carrierup" or . == "linkstatus_carrierdown"))))
' "${EVENTS_FILE}" > /dev/null \
	|| fatal "invalid netdev_events record; expected device=${NETDEV_TEST_DEVICE} index=${device_index} mac=${device_mac} driver=veth start=false; see ${EVENTS_FILE}"

if grep '^huatuo_bamai_netdev_events_linkstatus_' "${METRICS_FILE}" \
	| grep -vF "device=\"${NETDEV_TEST_DEVICE}\""; then
	fatal "netdev_events exported counters for an interface outside DeviceList"
fi
huatuo_bamai_log_check || fatal "unexpected failure in huatuo.log"
log_info "netdev_events passed: admin/carrier transitions, event metadata, counters and DeviceList filtering"
