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

# Verify CLI device filters through two real ingress devices and software drops.
# Storage delivery and hardware drop sources belong to their existing tests.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_namespace.sh"

readonly DEVICE_FILTER_PREFIX="df${BASHPID}"
readonly DEVICE_FILTER_SERVER_A="10.205.1.1"
readonly DEVICE_FILTER_CLIENT_A="10.205.1.2"
readonly DEVICE_FILTER_SERVER_B="10.205.2.1"
readonly DEVICE_FILTER_CLIENT_B="10.205.2.2"
readonly DEVICE_FILTER_DEVICE_A="vs_${DEVICE_FILTER_PREFIX}"
readonly DEVICE_FILTER_PEER_A="vc_${DEVICE_FILTER_PREFIX}"
readonly DEVICE_FILTER_DEVICE_B="vb_${DEVICE_FILTER_PREFIX}"
readonly DEVICE_FILTER_PEER_B="vp_${DEVICE_FILTER_PREFIX}"
readonly DEVICE_FILTER_DURATION=6
readonly DEVICE_FILTER_TIMEOUT=30

require_commands bash ip iptables jq timeout ss nsenter awk
require_readable /sys/kernel/btf/vmlinux
tracepoint_available skb kfree_skb || skip "skb/kfree_skb tracepoint is unavailable"
device_filter_port=$(allocate_available_port) || fatal "cannot allocate device filter destination port"

device_filter_capture_pid=""
device_filter_sender_pids=()
device_filter_host_devices=()

cleanup() {
	local status=$? pid device namespace
	local namespaces=("${NETNS_SERVER}" "${NETNS_CLIENT}")
	for pid in "${device_filter_sender_pids[@]}"; do
		stop_and_wait_by_pid "${pid}" 3 || true
	done
	device_filter_sender_pids=()
	# Let timeout finish its 5-second SIGKILL escalation before stopping it.
	stop_and_wait_by_pid "${device_filter_capture_pid}" 7 || true
	device_filter_capture_pid=""
	namespace_cleanup
	# The shared helper can fail after creation but before moving a veth.
	for device in "${device_filter_host_devices[@]}"; do
		if ip link show dev "${device}" > /dev/null 2>&1; then
			ip link del dev "${device}" || status=1
		fi
	done
	for namespace in "${namespaces[@]}"; do
		[[ -n "${namespace}" ]] || continue
		if [[ -e "/run/netns/${namespace}" ]]; then
			log_error "network namespace remains after cleanup: ${namespace}"
			status=1
		fi
	done
	exit "${status}"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Callers start this function in the background; its traps belong to that worker.
device_filter_send_syn() {
	local destination=$1 stop_file=$2 connection_pid="" status
	trap 'stop_and_wait_by_pid "${connection_pid}" 2 || true' EXIT
	trap 'exit 130' INT
	trap 'exit 143' TERM
	while [[ ! -e "${stop_file}" ]]; do
		# Each dropped connect waits 0.5 seconds, bounding the per-path packet rate.
		timeout --kill-after=1s 0.5s \
			nsenter --net="/run/netns/${NETNS_CLIENT}" bash -c \
			'exec 3<>/dev/tcp/"$1"/"$2"' device-filter "${destination}" "${device_filter_port}" &
		connection_pid=$!
		status=0
		wait "${connection_pid}" || status=$?
		connection_pid=""
		assert_eq "${status}" 124 "SYN connect to ${destination}:${device_filter_port}" \
			|| fatal "expected a dropped connect to time out; actual status=${status}"
	done
}

device_filter_drop_count() {
	local snapshot=$1 device=$2
	awk -v device="${device}" '
		$3 == "DROP" && $6 == device { count += $1; found = 1 }
		END { if (!found) exit 1; print count }
	' "${snapshot}" || fatal "DROP counter for ${device} is missing: ${snapshot}"
}

assert_device_filter_drops() {
	local case_dir=$1 device=$2 before after
	before=$(device_filter_drop_count "${case_dir}/drops-before.txt" "${device}")
	after=$(device_filter_drop_count "${case_dir}/drops-after.txt" "${device}")
	log_info "${case_dir##*/}: ${device} DROP delta=$((after - before))"
	((after > before)) || fatal "${device}: expected DROP counter growth, before=${before}, after=${after}"
}

assert_device_filter_events() {
	local case_dir=$1 source=$2 destination=$3 device=$4 ifindex=$5 expected=$6
	local events_file="${case_dir}/${device}.json" count
	# Identify traffic before examining devices, so a wrong device field cannot
	# hide an event that should have been excluded.
	jq -cs --arg source "${source}" --arg destination "${destination}" \
		--argjson port "${device_filter_port}" '
		[.[] | select(.layers.ipv4.saddr == $source
			and .layers.ipv4.daddr == $destination
			and .layers.ipv4.protocol == "TCP" and .layers.tcp.dport == $port)]
	' "${case_dir}/events.json" > "${events_file}" \
		|| fatal "${case_dir##*/}: invalid dropwatch JSON"
	count=$(jq 'length' "${events_file}")
	log_info "${case_dir##*/}: ${device} events=${count}, expected=${expected}"
	case "${expected}" in
	absent)
		assert_eq "${count}" 0 "${case_dir##*/}: excluded ${device} events" \
			|| fatal "${case_dir##*/}: excluded flow produced ${count} events; see ${events_file}"
		;;
	present)
		((count > 0)) || fatal "${case_dir##*/}: expected ${device} events, actual=0"
		# Kernel defaults may add ECN negotiation flags to an initial SYN.
		jq -e --arg device "${device}" --argjson ifindex "${ifindex}" '
			all(.[]; .drop_source == "software" and .netdev_name == $device
				and .netdev_ifindex == $ifindex
				and (.layers.tcp.flags | split("|") | index("SYN") != null))
		' "${events_file}" > /dev/null \
			|| fatal "${case_dir##*/}: expected software SYN drops on ${device}/${ifindex}; see ${events_file}"
		;;
	*) fatal "unknown device filter expectation: ${expected}" ;;
	esac
}

