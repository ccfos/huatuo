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

# Exercise real netdevsim discovery, sysfs, BPF snapshots and public metrics.
# Controlled statistics then check nonzero values without changing kernel data.
set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_net.sh"
source "${ROOT_DIR}/integration/config.sh"

readonly NETDEV_HW_FIXTURES="${HUATUO_BAMAI_TEST_TMPDIR}/netdev-hw-fixtures"
readonly NETDEV_HW_RESPONSE="${HUATUO_BAMAI_TEST_TMPDIR}/netdev-hw-metrics"
readonly NETDEV_HW_METRICS="${NETDEV_HW_RESPONSE}.txt"

require_commands curl ss awk grep
kprobe_available carrier_down_count_show \
	|| skip "netdev_hw requires the carrier_down_count_show kprobe"

NETDEV_HW_PORT=$(allocate_available_port) || fatal "cannot allocate netdev_hw HTTP port"
readonly NETDEV_HW_PORT
HUATUO_BAMAI_ADDR="http://127.0.0.1:${NETDEV_HW_PORT}"
HUATUO_BAMAI_METRICS_API="${HUATUO_BAMAI_ADDR}/metrics"

cleanup() {
	huatuo_bamai_stop || return 1
	netdevsim_destroy
}
trap 'netdev_hw_exit_status=$?
if ! cleanup && [[ ${netdev_hw_exit_status} == 0 || ${netdev_hw_exit_status} == 77 ]]; then
	netdev_hw_exit_status=1
fi
exit "${netdev_hw_exit_status}"' EXIT

assert_netdev_hw_initial_stats() {
	local stat value
	for stat in rx_missed_errors rx_dropped; do
		value=$(cat "/sys/class/net/${NETDEVSIM_NETDEV}/statistics/${stat}") \
			|| fatal "cannot read netdevsim ${stat}"
		[[ ${value} == 0 ]] || fatal "fresh netdevsim ${stat}=${value}, expected 0"
	done
}

netdev_hw_sample_ready() {
	fetch_metrics_response "${NETDEV_HW_RESPONSE}" || return 1
	grep -Fq "[rx_sw_dropped_stats] ${NETDEVSIM_NETDEV} => " "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" || return 1
	grep -q '^huatuo_bamai_netdev_hw_rx_dropped_total' "${NETDEV_HW_METRICS}"
}

assert_netdev_hw_metrics() {
	local expected=$1 samples name value device driver rest
	local metric=huatuo_bamai_netdev_hw_rx_dropped_total
	assert_metrics_response "${NETDEV_HW_RESPONSE}"
	assert_netdev_collector_success "${NETDEV_HW_METRICS}" netdev_hw
	assert_netdev_counter_type "${NETDEV_HW_METRICS}" "${metric}"
	samples=$(netdev_metric_samples "${NETDEV_HW_METRICS}" "${metric}") \
		|| fatal "cannot parse netdev_hw metrics: ${NETDEV_HW_METRICS}"
	[[ -n ${samples} && ${samples} != *$'\n'* ]] \
		|| fatal "expected one netdev_hw sample, actual=${samples:-empty}; see ${NETDEV_HW_METRICS}"
	IFS='|' read -r name value device driver rest <<< "${samples}"
	assert_eq "${name}/${device}/${driver}" "${metric}/${NETDEVSIM_NETDEV}/netdevsim" "hardware drop sample" \
		|| fatal "unexpected netdev_hw sample: ${name}/${device}/${driver}"
	assert_netdev_counter_value "${value}" "${device} hardware drops" "${expected}"

	# An empty BPF map must not satisfy the test through a sysfs-only result.
	grep -Fq "[rx_sw_dropped_stats] ${NETDEVSIM_NETDEV} => " "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
		|| fatal "netdev_hw ${NETDEVSIM_NETDEV}: real BPF software-drop map entry was not observed"
	grep '^huatuo_bamai_netdev_hw_rx_dropped_total' "${NETDEV_HW_METRICS}"
}

