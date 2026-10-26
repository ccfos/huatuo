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

integration_isolate_run_directory "$@"

readonly NETDEV_HW_FIXTURES="${HUATUO_BAMAI_TEST_TMPDIR}/netdev-hw-fixtures"
readonly NETDEV_HW_HEADERS="${HUATUO_BAMAI_TEST_TMPDIR}/netdev-hw-metrics.headers"
readonly NETDEV_HW_METRICS="${HUATUO_BAMAI_TEST_TMPDIR}/netdev-hw-metrics.txt"
readonly NETDEV_HW_STATUS="${HUATUO_BAMAI_TEST_TMPDIR}/netdev-hw-metrics.status"
readonly NETDEV_HW_ERRORS="${HUATUO_BAMAI_TEST_TMPDIR}/netdev-hw-metrics.err"

require_commands curl ss
kprobe_available carrier_down_count_show \
	|| skip "netdev_hw requires the carrier_down_count_show kprobe"

NETDEV_HW_PORT=$(allocate_available_port) || fatal "cannot allocate netdev_hw HTTP port"
readonly NETDEV_HW_PORT
HUATUO_BAMAI_ADDR="http://127.0.0.1:${NETDEV_HW_PORT}"
HUATUO_BAMAI_METRICS_API="${HUATUO_BAMAI_ADDR}/metrics"
CURL_TIMEOUT=(--connect-timeout 2 --max-time 5)

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
	netdevsim_destroy || status=1
	return "${status}"
}
trap 'netdev_hw_exit_status=$?
if ! cleanup && [[ ${netdev_hw_exit_status} == 0 || ${netdev_hw_exit_status} == 77 ]]; then
	netdev_hw_exit_status=1
fi
exit "${netdev_hw_exit_status}"' EXIT

netdevsim_create "${HUATUO_BAMAI_TEST_TMPDIR}"
netdev_hw_pattern="^${NETDEVSIM_NETDEV}$"
log_info "netdev_hw netdevsim device=${NETDEVSIM_NETDEV}"

assert_netdev_hw_initial_stats() {
	local stat value
	for stat in rx_missed_errors rx_dropped; do
		value=$(cat "/sys/class/net/${NETDEVSIM_NETDEV}/statistics/${stat}") \
			|| fatal "cannot read netdevsim ${stat}"
		[[ ${value} == 0 ]] || fatal "fresh netdevsim ${stat}=${value}, expected 0"
	done
}

netdev_hw_fetch_metrics() {
	local http_status curl_status=0 artifact
	# curl may leave an existing output file untouched on connection failure.
	for artifact in "${NETDEV_HW_HEADERS}" "${NETDEV_HW_METRICS}" \
		"${NETDEV_HW_STATUS}" "${NETDEV_HW_ERRORS}"; do
		: > "${artifact}" || return 1
	done

	http_status=$(curl -sS "${CURL_TIMEOUT[@]}" -D "${NETDEV_HW_HEADERS}" -o "${NETDEV_HW_METRICS}" \
		-w '%{http_code}' "${HUATUO_BAMAI_METRICS_API}" 2> "${NETDEV_HW_ERRORS}") || curl_status=$?
	printf 'curl_exit=%s\nhttp_status=%s\n' "${curl_status}" "${http_status}" > "${NETDEV_HW_STATUS}" || return 1

	[[ ${curl_status} -eq 0 && ${http_status} == 200 ]]
}

assert_netdev_hw_metrics_response() {
	if ! netdev_hw_fetch_metrics; then
		cat "${NETDEV_HW_STATUS}" "${NETDEV_HW_HEADERS}" "${NETDEV_HW_METRICS}" "${NETDEV_HW_ERRORS}" >&2
		printf '\n' >&2
		fatal "netdev_hw /metrics request failed; see ${NETDEV_HW_STATUS}"
	fi
	grep -Eqi '^Content-Type:[[:blank:]]*text/plain([;[:space:]]|$)' "${NETDEV_HW_HEADERS}" \
		|| fatal "netdev_hw /metrics: expected text/plain content type; see ${NETDEV_HW_HEADERS}"
}