device_filter_run_case() {
	local name=$1 expected_a=$2 expected_b=$3
	shift 3
	local case_dir="${TOOL_WORK_DIR}/${name}" capture_status=0 sender_status index pid destination
	mkdir "${case_dir}"
	ip netns exec "${NETNS_SERVER}" iptables -w 5 -nvxL INPUT > "${case_dir}/drops-before.txt"

	# Only change the network namespace: ip netns exec remounts sysfs and may
	# hide tracefs, which the tracepoint attachment needs.
	timeout --kill-after=5s "${DEVICE_FILTER_TIMEOUT}s" \
		nsenter --net="/run/netns/${NETNS_SERVER}" \
		"${TOOL_BIN}" --bpf-path "${TOOL_BPF}" \
		--filter "tcp and dst port ${device_filter_port}" \
		--max-events-per-second 0 --duration "${DEVICE_FILTER_DURATION}" --output json "$@" \
		> "${case_dir}/events.json" 2> "${case_dir}/stderr.log" &
	device_filter_capture_pid=$!

	# Fresh traffic spans startup and the whole capture; no startup sleep or
	# early success can shorten the negative observation window.
	for destination in "${DEVICE_FILTER_SERVER_A}" "${DEVICE_FILTER_SERVER_B}"; do
		device_filter_send_syn "${destination}" "${case_dir}/stop" \
			> "${case_dir}/sender-${destination}.log" 2>&1 &
		device_filter_sender_pids+=("$!")
	done
	wait "${device_filter_capture_pid}" || capture_status=$?
	device_filter_capture_pid=""
	# Let each bounded connect finish normally, so only timeout=124 is accepted.
	touch "${case_dir}/stop"
	for index in "${!device_filter_sender_pids[@]}"; do
		pid=${device_filter_sender_pids[index]}
		sender_status=0
		wait "${pid}" || sender_status=$?
		unset 'device_filter_sender_pids[index]'
		assert_eq "${sender_status}" 0 "${name}: sender exit status" \
			|| fatal "${name}: SYN sender failed; see ${case_dir}/sender-*.log"
	done
	device_filter_sender_pids=()
	ip netns exec "${NETNS_SERVER}" iptables -w 5 -nvxL INPUT > "${case_dir}/drops-after.txt"

	assert_eq "${capture_status}" 0 "${name}: dropwatch exit status" \
		|| fatal "${name}: dropwatch failed; see ${case_dir}/stderr.log"
	assert_device_filter_drops "${case_dir}" "${DEVICE_FILTER_DEVICE_A}"
	assert_device_filter_drops "${case_dir}" "${DEVICE_FILTER_DEVICE_B}"
	assert_device_filter_events "${case_dir}" "${DEVICE_FILTER_CLIENT_A}" "${DEVICE_FILTER_SERVER_A}" \
		"${DEVICE_FILTER_DEVICE_A}" "${device_filter_ifindex_a}" "${expected_a}"
	assert_device_filter_events "${case_dir}" "${DEVICE_FILTER_CLIENT_B}" "${DEVICE_FILTER_SERVER_B}" \
		"${DEVICE_FILTER_DEVICE_B}" "${device_filter_ifindex_b}" "${expected_b}"
	assert_kernel_observation_timestamps "${case_dir}/events.json"
	assert_log_has_no_failure "${case_dir}/stderr.log" "${name}: dropwatch"
}

