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

# Exercise netlink state changes, local event storage and counters with a veth pair.
# Only test-owned interfaces are changed; interface discovery and deletion are not covered.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"

readonly NETDEV_DEVICE="ht_netdev_evt_t"
readonly NETDEV_PEER="ht_netdev_p_t"
readonly NETDEV_DEVICE_MAC="02:68:74:00:00:01"
readonly NETDEV_PEER_MAC="02:68:74:00:00:02"
readonly NETDEV_EVENTS_FILE="${HUATUO_BAMAI_TEST_TMPDIR}/events/netdev_events"
readonly NETDEV_METRICS_FILE="${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt"
readonly NETDEV_EVENT_ERROR_FILE="${HUATUO_BAMAI_TEST_TMPDIR}/netdev-events.err"
readonly NETDEV_METRIC_ERROR_FILE="${HUATUO_BAMAI_TEST_TMPDIR}/netdev-metrics.err"
readonly NETDEV_CLEANUP_ERROR_FILE="${HUATUO_BAMAI_TEST_TMPDIR}/netdev-cleanup.err"
readonly NETDEV_NM_ERROR_FILE="${HUATUO_BAMAI_TEST_TMPDIR}/netdev-network-manager.err"
readonly CURL_TIMEOUT=(--connect-timeout 2 --max-time 3)

require_commands ip jq curl awk ethtool ss
[[ ${EUID} -eq 0 ]] || skip "veth creation requires root and CAP_NET_ADMIN"
# Masking /var/run (usually /run) hides D-Bus, not NetworkManager itself. Preserve
# bus access so nmcli can stop it managing our veth. Private mounts cannot leak.
if [[ ! /var/run -ef "${HUATUO_BAMAI_TEST_TMPDIR}/run" && -S /run/dbus/system_bus_socket ]]; then
	require_commands unshare mount
	exec unshare --mount --propagation private bash -euc '
		mkdir -p "${HUATUO_BAMAI_TEST_TMPDIR}/system-dbus"
		mount --bind /run/dbus "${HUATUO_BAMAI_TEST_TMPDIR}/system-dbus"
		export DBUS_SYSTEM_BUS_ADDRESS="unix:path=${HUATUO_BAMAI_TEST_TMPDIR}/system-dbus/system_bus_socket"
		source "${ROOT_DIR}/integration/lib.sh"
		integration_isolate_run_directory "$@"
	' "$0" "$@"
fi
integration_isolate_run_directory "$@"

NETDEV_HTTP_PORT=$(allocate_available_port) || fatal "failed to allocate a netdev_events API port"
readonly NETDEV_HTTP_PORT
HUATUO_BAMAI_ADDR="http://127.0.0.1:${NETDEV_HTTP_PORT}"
HUATUO_BAMAI_METRICS_API="${HUATUO_BAMAI_ADDR}/metrics"

netdev_pair_created=false
netdev_device_index=""
netdev_device_mac=""
netdev_last_metric_value=""
netdev_baseline_event_count=""
netdev_baseline_metric_value=""
netdev_nm_state=unknown

netdev_cleanup() {
	[[ ${netdev_pair_created} == true ]] || return 0
	if ip link delete dev "${NETDEV_DEVICE}" 2> "${NETDEV_CLEANUP_ERROR_FILE}"; then
		netdev_pair_created=false
		return 0
	fi
	# A removed veth takes its peer with it; distinguish this from a deletion failure.
	if ip -j link show | jq -e --arg device "${NETDEV_DEVICE}" --arg peer "${NETDEV_PEER}" \
		'all(.[]; .ifname != $device and .ifname != $peer)' > /dev/null; then
		netdev_pair_created=false
		return 0
	fi
	log_error "failed to delete ${NETDEV_DEVICE}: $(< "${NETDEV_CLEANUP_ERROR_FILE}")"
	return 1
}

netdev_exit() {
	local status=$1
	trap - EXIT
	# Bash ignores a failing EXIT handler unless it explicitly sets the exit status.
	if ! netdev_cleanup; then
		if [[ ${status} -eq 0 || ${status} -eq 77 ]]; then status=1; fi
	fi
	exit "${status}"
}
trap 'netdev_exit "$?"' EXIT

netdev_query_network_manager() {
	local output status=0
	netdev_nm_state=unknown
	output=$(LC_ALL=C nmcli --wait 3 -t -f RUNNING general 2> "${NETDEV_NM_ERROR_FILE}") || status=$?
	case ${status}:${output} in
	0:running) netdev_nm_state=running ;;
	0:"not running" | 8:*) netdev_nm_state=stopped ;;
	*)
		printf 'nmcli status=%s output=%s\n' "${status}" "${output}" >> "${NETDEV_NM_ERROR_FILE}"
		return 1
		;;
	esac
}