netdev_hw_sample_ready() {
	netdev_hw_fetch_metrics || return 1
	grep -Fq "[rx_sw_dropped_stats] ${NETDEVSIM_NETDEV} => " "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" || return 1
	grep -q '^huatuo_bamai_netdev_hw_rx_dropped_total' "${NETDEV_HW_METRICS}"
}

assert_netdev_hw_metrics() {
	local expected=$1
	assert_netdev_hw_metrics_response
	awk -v device="${NETDEVSIM_NETDEV}" -v driver="netdevsim" -v expected="${expected}" '
		function has_label(key, value) {
			return index($1, "{" key "=\"" value "\"") || index($1, "," key "=\"" value "\"")
		}
		$1 ~ /^huatuo_bamai_scrape_collector_success\{/ && has_label("collector", "netdev_hw") {
			success = ($2 == 1)
		}
		$1 == "#" && $2 == "TYPE" && $3 == "huatuo_bamai_netdev_hw_rx_dropped_total" {
			counter = ($4 == "counter")
		}
		$1 ~ /^huatuo_bamai_netdev_hw_rx_dropped_total\{/ &&
		has_label("device", device) && has_label("driver", driver) {
			rows++
			if ($2 !~ /^[0-9]+([.][0-9]+)?([eE][+]?[0-9]+)?$/) bad = 1
			if ($2 + 0 != expected + 0) bad = 1
		}
		END { exit !(success && counter && rows == 1 && !bad) }
	' "${NETDEV_HW_METRICS}" \
		|| fatal "netdev_hw ${NETDEVSIM_NETDEV}/netdevsim: expected counter=${expected}; see ${NETDEV_HW_METRICS}"

	# This message is emitted only after decoding a map entry for this NIC,
	# so a metric computed with an empty BPF map cannot satisfy the test.
	grep -Fq "[rx_sw_dropped_stats] ${NETDEVSIM_NETDEV} => " "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
		|| fatal "netdev_hw ${NETDEVSIM_NETDEV}: real BPF software-drop map entry was not observed"
	grep '^huatuo_bamai_netdev_hw_rx_dropped_total' "${NETDEV_HW_METRICS}"
}

assert_netdev_hw_excluded() {
	# Require discovery evidence; an empty scrape before BPF publication is insufficient.
	grep -Fq "${NETDEVSIM_NETDEV} is skipped (not in whitelist or driver not allowed)" \
		"${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
		|| fatal "netdev_hw ${NETDEVSIM_NETDEV}: discovery did not report DeviceList exclusion"
	assert_netdev_hw_metrics_response
	awk '
		$1 ~ /^huatuo_bamai_scrape_collector_success\{/ && ($1 ~ /[,{]collector="netdev_hw"/) {
			success = ($2 == 1)
		}
		$1 ~ /^huatuo_bamai_netdev_hw_rx_dropped_total/ { rows++ }
		END { exit !(success && rows == 0) }
	' "${NETDEV_HW_METRICS}" \
		|| fatal "netdev_hw: expected successful scrape with no counters for excluded devices; see ${NETDEV_HW_METRICS}"
}

# 1. Start with live sysfs links; a fresh simulated device has no RX drops.
assert_netdev_hw_initial_stats
cp -a "${HUATUO_BAMAI_TEST_FIXTURES}" "${NETDEV_HW_FIXTURES}"
rm -rf "${NETDEV_HW_FIXTURES}/sys/class/net"
mkdir -p "${NETDEV_HW_FIXTURES}/sys/class/net"
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
write_config_bamai_netdev_hw > "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf"
integration_huatuo_bamai_start : \
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
write_config_bamai_netdev_hw > "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf"
integration_huatuo_bamai_start : \
	--region dev --procfs-prefix "${NETDEV_HW_FIXTURES}" \
	--disable-storage --disable-kubelet --log-debug
assert_netdev_hw_excluded
assert_huatuo_bamai_shutdown
assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" netdev_hw
log_info "netdev_hw discovery, BPF snapshots, metrics and device exclusion passed"
