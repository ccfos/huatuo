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

# Exercise ethtool discovery, the real BPF hook and public metrics. Physical
# NICs use live counters; virtio uses controlled statistics with real sysfs
# trigger files. Driver selection and arithmetic boundaries have unit tests.
set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"

integration_isolate_run_directory "$@"

readonly NETDEV_HW_FIXTURES="${HUATUO_BAMAI_TEST_TMPDIR}/netdev-hw-fixtures"
readonly NETDEV_HW_HEADERS="${HUATUO_BAMAI_TEST_TMPDIR}/netdev-hw-metrics.headers"
readonly NETDEV_HW_METRICS="${HUATUO_BAMAI_TEST_TMPDIR}/netdev-hw-metrics.txt"

require_commands curl ss
kprobe_available carrier_down_count_show \
	|| skip "netdev_hw requires the carrier_down_count_show kprobe"

NETDEV_HW_PORT=$(allocate_available_port) || fatal "cannot allocate netdev_hw HTTP port"
readonly NETDEV_HW_PORT
HUATUO_BAMAI_ADDR="http://127.0.0.1:${NETDEV_HW_PORT}"
HUATUO_BAMAI_METRICS_API="${HUATUO_BAMAI_ADDR}/metrics"
CURL_TIMEOUT=(--connect-timeout 2 --max-time 5)

declare -A netdev_hw_drivers=()
netdev_hw_physical=()
netdev_hw_virtual=()
netdev_hw_pattern='.*'

for netdev_hw_entry in /sys/class/net/*; do
	[[ -L "${netdev_hw_entry}/device/driver" ]] || continue
	netdev_hw_driver=$(basename "$(readlink -f "${netdev_hw_entry}/device/driver")")
	netdev_hw_device=${netdev_hw_entry##*/}
	case ${netdev_hw_driver} in
	mlx5_core | i40e | ixgbe | bnxt_en)
		netdev_hw_physical+=("${netdev_hw_device}")
		;;
	virtio_net)
		netdev_hw_virtual+=("${netdev_hw_device}")
		;;
	*) continue ;;
	esac
	netdev_hw_drivers[${netdev_hw_device}]=${netdev_hw_driver}
done