netdev_report_event_diagnostics() {
	local count
	if [[ -s "${NETDEV_EVENT_ERROR_FILE}" ]]; then
		log_error "netdev event parsing: $(< "${NETDEV_EVENT_ERROR_FILE}")"
	fi
	count=$(jq -s 'length' "${NETDEV_EVENTS_FILE}" 2> /dev/null) || count=unreadable
	log_error "netdev event count=${count}; file=${NETDEV_EVENTS_FILE}"
	jq -cs '.[-5:] | map(.tracer_data)' "${NETDEV_EVENTS_FILE}" >&2 2> /dev/null || true
}

# HTTP readiness precedes netlink subscription; probe only our pair until an event arrives.
netdev_subscription_ready() {
	if [[ -s "${NETDEV_EVENTS_FILE}" ]] && jq -se --arg device "${NETDEV_DEVICE}" '
		any(.[]; .tracer_data.ifname == $device
			and .tracer_data.linkstatus == "linkstatus_carrierup")
	' "${NETDEV_EVENTS_FILE}" > /dev/null 2> "${NETDEV_EVENT_ERROR_FILE}"; then
		return 0
	fi
	ip link set dev "${NETDEV_PEER}" down
	ip link set dev "${NETDEV_PEER}" up
	return 1
}
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
	' "${NETDEV_EVENTS_FILE}" > /dev/null 2> "${NETDEV_EVENT_ERROR_FILE}"
}

netdev_metric_value() {
	local status=$1
	awk -v name="huatuo_bamai_netdev_events_${status}_total" \
		-v device="device=\"${NETDEV_DEVICE}\"" '
		index($1, name "{") == 1 && index($1, device) { value = $2 }
		END { print value + 0 }
	' "${NETDEV_METRICS_FILE}"
}

netdev_metric_increased() {
	local status=$1 previous=$2
	netdev_last_metric_value=unavailable
	huatuo_bamai_collect_metrics 2> "${NETDEV_METRIC_ERROR_FILE}" || return 1
	netdev_last_metric_value=$(netdev_metric_value "${status}")
	awk -v current="${netdev_last_metric_value}" -v previous="${previous}" 'BEGIN { exit !(current > previous) }'
}

netdev_capture_baseline() {
	local status=$1
	# Storage may be mid-write and the HTTP endpoint may briefly stall under VM load.
	netdev_baseline_event_count=$(jq -s 'length' "${NETDEV_EVENTS_FILE}" 2> "${NETDEV_EVENT_ERROR_FILE}") || return 1
	huatuo_bamai_collect_metrics 2> "${NETDEV_METRIC_ERROR_FILE}" || return 1
	netdev_baseline_metric_value=$(netdev_metric_value "${status}")
}

assert_netdev_transition() {
	local device=$1 state=$2 status=$3 carrier_only=${4:-false}
	local offset previous carrier_status
	if ! wait_until 30 0.5 netdev_capture_baseline "${status}"; then
		netdev_report_event_diagnostics
		fatal "cannot read baseline for ${device} ${state}; metrics error: $(cat "${NETDEV_METRIC_ERROR_FILE}" 2> /dev/null)"
	fi
	offset=${netdev_baseline_event_count}
	previous=${netdev_baseline_metric_value}
	ip link set dev "${device}" "${state}"
	if ! wait_until 30 0.5 netdev_has_event "${offset}" "${status}" "${carrier_only}"; then
		netdev_report_event_diagnostics
		fatal "${device} ${state}: no new ${status} event after offset=${offset}"
	fi
	# Veth carrier notification may arrive separately from the admin notification.
	# Drain both before the next step so it cannot consume the previous transition.
	if [[ ${device} == "${NETDEV_DEVICE}" ]]; then
		case ${state} in
		up) carrier_status=linkstatus_carrierup ;;
		down) carrier_status=linkstatus_carrierdown ;;
		esac
		if ! wait_until 30 0.5 netdev_has_event "${offset}" "${carrier_status}" false; then
			netdev_report_event_diagnostics
			fatal "${device} ${state}: no new ${carrier_status} event after offset=${offset}"
		fi
	fi
	wait_until 30 0.5 netdev_metric_increased "${status}" "${previous}" \
		|| fatal "${device} ${state}: ${status} counter previous=${previous} actual=${netdev_last_metric_value}; see ${NETDEV_METRICS_FILE} and ${NETDEV_METRIC_ERROR_FILE}"
	log_info "${device} ${state}: ${status} counter ${previous} -> ${netdev_last_metric_value}"
}