bpf_tool_setup dropwatch net_dropwatch dropwatch-device-filter
for device_filter_device in \
	"${DEVICE_FILTER_DEVICE_A}" "${DEVICE_FILTER_PEER_A}" \
	"${DEVICE_FILTER_DEVICE_B}" "${DEVICE_FILTER_PEER_B}"; do
	if ip link show dev "${device_filter_device}" > /dev/null 2>&1; then
		fatal "test device already exists: ${device_filter_device}"
	fi
done
device_filter_host_devices=(
	"${DEVICE_FILTER_DEVICE_A}" "${DEVICE_FILTER_PEER_A}"
	"${DEVICE_FILTER_DEVICE_B}" "${DEVICE_FILTER_PEER_B}"
)
namespace_setup_with_pair "${DEVICE_FILTER_PREFIX}" "${DEVICE_FILTER_SERVER_A}" "${DEVICE_FILTER_CLIENT_A}"
ip link add "${DEVICE_FILTER_DEVICE_B}" type veth peer name "${DEVICE_FILTER_PEER_B}"
ip link set "${DEVICE_FILTER_DEVICE_B}" netns "${NETNS_SERVER}"
ip link set "${DEVICE_FILTER_PEER_B}" netns "${NETNS_CLIENT}"
ip -n "${NETNS_SERVER}" addr add "${DEVICE_FILTER_SERVER_B}/24" dev "${DEVICE_FILTER_DEVICE_B}"
ip -n "${NETNS_CLIENT}" addr add "${DEVICE_FILTER_CLIENT_B}/24" dev "${DEVICE_FILTER_PEER_B}"
ip -n "${NETNS_SERVER}" link set "${DEVICE_FILTER_DEVICE_B}" up
ip -n "${NETNS_CLIENT}" link set "${DEVICE_FILTER_PEER_B}" up

ip -n "${NETNS_SERVER}" -j link show > "${TOOL_WORK_DIR}/devices.json"
device_filter_ifindex_a=$(jq -er --arg device "${DEVICE_FILTER_DEVICE_A}" \
	'.[] | select(.ifname == $device) | .ifindex' "${TOOL_WORK_DIR}/devices.json")
device_filter_ifindex_b=$(jq -er --arg device "${DEVICE_FILTER_DEVICE_B}" \
	'.[] | select(.ifname == $device) | .ifindex' "${TOOL_WORK_DIR}/devices.json")

ip netns exec "${NETNS_SERVER}" iptables -w 5 -A INPUT -i "${DEVICE_FILTER_DEVICE_A}" \
	-s "${DEVICE_FILTER_CLIENT_A}" -d "${DEVICE_FILTER_SERVER_A}" \
	-p tcp --dport "${device_filter_port}" -j DROP
ip netns exec "${NETNS_SERVER}" iptables -w 5 -A INPUT -i "${DEVICE_FILTER_DEVICE_B}" \
	-s "${DEVICE_FILTER_CLIENT_B}" -d "${DEVICE_FILTER_SERVER_B}" \
	-p tcp --dport "${device_filter_port}" -j DROP

device_filter_run_case baseline present present
device_filter_run_case allow_a present absent --device "${DEVICE_FILTER_DEVICE_A}"
device_filter_run_case exclude_a absent present --device-excluded "${DEVICE_FILTER_DEVICE_A}"