netdev_hw_devices=("${netdev_hw_physical[@]}")
netdev_hw_mode=hardware
if [[ ${#netdev_hw_devices[@]} -eq 0 ]]; then
	netdev_hw_devices=("${netdev_hw_virtual[@]}")
	netdev_hw_mode=fixture
fi
[[ ${#netdev_hw_devices[@]} -gt 0 ]] \
	|| skip "netdev_hw needs mlx5_core, i40e, ixgbe, bnxt_en or virtio_net"
log_info "netdev_hw mode=${netdev_hw_mode}, devices=${netdev_hw_devices[*]}"

assert_netdev_hw_metrics_response() {
	curl -fsS "${CURL_TIMEOUT[@]}" -D "${NETDEV_HW_HEADERS}" -o "${NETDEV_HW_METRICS}" \
		"${HUATUO_BAMAI_METRICS_API}" \
		|| fatal "netdev_hw /metrics request failed"
	grep -Eqi '^Content-Type:[[:blank:]]*text/plain([;[:space:]]|$)' "${NETDEV_HW_HEADERS}" \
		|| fatal "netdev_hw /metrics: expected text/plain content type; see ${NETDEV_HW_HEADERS}"
}

netdev_hw_sample_ready() {
	local device
	huatuo_bamai_metrics > "${NETDEV_HW_METRICS}" || return 1
	for device in "${netdev_hw_devices[@]}"; do
		grep -Fq "[rx_sw_dropped_stats] ${device} => " "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" || return 1
	done
	grep -q '^huatuo_bamai_netdev_hw_rx_dropped_total' "${NETDEV_HW_METRICS}"
}

assert_netdev_hw_metrics() {
	local expected=$1 device driver
	assert_netdev_hw_metrics_response
	for device in "${netdev_hw_devices[@]}"; do
		driver=${netdev_hw_drivers[${device}]}
		awk -v device="${device}" -v driver="${driver}" -v expected="${expected}" '
			$1 ~ /^huatuo_bamai_scrape_collector_success\{/ && index($1, "collector=\"netdev_hw\"") {
				success = ($2 == 1)
			}
			$1 == "#" && $2 == "TYPE" && $3 == "huatuo_bamai_netdev_hw_rx_dropped_total" {
				counter = ($4 == "counter")
			}
			$1 ~ /^huatuo_bamai_netdev_hw_rx_dropped_total\{/ &&
			index($1, "device=\"" device "\"") && index($1, "driver=\"" driver "\"") {
				rows++
				if ($2 !~ /^[0-9]+([.][0-9]+)?([eE][+]?[0-9]+)?$/) bad = 1
				if (expected != "live" && $2 + 0 != expected + 0) bad = 1
			}
			END { exit !(success && counter && rows == 1 && !bad) }
		' "${NETDEV_HW_METRICS}" \
			|| fatal "netdev_hw ${device}/${driver}: expected counter=${expected}; see ${NETDEV_HW_METRICS}"

		# This message is emitted only after decoding a map entry for this NIC,
		# so a metric computed with an empty BPF map cannot satisfy the test.
		grep -Fq "[rx_sw_dropped_stats] ${device} => " "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
			|| fatal "netdev_hw ${device}: real BPF software-drop map entry was not observed"
	done
	grep '^huatuo_bamai_netdev_hw_rx_dropped_total' "${NETDEV_HW_METRICS}"
}

assert_netdev_hw_excluded() {
	local device
	# A successful empty scrape is also possible before BPF publication. Require
	# the actual discovery decision for NICs accepted by the preceding run.
	for device in "${netdev_hw_devices[@]}"; do
		grep -Fq "${device} is skipped (not in whitelist or driver not allowed)" \
			"${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
			|| fatal "netdev_hw ${device}: expected DeviceList exclusion; discovery did not report a skipped device"
	done
	assert_netdev_hw_metrics_response
	awk '
		$1 ~ /^huatuo_bamai_scrape_collector_success\{/ && index($1, "collector=\"netdev_hw\"") {
			success = ($2 == 1)
		}
		$1 ~ /^huatuo_bamai_netdev_hw_rx_dropped_total/ { rows++ }
		END { exit !(success && rows == 0) }
	' "${NETDEV_HW_METRICS}" \
		|| fatal "netdev_hw: expected successful scrape with no counters for excluded devices; see ${NETDEV_HW_METRICS}"
}

# 1. Retain the shared proc/dev fixtures and live NIC metadata. Only the two
# statistics below are replaced; reading carrier_down_count must reach sysfs.
cp -a "${HUATUO_BAMAI_TEST_FIXTURES}" "${NETDEV_HW_FIXTURES}"
rm -rf "${NETDEV_HW_FIXTURES}/sys/class/net"
mkdir -p "${NETDEV_HW_FIXTURES}/sys/class/net"
for netdev_hw_device in "${netdev_hw_devices[@]}"; do
	netdev_hw_dir="${NETDEV_HW_FIXTURES}/sys/class/net/${netdev_hw_device}"
	mkdir -p "${netdev_hw_dir}/statistics"
	for netdev_hw_file in /sys/class/net/"${netdev_hw_device}"/*; do
		[[ -f ${netdev_hw_file} ]] || continue
		ln -s "${netdev_hw_file}" "${netdev_hw_dir}/${netdev_hw_file##*/}"
	done
	for netdev_hw_file in /sys/class/net/"${netdev_hw_device}"/statistics/*; do
		ln -s "${netdev_hw_file}" "${netdev_hw_dir}/statistics/${netdev_hw_file##*/}"
	done
	if [[ ${netdev_hw_mode} == fixture ]]; then
		rm "${netdev_hw_dir}/statistics/rx_missed_errors" "${netdev_hw_dir}/statistics/rx_dropped"
		printf '7\n' > "${netdev_hw_dir}/statistics/rx_missed_errors"
		printf '100\n' > "${netdev_hw_dir}/statistics/rx_dropped"
	fi
done

# 2. Run the real daemon; readiness must be followed by an observed BPF entry.
integration_huatuo_bamai_start write_netdev_hw_config \
	--region dev --procfs-prefix "${NETDEV_HW_FIXTURES}" \
	--disable-storage --disable-kubelet --log-debug
wait_until 20 0.2 netdev_hw_sample_ready \
	|| fatal "netdev_hw did not expose a real BPF snapshot within 20s; see ${NETDEV_HW_METRICS} and huatuo.log"
netdev_hw_expected=live
[[ ${netdev_hw_mode} == hardware ]] || netdev_hw_expected=7
assert_netdev_hw_metrics "${netdev_hw_expected}"

# 3. Check the fallback path without manufacturing packet loss on the NIC.
if [[ ${netdev_hw_mode} == fixture ]]; then
	for netdev_hw_device in "${netdev_hw_devices[@]}"; do
		printf '0\n' > "${NETDEV_HW_FIXTURES}/sys/class/net/${netdev_hw_device}/statistics/rx_missed_errors"
		printf '0\n' > "${NETDEV_HW_FIXTURES}/sys/class/net/${netdev_hw_device}/statistics/rx_dropped"
	done
	assert_netdev_hw_metrics 0
fi
assert_huatuo_bamai_shutdown
assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" netdev_hw
mv "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${HUATUO_BAMAI_TEST_TMPDIR}/netdev-hw-enabled.log"

# 4. Restart with a whitelist matching no device and check the public result.
netdev_hw_pattern='huatuo-test-no-device'
integration_huatuo_bamai_start write_netdev_hw_config \
	--region dev --procfs-prefix "${NETDEV_HW_FIXTURES}" \
	--disable-storage --disable-kubelet --log-debug
assert_netdev_hw_excluded
assert_huatuo_bamai_shutdown
assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" netdev_hw
log_info "netdev_hw discovery, BPF snapshots, metrics and device exclusion passed"
