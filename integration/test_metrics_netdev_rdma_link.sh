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
source "${ROOT_DIR}/integration/lib_net.sh"
source "${ROOT_DIR}/integration/config.sh"

readonly RDMA_RESPONSE="${HUATUO_BAMAI_TEST_TMPDIR}/rdma-metrics"
readonly RDMA_METRICS="${RDMA_RESPONSE}.txt"
readonly RDMA_COUNTERS="${HUATUO_BAMAI_TEST_TMPDIR}/rdma-counters.txt"

require_commands rdma jq curl ss awk grep
RDMA_PORT=$(allocate_available_port) || fatal "cannot allocate RDMA metrics HTTP port"
readonly RDMA_PORT
HUATUO_BAMAI_ADDR="http://127.0.0.1:${RDMA_PORT}"
HUATUO_BAMAI_METRICS_API="${HUATUO_BAMAI_ADDR}/metrics"

rdma_device=""
rdma_owned_device=""
rdma_owned_netdev=""
rdma_ports=0
rdma_index=""

cleanup() {
	local status=0
	huatuo_bamai_stop || return 1
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

rdma_read_statistics() {
	local device=$1 port response counters
	: > "${RDMA_COUNTERS}" || return 1
	rdma_ports=0
	for port in /sys/class/infiniband/"${device}"/ports/*; do
		[[ -d ${port} ]] || continue
		response="${HUATUO_BAMAI_TEST_TMPDIR}/rdma-statistics-${port##*/}.json"
		if ! rdma -j statistic show link "${device}/${port##*/}" \
			> "${response}" 2> "${HUATUO_BAMAI_TEST_TMPDIR}/rdma-statistics.err"; then
			cat "${HUATUO_BAMAI_TEST_TMPDIR}/rdma-statistics.err" >&2
			return 1
		fi
		counters=$(jq -r --arg device "${device}" --argjson port "${port##*/}" '
			if type != "array" or any(.[];
				type != "object" or (.ifname | type != "string") or (.port | type != "number"))
			then error("invalid RDMA statistics response") else . end
			| .[] | select(.ifname == $device and .port == $port)
			| del(.ifname, .port) | to_entries[] | select(.value | type == "number")
			| [.key, .value, $port] | @tsv
		' "${response}") || fatal "cannot parse RDMA statistics: ${response}"
		[[ -n ${counters} ]] || return 1
		printf '%s\n' "${counters}" >> "${RDMA_COUNTERS}" || return 1
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
		rdma_read_statistics "${device}" || continue
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
	LC_ALL=C ip "$@" > "${output}" 2>&1 && return 0
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
	rdma_read_statistics "${device}" \
		|| skip "software RDMA device has no readable netlink statistics"
	log_info "RDMA selected test-owned RXE device=${rdma_device} netdev=${netdev}"
}

assert_rdma_metrics() {
	local name value port key samples device driver index ports guid
	local -A expected=() found=() metrics=()
	while IFS=$'\t' read -r name value port; do
		expected["huatuo_bamai_netdev_rdma_link_${name}/${port}"]=${value}
		metrics["huatuo_bamai_netdev_rdma_link_${name}"]=1
	done < "${RDMA_COUNTERS}"
	((${#expected[@]} > 0)) || fatal "no RDMA counter expectations: ${RDMA_COUNTERS}"

	assert_metrics_response "${RDMA_RESPONSE}"
	assert_netdev_collector_success "${RDMA_METRICS}" netdev_rdma_link
	samples=$(netdev_metric_samples "${RDMA_METRICS}" huatuo_bamai_netdev_rdma_link_) \
		|| fatal "cannot parse RDMA metrics: ${RDMA_METRICS}"
	while IFS='|' read -r name value device driver port index ports guid; do
		[[ ${device} == "${rdma_device}" ]] || continue
		key="${name}/${port}"
		[[ -n ${expected[${key}]+present} ]] \
			|| fatal "RDMA ${device}: unexpected counter/port ${key}"
		found[${key}]=$((${found[${key}]:-0} + 1))
		assert_eq "${index}/${ports}" "${rdma_index}/${rdma_ports}" "RDMA ${device} index/num_ports" \
			|| fatal "RDMA ${device}: invalid device labels; see ${RDMA_METRICS}"
		[[ ${guid} =~ ^([[:xdigit:]]{2}:){7}[[:xdigit:]]{2}$ ]] \
			|| fatal "RDMA ${device}: expected eight-byte nodeguid, actual=${guid}"
		# Existing hardware counters can advance between the two observations.
		if [[ -n ${rdma_owned_device} ]]; then
			assert_netdev_counter_value "${value}" "RDMA ${device} ${key}" "${expected[${key}]}"
		else
			assert_netdev_counter_value "${value}" "RDMA ${device} ${key}"
		fi
	done <<< "${samples}"
	for key in "${!expected[@]}"; do
		assert_eq "${found[${key}]:-0}" 1 "RDMA ${rdma_device} ${key} sample count" \
			|| fatal "RDMA ${rdma_device}: expected each counter/port once; see ${RDMA_METRICS}"
	done
	for name in "${!metrics[@]}"; do
		assert_netdev_counter_type "${RDMA_METRICS}" "${name}"
	done
	grep '^huatuo_bamai_netdev_rdma_link_' "${RDMA_METRICS}" | grep -F "device=\"${rdma_device}\""
}

# 1. Existing hardware is read-only; only the final RXE fallback creates devices.
if ! rdma_select_existing hardware && ! rdma_select_existing fallback; then
	rdma_create_software
fi
rdma -j dev show > "${HUATUO_BAMAI_TEST_TMPDIR}/rdma-devices.json" \
	2> "${HUATUO_BAMAI_TEST_TMPDIR}/rdma-devices.err" \
	|| fatal "cannot query RDMA devices; see ${HUATUO_BAMAI_TEST_TMPDIR}/rdma-devices.err"
rdma_index=$(jq -er --arg device "${rdma_device}" \
	'.[] | select(.ifname == $device) | .ifindex | select(type == "number")' "${HUATUO_BAMAI_TEST_TMPDIR}/rdma-devices.json") \
	|| fatal "RDMA device ${rdma_device} has no numeric index; see rdma-devices.json"

# 2. Use the real daemon and kernel statistics; hardware counters may change.
integration_huatuo_bamai_start write_config_bamai_netdev_rdma_link \
	--region dev --procfs-prefix "${HUATUO_BAMAI_TEST_FIXTURES}" \
	--disable-storage --disable-kubelet
assert_rdma_metrics
assert_huatuo_bamai_shutdown
log_info "RDMA device discovery, statistics and public metrics passed: ${rdma_device}"
