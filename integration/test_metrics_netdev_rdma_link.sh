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

# Read registered hardware first, then software RDMA. RXE on a private veth
# supplies the same kernel netlink path when no existing device is usable.
set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"

integration_isolate_run_directory "$@"

readonly RDMA_METRICS="${HUATUO_BAMAI_TEST_TMPDIR}/rdma-metrics.txt"
readonly RDMA_HEADERS="${HUATUO_BAMAI_TEST_TMPDIR}/rdma-metrics.headers"
readonly RDMA_COUNTERS="${HUATUO_BAMAI_TEST_TMPDIR}/rdma-counters.txt"

require_commands rdma jq curl ss
RDMA_PORT=$(allocate_available_port) || fatal "cannot allocate RDMA metrics HTTP port"
readonly RDMA_PORT
HUATUO_BAMAI_ADDR="http://127.0.0.1:${RDMA_PORT}"
HUATUO_BAMAI_METRICS_API="${HUATUO_BAMAI_ADDR}/metrics"
CURL_TIMEOUT=(--connect-timeout 2 --max-time 5)

rdma_device=""
rdma_owned_device=""
rdma_owned_netdev=""
rdma_ports=0
rdma_index=""

cleanup() {
	local status=0 pid=""
	if [[ -r ${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid ]]; then
		pid=$(< "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")
	fi
	huatuo_bamai_stop || status=1
	# Keep devices registered until all daemon readers have stopped.
	if [[ -n ${pid} ]] && ! wait_until 2 0.1 process_exited "${pid}"; then
		log_error "daemon pid=${pid} is still running; retaining test devices"
		return 1
	fi
	if [[ -n ${rdma_owned_device} && -e /sys/class/infiniband/${rdma_owned_device} ]]; then
		rdma link delete "${rdma_owned_device}" || status=1
	fi
	# Deleting the backing veth can also remove RXE after an RDMA delete failure.
	if [[ -n ${rdma_owned_netdev} && -e /sys/class/net/${rdma_owned_netdev} ]]; then
		ip link delete "${rdma_owned_netdev}" || status=1
	fi
	return "${status}"
}
trap 'rdma_exit_status=$?
if ! cleanup && [[ ${rdma_exit_status} == 0 || ${rdma_exit_status} == 77 ]]; then
	rdma_exit_status=1
fi
exit "${rdma_exit_status}"' EXIT

rdma_statistics_available() {
	local device=$1 port
	: > "${RDMA_COUNTERS}"
	rdma_ports=0
	for port in /sys/class/infiniband/"${device}"/ports/*; do
		[[ -d ${port} ]] || continue
		if ! rdma -j statistic show link "${device}/${port##*/}" \
			> "${HUATUO_BAMAI_TEST_TMPDIR}/rdma-statistics-${port##*/}.json" \
			2> "${HUATUO_BAMAI_TEST_TMPDIR}/rdma-statistics.err"; then
			cat "${HUATUO_BAMAI_TEST_TMPDIR}/rdma-statistics.err" >&2
			return 1
		fi
		jq -e 'type == "array" and all(.[];
			type == "object" and (.ifname | type == "string") and (.port | type == "number"))' \
			"${HUATUO_BAMAI_TEST_TMPDIR}/rdma-statistics-${port##*/}.json" > /dev/null \
			|| fatal "invalid RDMA statistics response; see rdma-statistics-${port##*/}.json"
		jq -er --arg device "${device}" --argjson port "${port##*/}" '
			[.[] | select(.ifname == $device and .port == $port)
			 | del(.ifname, .port) | to_entries[]
			 | select(.value | type == "number") | [.key, (.value | tostring)] | @tsv]
			| select(length > 0) | .[]
		' "${HUATUO_BAMAI_TEST_TMPDIR}/rdma-statistics-${port##*/}.json" \
			>> "${RDMA_COUNTERS}" || return 1
		rdma_ports=$((rdma_ports + 1))
	done
	[[ ${rdma_ports} -gt 0 && -s ${RDMA_COUNTERS} ]]
}

