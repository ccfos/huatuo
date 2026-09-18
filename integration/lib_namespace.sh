#!/usr/bin/env bash

# Copyright 2026 The HuaTuo Authors
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

TCP_NS_SERVER=""
TCP_NS_CLIENT=""
TCP_NS_VETH_SERVER=""
TCP_NS_VETH_CLIENT=""
TCP_NS_SERVER_ADDR=""
TCP_NS_CLIENT_ADDR=""

PROCESS_NET_NAMESPACE=""
PROCESS_NET_NAMESPACE_PID=""

tcp_namespace_setup() {
	local prefix=$1 server_addr=$2 client_addr=$3 netmask=${4:-24}

	TCP_NS_SERVER="ts_${prefix}"
	TCP_NS_CLIENT="tc_${prefix}"
	TCP_NS_VETH_SERVER="vs_${prefix}"
	TCP_NS_VETH_CLIENT="vc_${prefix}"
	TCP_NS_SERVER_ADDR=${server_addr}
	TCP_NS_CLIENT_ADDR=${client_addr}

	ip netns add "${TCP_NS_SERVER}" || fatal "failed to create server netns"
	ip netns add "${TCP_NS_CLIENT}" || fatal "failed to create client netns"
	ip link add "${TCP_NS_VETH_SERVER}" type veth peer name "${TCP_NS_VETH_CLIENT}"
	ip link set "${TCP_NS_VETH_SERVER}" netns "${TCP_NS_SERVER}"
	ip link set "${TCP_NS_VETH_CLIENT}" netns "${TCP_NS_CLIENT}"

	ip netns exec "${TCP_NS_SERVER}" ip addr add "${server_addr}/${netmask}" dev "${TCP_NS_VETH_SERVER}"
	ip netns exec "${TCP_NS_SERVER}" ip link set "${TCP_NS_VETH_SERVER}" up
	ip netns exec "${TCP_NS_SERVER}" ip link set lo up

	ip netns exec "${TCP_NS_CLIENT}" ip addr add "${client_addr}/${netmask}" dev "${TCP_NS_VETH_CLIENT}"
	ip netns exec "${TCP_NS_CLIENT}" ip link set "${TCP_NS_VETH_CLIENT}" up
	ip netns exec "${TCP_NS_CLIENT}" ip link set lo up
}

tcp_namespace_cleanup() {
	[[ -z "${TCP_NS_SERVER}" ]] || ip netns del "${TCP_NS_SERVER}" 2> /dev/null || true
	[[ -z "${TCP_NS_CLIENT}" ]] || ip netns del "${TCP_NS_CLIENT}" 2> /dev/null || true
}

process_network_namespace_setup() {
	local prefix=$1
	local attempt

	PROCESS_NET_NAMESPACE="pn_${prefix}_$$"
	ip netns add "${PROCESS_NET_NAMESPACE}" \
		|| fatal "failed to create process netns ${PROCESS_NET_NAMESPACE}"
	ip netns exec "${PROCESS_NET_NAMESPACE}" sleep 300 &
	local launcher_pid=$!

	for ((attempt = 0; attempt < 50; attempt++)); do
		PROCESS_NET_NAMESPACE_PID=$(ip netns pids "${PROCESS_NET_NAMESPACE}" | head -n 1)
		[[ -n "${PROCESS_NET_NAMESPACE_PID}" ]] && break
		sleep 0.1
	done
	if [[ -z "${PROCESS_NET_NAMESPACE_PID}" ]]; then
		kill "${launcher_pid}" 2> /dev/null || true
		fatal "no live process found in netns ${PROCESS_NET_NAMESPACE}"
	fi

	local process_inode namespace_inode
	process_inode=$(stat -Lc '%i' "/proc/${PROCESS_NET_NAMESPACE_PID}/ns/net")
	namespace_inode=$(stat -Lc '%i' "/run/netns/${PROCESS_NET_NAMESPACE}")
	[[ "${process_inode}" == "${namespace_inode}" ]] \
		|| fatal "pid ${PROCESS_NET_NAMESPACE_PID} is not in ${PROCESS_NET_NAMESPACE}"
}

process_network_namespace_cleanup() {
	if [[ -n "${PROCESS_NET_NAMESPACE_PID}" ]]; then
		kill "${PROCESS_NET_NAMESPACE_PID}" 2> /dev/null || true
		wait "${PROCESS_NET_NAMESPACE_PID}" 2> /dev/null || true
	fi
	[[ -z "${PROCESS_NET_NAMESPACE}" ]] \
		|| ip netns del "${PROCESS_NET_NAMESPACE}" 2> /dev/null \
		|| true
	PROCESS_NET_NAMESPACE=""
	PROCESS_NET_NAMESPACE_PID=""
}