assert_netdev_event_metadata() {
	jq -se --arg device "${NETDEV_DEVICE}" --argjson index "${netdev_device_index}" \
		--arg mac "${netdev_device_mac}" '
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
	' "${NETDEV_EVENTS_FILE}" > /dev/null 2> "${NETDEV_EVENT_ERROR_FILE}" || {
		netdev_report_event_diagnostics
		fatal "invalid netdev_events record; expected device=${NETDEV_DEVICE} index=${netdev_device_index} mac=${netdev_device_mac} driver=veth start=false"
	}
}

assert_netdev_device_filter() {
	if grep '^huatuo_bamai_netdev_events_linkstatus_' "${NETDEV_METRICS_FILE}" \
		| grep -vF "device=\"${NETDEV_DEVICE}\""; then
		fatal "netdev_events exported counters for an interface outside DeviceList; see ${NETDEV_METRICS_FILE}"
	fi
}

# Main flow.
for netdev_device in "${NETDEV_DEVICE}" "${NETDEV_PEER}"; do
	((${#netdev_device} <= 15)) || fatal "interface name ${netdev_device} exceeds the 15-character limit"
	if ip link show dev "${netdev_device}" > /dev/null 2>&1; then
		fatal "interface ${netdev_device} already exists; remove the conflict or stop the concurrent test"
	fi
done

# udev may replace random veth MACs after the baseline is read, breaking metadata
# checks. Assign both at creation so its persistent-MAC policy leaves them alone.
if ! ip link add "${NETDEV_DEVICE}" address "${NETDEV_DEVICE_MAC}" \
	type veth peer name "${NETDEV_PEER}" address "${NETDEV_PEER_MAC}" \
	2> "${HUATUO_BAMAI_TEST_TMPDIR}/veth-create.err"; then
	skip "cannot create veth pair; check CAP_NET_ADMIN and kernel veth support: $(< "${HUATUO_BAMAI_TEST_TMPDIR}/veth-create.err")"
fi
netdev_pair_created=true
# NetworkManager may otherwise activate the new veth and replace its MAC.
# These devices belong to this test and disappear during cleanup.
if command -v nmcli > /dev/null 2>&1; then
	wait_until 10 0.5 netdev_query_network_manager \
		|| fatal "cannot query NetworkManager; check D-Bus access: $(< "${NETDEV_NM_ERROR_FILE}")"
	if [[ ${netdev_nm_state} == running ]]; then
		for netdev_device in "${NETDEV_DEVICE}" "${NETDEV_PEER}"; do
			wait_until 10 0.5 nmcli --wait 3 device set "${netdev_device}" managed no \
				|| fatal "cannot disable NetworkManager management for ${netdev_device}"
		done
	fi
else
	log_info "nmcli is not installed; skipping NetworkManager control"
fi
ethtool -i "${NETDEV_DEVICE}" > "${HUATUO_BAMAI_TEST_TMPDIR}/driver.txt" \
	|| skip "ethtool driver information is unavailable for ${NETDEV_DEVICE}"
ip link set dev "${NETDEV_DEVICE}" up
ip link set dev "${NETDEV_PEER}" up
ip -j link show dev "${NETDEV_DEVICE}" > "${HUATUO_BAMAI_TEST_TMPDIR}/interface.json"
netdev_device_index=$(jq -r '.[0].ifindex' "${HUATUO_BAMAI_TEST_TMPDIR}/interface.json")
netdev_device_mac=$(jq -r '.[0].address' "${HUATUO_BAMAI_TEST_TMPDIR}/interface.json")

integration_huatuo_bamai_start write_netdev_events_config \
	--region dev \
	--procfs-prefix "${HUATUO_BAMAI_TEST_FIXTURES}" \
	--disable-kubelet

if ! wait_until 30 0.5 netdev_subscription_ready; then
	netdev_report_event_diagnostics
	fatal "netdev_events did not observe ${NETDEV_DEVICE}; see huatuo.log and driver.txt"
fi
assert_netdev_transition "${NETDEV_DEVICE}" down linkstatus_admindown
assert_netdev_transition "${NETDEV_DEVICE}" up linkstatus_adminup
assert_netdev_transition "${NETDEV_PEER}" down linkstatus_carrierdown true
assert_netdev_transition "${NETDEV_PEER}" up linkstatus_carrierup true

assert_huatuo_bamai_shutdown
assert_netdev_event_metadata
assert_netdev_device_filter
assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" netdev_events
log_info "valid netdev_events sample: $(jq -cs '.[0]' "${NETDEV_EVENTS_FILE}")"
log_info "netdev_events passed: admin/carrier transitions, event metadata, counters and DeviceList filtering"