rdma_is_hardware() {
	local device=$1
	# RXE exposes a parent attribute even when backed by a physical Ethernet NIC.
	[[ -L /sys/class/infiniband/${device}/device/driver &&
		! -f /sys/class/infiniband/${device}/parent ]] || return 1
	! grep -qiE '^(rxe$|Software iWARP)' "/sys/class/infiniband/${device}/node_desc"
}

rdma_select_existing() {
	local mode=$1 entry device
	for entry in /sys/class/infiniband/*; do
		[[ -d ${entry} ]] || continue
		device=${entry##*/}
		if [[ ${mode} == hardware ]]; then
			rdma_is_hardware "${device}" || continue
		else
			rdma_is_hardware "${device}" && continue
		fi
		rdma_statistics_available "${device}" || continue
		rdma_device=${device}
		log_info "RDMA selected existing ${mode} device=${rdma_device} ports=${rdma_ports}"
		return 0
	done
	return 1
}

rdma_setup_netdev() {
	local operation=$1
	shift
	local output="${HUATUO_BAMAI_TEST_TMPDIR}/rdma-netdev-${operation}.log"
	if LC_ALL=C ip "$@" > "${output}" 2>&1; then
		return 0
	fi
	cat "${output}" >&2
	if grep -Eqi 'operation not permitted|permission denied|not supported' "${output}"; then
		skip "software RDMA netdev ${operation} lacks network permissions or kernel support"
	fi
	fatal "software RDMA netdev ${operation} failed; see ${output}"
}

