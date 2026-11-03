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

# One netdevsim device is owned by each test shell. Call destroy from the
# caller's EXIT trap; the library does not unload a module shared by other tests.

set -euo pipefail

NETDEVSIM_ID=""
NETDEVSIM_DEVICE=""
NETDEVSIM_NETDEV=""

netdevsim_device_ready() {
	devlink dev show "${NETDEVSIM_DEVICE}" > /dev/null 2>&1
}

# Call in the current shell so cleanup retains ownership after partial setup.
netdevsim_create() {
	local log_dir=$1 candidate attempt create_log
	require_commands devlink jq udevadm
	[[ -z "${NETDEVSIM_ID}" ]] || fatal "this test already owns a netdevsim device"

	if [[ ! -d /sys/module/netdevsim ]]; then
		require_commands modprobe
		if ! modprobe netdevsim > "${log_dir}/modprobe.out" 2> "${log_dir}/modprobe.err"; then
			cat "${log_dir}/modprobe.err" >&2
			skip "netdevsim kernel module is unavailable"
		fi
	fi
	[[ -w /sys/bus/netdevsim/new_device ]] || skip "netdevsim new_device is not writable"

	for ((attempt = 0; attempt < 20; attempt++)); do
		candidate=$((10000 + RANDOM % 50000))
		[[ ! -e /sys/bus/netdevsim/devices/netdevsim${candidate} ]] && break
	done
	[[ ${attempt} -lt 20 ]] || fatal "failed to allocate a netdevsim device ID"

	create_log="${log_dir}/netdevsim-create.err"
	if ! { printf '%s 1 1\n' "${candidate}" > /sys/bus/netdevsim/new_device; } 2> "${create_log}"; then
		if ! { printf '%s 1\n' "${candidate}" > /sys/bus/netdevsim/new_device; } 2>> "${create_log}"; then
			cat "${create_log}" >&2
			skip "kernel rejected netdevsim device creation"
		fi
	fi

	# Only a successful creation grants permission to delete this device.
	NETDEVSIM_ID=${candidate}
	NETDEVSIM_DEVICE="netdevsim/netdevsim${NETDEVSIM_ID}"
	wait_until 5 1 netdevsim_device_ready \
		|| fatal "netdevsim devlink device did not appear: ${NETDEVSIM_DEVICE}"

	# Netdev registration precedes udev renaming; publish only the settled name.
	if ! udevadm settle --timeout=5 > "${log_dir}/netdevsim-udev.log" 2>&1; then
		cat "${log_dir}/netdevsim-udev.log" >&2
		fatal "netdevsim udev events did not settle; see ${log_dir}/netdevsim-udev.log"
	fi

	if ! devlink -j port show > "${log_dir}/netdevsim-ports.json" 2> "${log_dir}/netdevsim-port.err"; then
		cat "${log_dir}/netdevsim-ports.json" "${log_dir}/netdevsim-port.err" >&2
		fatal "cannot query netdevsim ports; see ${log_dir}/netdevsim-ports.json"
	fi
	NETDEVSIM_NETDEV=$(jq -er --arg prefix "${NETDEVSIM_DEVICE}/" '
		[.. | objects | to_entries[]? | select(.key | startswith($prefix))
		 | .value.netdev? | select(type == "string")][0] // empty
	' "${log_dir}/netdevsim-ports.json") || {
		cat "${log_dir}/netdevsim-ports.json" "${log_dir}/netdevsim-port.err" >&2
		fatal "netdevsim port has no associated netdev; see ${log_dir}/netdevsim-ports.json"
	}
}

netdevsim_destroy() {
	[[ -n "${NETDEVSIM_ID}" ]] || return 0
	if [[ -d /sys/bus/netdevsim/devices/netdevsim${NETDEVSIM_ID} ]]; then
		printf '%s\n' "${NETDEVSIM_ID}" > /sys/bus/netdevsim/del_device \
			|| return 1
	fi
	NETDEVSIM_ID=""
	NETDEVSIM_DEVICE=""
	NETDEVSIM_NETDEV=""
}
