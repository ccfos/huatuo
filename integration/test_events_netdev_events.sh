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
# Devices and the daemon share a private network namespace; discovery and deletion are not covered.

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
readonly CURL_TIMEOUT=(--connect-timeout 2 --max-time 3)

require_commands ip jq curl awk ethtool ss unshare
[[ ${EUID} -eq 0 ]] || skip "veth creation requires root and CAP_NET_ADMIN"
# Host network managers cannot change these devices. The kernel destroys the
# pair when the last process exits this unnamed namespace.
if [[ ${1:-} != --netdev-isolated ]]; then
	exec unshare --net bash "$0" --netdev-isolated
fi
integration_isolate_run_directory "$@"
ip link set dev lo up

NETDEV_HTTP_PORT=$(allocate_available_port) || fatal "failed to allocate a netdev_events API port"
readonly NETDEV_HTTP_PORT
HUATUO_BAMAI_ADDR="http://127.0.0.1:${NETDEV_HTTP_PORT}"
HUATUO_BAMAI_METRICS_API="${HUATUO_BAMAI_ADDR}/metrics"

netdev_device_index=""
netdev_device_mac=""
netdev_baseline_event_count=""
# Stop the daemon on failure as well so it cannot keep the namespace alive.
trap 'huatuo_bamai_stop' EXIT

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
netdev_probe_subscription() {
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

netdev_events_match() {
	local offset=$1
	shift
	# Admin and carrier notifications may arrive separately. Compare their union
	# with the complete expected set so opposite or unrelated states cannot pass.
	jq -se --argjson offset "${offset}" '
		[.[$offset:][] | .tracer_data.linkstatus | split(", ")[]] | unique
		== ($ARGS.positional | sort)
	' "${NETDEV_EVENTS_FILE}" --args "$@" > /dev/null 2> "${NETDEV_EVENT_ERROR_FILE}"
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
	local status=$1 previous=$2 current
	huatuo_bamai_collect_metrics 2> "${NETDEV_METRIC_ERROR_FILE}" || return 1
	current=$(netdev_metric_value "${status}")
	awk -v current="${current}" -v previous="${previous}" 'BEGIN { exit !(current > previous) }'
}

netdev_report_metric_diagnostics() {
	cat "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.status" >&2 2> /dev/null || true
	report_http_response "GET /metrics" "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.response" \
		"${HUATUO_BAMAI_TEST_TMPDIR}/metrics.curl-error"
}

netdev_capture_baseline() {
	# Storage may be mid-write and the HTTP endpoint may briefly stall under VM load.
	netdev_baseline_event_count=$(jq -s 'length' "${NETDEV_EVENTS_FILE}" 2> "${NETDEV_EVENT_ERROR_FILE}") || return 1
	huatuo_bamai_collect_metrics 2> "${NETDEV_METRIC_ERROR_FILE}"
}

assert_netdev_transition() {
	local device=$1 state=$2 offset status
	shift 2
	local -A previous=()
	if ! wait_until 30 0.5 netdev_capture_baseline; then
		netdev_report_event_diagnostics
		fatal "cannot read baseline for ${device} ${state}; metrics error: $(cat "${NETDEV_METRIC_ERROR_FILE}" 2> /dev/null)"
	fi
	offset=${netdev_baseline_event_count}
	for status in "$@"; do previous[${status}]=$(netdev_metric_value "${status}"); done
	ip link set dev "${device}" "${state}"
	if ! wait_until 30 0.5 netdev_events_match "${offset}" "$@"; then
		netdev_report_event_diagnostics
		fatal "${device} ${state}: expected states [$*] after offset=${offset}"
	fi
	for status in "$@"; do
		if ! wait_until 30 0.5 netdev_metric_increased "${status}" "${previous[${status}]}"; then
			netdev_report_metric_diagnostics
			fatal "${device} ${state}: ${status} counter did not increase from ${previous[${status}]}; see ${NETDEV_METRICS_FILE} and ${NETDEV_METRIC_ERROR_FILE}"
		fi
	done
	# Include events persisted while waiting for the metric response.
	if ! wait_until 30 0.5 netdev_events_match "${offset}" "$@"; then
		netdev_report_event_diagnostics
		fatal "${device} ${state}: unexpected states after offset=${offset}; expected [$*]"
	fi
	log_info "${device} ${state}: events and counters matched [$*]"
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
if ! ip link add "${NETDEV_DEVICE}" address "${NETDEV_DEVICE_MAC}" \
	type veth peer name "${NETDEV_PEER}" address "${NETDEV_PEER_MAC}" \
	2> "${HUATUO_BAMAI_TEST_TMPDIR}/veth-create.err"; then
	skip "cannot create veth pair; check CAP_NET_ADMIN and kernel veth support: $(< "${HUATUO_BAMAI_TEST_TMPDIR}/veth-create.err")"
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

if ! wait_until 30 0.5 netdev_probe_subscription; then
	netdev_report_event_diagnostics
	fatal "netdev_events did not observe ${NETDEV_DEVICE}; see huatuo.log and driver.txt"
fi
assert_netdev_transition "${NETDEV_DEVICE}" down linkstatus_admindown linkstatus_carrierdown
assert_netdev_transition "${NETDEV_DEVICE}" up linkstatus_adminup linkstatus_carrierup
assert_netdev_transition "${NETDEV_PEER}" down linkstatus_carrierdown
assert_netdev_transition "${NETDEV_PEER}" up linkstatus_carrierup

assert_huatuo_bamai_shutdown
assert_netdev_event_metadata
assert_netdev_device_filter
assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" netdev_events
log_info "valid netdev_events sample: $(jq -cs '.[0]' "${NETDEV_EVENTS_FILE}")"
log_info "netdev_events passed: admin/carrier transitions, event metadata, counters and DeviceList filtering"