rdma_create_software() {
	local suffix=${HUATUO_BAMAI_TEST_TMPDIR##*.} device netdev
	require_commands ip
	if [[ ! -d /sys/module/rdma_rxe ]]; then
		require_commands modprobe
		if ! modprobe rdma_rxe > "${HUATUO_BAMAI_TEST_TMPDIR}/rdma-modprobe.log" 2>&1; then
			cat "${HUATUO_BAMAI_TEST_TMPDIR}/rdma-modprobe.log" >&2
			skip "no usable RDMA device and rdma_rxe module is unavailable"
		fi
	fi
	device="htrdma${suffix}"
	netdev="htr${suffix}"
	[[ ! -e /sys/class/net/${netdev} && ! -e /sys/class/infiniband/${device} ]] \
		|| fatal "RDMA test device name already exists: ${device}/${netdev}"
	rdma_setup_netdev create link add "${netdev}" type veth peer name "${netdev}p"
	rdma_owned_netdev=${netdev}
	rdma_setup_netdev enable link set "${netdev}" up
	rdma_setup_netdev enable-peer link set "${netdev}p" up
	if ! LC_ALL=C rdma link add "${device}" type rxe netdev "${netdev}" \
		> "${HUATUO_BAMAI_TEST_TMPDIR}/rdma-create.log" 2>&1; then
		cat "${HUATUO_BAMAI_TEST_TMPDIR}/rdma-create.log" >&2
		if grep -Eqi 'not supported|unknown .*type|operation not permitted' \
			"${HUATUO_BAMAI_TEST_TMPDIR}/rdma-create.log"; then
			skip "no usable RDMA device and software RDMA creation is unavailable"
		fi
		fatal "failed to create RXE device ${device}; see rdma-create.log"
	fi
	rdma_owned_device=${device}
	rdma_device=${device}
	rdma_statistics_available "${device}" \
		|| skip "software RDMA device has no readable netlink statistics"
	log_info "RDMA selected test-owned RXE device=${rdma_device} netdev=${netdev}"
}

assert_rdma_metrics() {
	local status curl_status=0
	: > "${RDMA_METRICS}"
	: > "${RDMA_HEADERS}"
	status=$(curl -sS "${CURL_TIMEOUT[@]}" -D "${RDMA_HEADERS}" -o "${RDMA_METRICS}" \
		-w '%{http_code}' "${HUATUO_BAMAI_METRICS_API}" \
		2> "${HUATUO_BAMAI_TEST_TMPDIR}/rdma-metrics.err") || curl_status=$?
	if [[ ${curl_status} -ne 0 || ${status} != 200 ]]; then
		cat "${RDMA_HEADERS}" "${RDMA_METRICS}" "${HUATUO_BAMAI_TEST_TMPDIR}/rdma-metrics.err" >&2
		fatal "RDMA /metrics: curl=${curl_status}, HTTP=${status}; expected curl=0 HTTP=200"
	fi
	grep -Eqi '^Content-Type:[[:blank:]]*text/plain([;[:space:]]|$)' "${RDMA_HEADERS}" \
		|| fatal "RDMA /metrics: expected text/plain; see ${RDMA_HEADERS}"
	awk -v device="${rdma_device}" -v ports="${rdma_ports}" -v device_index="${rdma_index}" -v compare_values="${rdma_owned_device:+1}" '
		function has_label(key, value) {
			return index($1, "{" key "=\"" value "\"") || index($1, "," key "=\"" value "\"")
		}
		NR == FNR {
			name = "huatuo_bamai_netdev_rdma_link_" $1
			expected[name] = 1; values[name] = $2; next
		}
		$1 == "#" && $2 == "TYPE" && $4 == "counter" { types[$3] = 1 }
		$1 ~ /^huatuo_bamai_scrape_collector_success\{/ && has_label("collector", "netdev_rdma_link") { success = ($2 == 1) }
		$1 ~ /^huatuo_bamai_netdev_rdma_link_/ && has_label("device", device) {
			name = $1; sub(/\{.*/, "", name)
			guid = $1; sub(/^.*nodeguid="/, "", guid); sub(/".*$/, "", guid)
			if (!has_label("index", device_index) || !has_label("num_ports", ports) ||
			    (!has_label("nodeguid", guid) || length(guid) != 23 || guid ~ /[^[:xdigit:]:]/) || $2 !~ /^[0-9]+([.][0-9]+)?([eE][+]?[0-9]+)?$/) bad = 1
			# A fresh private RXE has no RDMA workload; its counters stay stable.
			if (compare_values && $2 + 0 != values[name] + 0) bad = 1
			found[name]++
		}
		END {
			for (name in expected) if (!types[name] || !found[name]) bad = 1
			exit !(success && !bad)
		}
	' "${RDMA_COUNTERS}" "${RDMA_METRICS}" \
		|| fatal "RDMA ${rdma_device}: missing counters, invalid types/labels or mismatched private RXE values; see ${RDMA_METRICS}"
	grep '^huatuo_bamai_netdev_rdma_link_' "${RDMA_METRICS}" | grep -F "device=\"${rdma_device}\""
}

# 1. Existing hardware is read-only; only the final RXE fallback creates devices.
if ! rdma_select_existing hardware && ! rdma_select_existing fallback; then
	rdma_create_software
fi
rdma -j dev show > "${HUATUO_BAMAI_TEST_TMPDIR}/rdma-devices.json"
rdma_index=$(jq -er --arg device "${rdma_device}" \
	'.[] | select(.ifname == $device) | .ifindex' "${HUATUO_BAMAI_TEST_TMPDIR}/rdma-devices.json")

# 2. Use the real daemon and kernel statistics; hardware counters may change.
write_config_bamai_netdev_rdma_link > "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf"
integration_huatuo_bamai_start : \
	--region dev --procfs-prefix "${HUATUO_BAMAI_TEST_FIXTURES}" \
	--disable-storage --disable-kubelet
assert_rdma_metrics
assert_huatuo_bamai_shutdown
log_info "RDMA device discovery, statistics and public metrics passed: ${rdma_device}"