assert_netdev_hw_excluded() {
	# Require discovery evidence; an empty scrape before BPF publication is insufficient.
	grep -Fq "${NETDEVSIM_NETDEV} is skipped (not in whitelist or driver not allowed)" \
		"${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
		|| fatal "netdev_hw ${NETDEVSIM_NETDEV}: discovery did not report DeviceList exclusion"
	assert_metrics_response "${NETDEV_HW_RESPONSE}"
	assert_netdev_collector_success "${NETDEV_HW_METRICS}" netdev_hw
	local samples
	samples=$(netdev_metric_samples "${NETDEV_HW_METRICS}" huatuo_bamai_netdev_hw_rx_dropped_total) \
		|| fatal "cannot parse netdev_hw metrics: ${NETDEV_HW_METRICS}"
	assert_eq "${samples}" "" "excluded netdev_hw samples" \
		|| fatal "excluded devices produced counters; see ${NETDEV_HW_METRICS}"
}

netdevsim_create "${HUATUO_BAMAI_TEST_TMPDIR}"
netdev_hw_pattern="^${NETDEVSIM_NETDEV}$"
log_info "netdev_hw netdevsim device=${NETDEVSIM_NETDEV}"

# 1. Start with live sysfs links; a fresh simulated device has no RX drops.
assert_netdev_hw_initial_stats
cp -a "${HUATUO_BAMAI_TEST_FIXTURES}" "${NETDEV_HW_FIXTURES}"
rm -rf "${NETDEV_HW_FIXTURES}/sys/class/net"
netdev_hw_dir="${NETDEV_HW_FIXTURES}/sys/class/net/${NETDEVSIM_NETDEV}"
mkdir -p "${netdev_hw_dir}/statistics"
for netdev_hw_file in /sys/class/net/"${NETDEVSIM_NETDEV}"/*; do
	[[ -f ${netdev_hw_file} ]] || continue
	ln -s "${netdev_hw_file}" "${netdev_hw_dir}/${netdev_hw_file##*/}"
done
for netdev_hw_file in /sys/class/net/"${NETDEVSIM_NETDEV}"/statistics/*; do
	ln -s "${netdev_hw_file}" "${netdev_hw_dir}/statistics/${netdev_hw_file##*/}"
done

# 2. Run the real daemon; readiness must be followed by an observed BPF entry.
integration_huatuo_bamai_start write_config_bamai_netdev_hw \
	--region dev --procfs-prefix "${NETDEV_HW_FIXTURES}" \
	--disable-storage --disable-kubelet --log-debug
wait_until 20 0.2 netdev_hw_sample_ready \
	|| fatal "netdev_hw did not expose a real BPF snapshot within 20s; see ${NETDEV_HW_METRICS} and huatuo.log"
assert_netdev_hw_metrics 0

# 3. Replace only the statistics files. The BPF trigger still reaches sysfs.
netdev_hw_stats="${netdev_hw_dir}/statistics"
rm "${netdev_hw_stats}/rx_missed_errors" "${netdev_hw_stats}/rx_dropped"
printf '7\n' > "${netdev_hw_stats}/rx_missed_errors"
printf '100\n' > "${netdev_hw_stats}/rx_dropped"
assert_netdev_hw_metrics 7

printf '0\n' > "${netdev_hw_stats}/rx_missed_errors"
assert_netdev_hw_metrics 100

printf '0\n' > "${netdev_hw_stats}/rx_dropped"
assert_netdev_hw_metrics 0
assert_huatuo_bamai_shutdown
assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" netdev_hw
mv "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${HUATUO_BAMAI_TEST_TMPDIR}/netdev-hw-enabled.log"

# 4. Restart with a whitelist matching no device and check the public result.
netdev_hw_pattern='huatuo-test-no-device'
integration_huatuo_bamai_start write_config_bamai_netdev_hw \
	--region dev --procfs-prefix "${NETDEV_HW_FIXTURES}" \
	--disable-storage --disable-kubelet --log-debug
assert_netdev_hw_excluded
assert_huatuo_bamai_shutdown
assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" netdev_hw
log_info "netdev_hw discovery, BPF snapshots, metrics and device exclusion passed"
