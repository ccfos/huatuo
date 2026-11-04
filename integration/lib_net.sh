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

netdevsim_netdev_ready() {
	local netdev
	netdev=$(devlink -j port show | jq -er --arg prefix "${NETDEVSIM_DEVICE}/" '
		.port | to_entries[] | select(.key | startswith($prefix))
		| .value.netdev | select(type == "string" and length > 0)
	') || return 1
	[[ -e /sys/class/net/${netdev}/ifindex ]] || return 1
	NETDEVSIM_NETDEV=${netdev}
}

# Call in the current shell so cleanup retains ownership after partial setup.
netdevsim_create() {
	local log_dir=$1 candidate attempt create_log
	require_commands devlink jq
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
	# Older kernels accept only the device ID and port count.
	if ! {
		printf '%s 1 1\n' "${candidate}" > /sys/bus/netdevsim/new_device \
			|| printf '%s 1\n' "${candidate}" > /sys/bus/netdevsim/new_device
	} 2> "${create_log}"; then
		cat "${create_log}" >&2
		skip "kernel rejected netdevsim device creation"
	fi

	# Only a successful creation grants permission to delete this device.
	NETDEVSIM_ID=${candidate}
	NETDEVSIM_DEVICE="netdevsim/netdevsim${NETDEVSIM_ID}"
	# Only a running udev needs synchronization with asynchronous renaming.
	# Minimal systems without udev can use the kernel-assigned interface name.
	if [[ -S /run/udev/control ]]; then
		require_commands udevadm
		if ! udevadm settle --timeout=5 > "${log_dir}/netdevsim-udev.log" 2>&1; then
			cat "${log_dir}/netdevsim-udev.log" >&2
			fatal "netdevsim udev events did not settle; see ${log_dir}/netdevsim-udev.log"
		fi
	fi

	if ! wait_until 5 0.1 netdevsim_netdev_ready 2> "${log_dir}/netdevsim-port.err"; then
		devlink -j port show > "${log_dir}/netdevsim-ports.json" \
			2>> "${log_dir}/netdevsim-port.err" || true
		cat "${log_dir}/netdevsim-ports.json" "${log_dir}/netdevsim-port.err" >&2
		fatal "netdevsim port has no usable netdev after 5s: ${NETDEVSIM_DEVICE}"
	fi
}

netdevsim_destroy() {
	[[ -n "${NETDEVSIM_ID}" ]] || return 0
	if [[ -d /sys/bus/netdevsim/devices/netdevsim${NETDEVSIM_ID} ]] \
		&& ! printf '%s\n' "${NETDEVSIM_ID}" > /sys/bus/netdevsim/del_device; then
		log_error "cannot delete owned netdevsim device: ${NETDEVSIM_DEVICE}"
		return 1
	fi
	NETDEVSIM_ID=""
	NETDEVSIM_DEVICE=""
	NETDEVSIM_NETDEV=""
}

# Decode the numeric network counters used by these tests. Keep assertions in Shell.
netdev_metric_samples() {
	local file=$1 prefix=$2
	awk -v prefix="${prefix}" '
		function label(key, value) {
			if (!match($1, "[,{]" key "=\"[^\"]*\"")) return ""
			value = substr($1, RSTART, RLENGTH)
			sub(/^[^=]*="/, "", value); sub(/"$/, "", value)
			return value
		}
		index($1, prefix) == 1 {
			name = $1; sub(/\{.*/, "", name)
			printf "%s|%s|%s|%s|%s|%s|%s|%s\n", name, $2,
				label("device"), label("driver"), label("port"), label("index"),
				label("num_ports"), label("nodeguid")
		}
	' "${file}"
}

assert_netdev_collector_success() {
	local file=$1 collector=$2 actual
	actual=$(awk -v collector="${collector}" '
		$1 ~ /^huatuo_bamai_scrape_collector_success\{/ &&
		(index($1, "{collector=\"" collector "\"") || index($1, ",collector=\"" collector "\"")) { print $2 }
	' "${file}") || fatal "cannot parse collector status: ${file}"
	# Empty or repeated samples also differ from the single expected value.
	assert_eq "${actual}" 1 "${collector} scrape success" \
		|| fatal "expected one successful scrape for ${collector}; see ${file}"
}

assert_netdev_counter_type() {
	local file=$1 metric=$2 actual
	actual=$(awk -v metric="${metric}" '$1 == "#" && $2 == "TYPE" && $3 == metric { print $4 }' "${file}") \
		|| fatal "cannot parse metric type: ${file}"
	assert_eq "${actual}" counter "${metric} type" \
		|| fatal "expected one counter declaration for ${metric}; see ${file}"
}

assert_netdev_counter_value() {
	local actual=$1 description=$2 expected=${3:-} normalized
	[[ ${actual} =~ ^[0-9]+([.][0-9]+)?([eE][+]?[0-9]+)?$ ]] \
		|| fatal "${description}: expected nonnegative counter, actual=${actual}"
	[[ -n ${expected} ]] || return 0
	# Prometheus and rdma may render the same number in different notation.
	normalized=$(awk -v actual="${actual}" -v expected="${expected}" \
		'BEGIN { printf "%.17g|%.17g", actual, expected }') \
		|| fatal "cannot normalize ${description} counter value"
	assert_eq "${normalized%%|*}" "${normalized#*|}" "${description}" \
		|| fatal "${description}: actual=${actual}, expected=${expected}"
}
