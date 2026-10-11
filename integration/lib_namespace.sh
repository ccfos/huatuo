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

NETNS=""
NETNS_SERVER=""
NETNS_CLIENT=""
NETNS_VETH_SERVER=""
NETNS_VETH_CLIENT=""
NETNS_SERVER_ADDR=""
NETNS_CLIENT_ADDR=""

namespace_setup() {
	local name=$1

	ip netns add "${name}" || fatal "failed to create netns ${name}"
	NETNS=${name}
}

namespace_setup_with_pair() {
	local prefix=$1 server_addr=$2 client_addr=$3 netmask=${4:-24}
	local server="ts_${prefix}" client="tc_${prefix}"

	ip netns add "${server}" || fatal "failed to create server netns ${server}"
	NETNS_SERVER=${server}
	ip netns add "${client}" || fatal "failed to create client netns ${client}"
	NETNS_CLIENT=${client}
	NETNS_VETH_SERVER="vs_${prefix}"
	NETNS_VETH_CLIENT="vc_${prefix}"
	NETNS_SERVER_ADDR=${server_addr}
	NETNS_CLIENT_ADDR=${client_addr}

	ip link add "${NETNS_VETH_SERVER}" type veth peer name "${NETNS_VETH_CLIENT}"
	ip link set "${NETNS_VETH_SERVER}" netns "${NETNS_SERVER}"
	ip link set "${NETNS_VETH_CLIENT}" netns "${NETNS_CLIENT}"

	ip netns exec "${NETNS_SERVER}" ip addr add "${server_addr}/${netmask}" dev "${NETNS_VETH_SERVER}"
	ip netns exec "${NETNS_SERVER}" ip link set "${NETNS_VETH_SERVER}" up
	ip netns exec "${NETNS_SERVER}" ip link set lo up

	ip netns exec "${NETNS_CLIENT}" ip addr add "${client_addr}/${netmask}" dev "${NETNS_VETH_CLIENT}"
	ip netns exec "${NETNS_CLIENT}" ip link set "${NETNS_VETH_CLIENT}" up
	ip netns exec "${NETNS_CLIENT}" ip link set lo up
}

namespace_cleanup() {
	local namespace
	for namespace in "${NETNS}" "${NETNS_SERVER}" "${NETNS_CLIENT}"; do
		[[ -z "${namespace}" ]] || ip netns del "${namespace}" 2> /dev/null || true
	done
	NETNS=""
	NETNS_SERVER=""
	NETNS_CLIENT=""
	NETNS_VETH_SERVER=""
	NETNS_VETH_CLIENT=""
	NETNS_SERVER_ADDR=""
	NETNS_CLIENT_ADDR=""
}
