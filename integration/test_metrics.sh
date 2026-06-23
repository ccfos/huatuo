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

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

# Reconstruct array from environment variable (arrays cannot be exported directly)
if [[ -z "${HUATUO_BAMAI_ARGS_INTEGRATION:-}" ]]; then
	eval "HUATUO_BAMAI_ARGS_INTEGRATION=(${HUATUO_BAMAI_INTEGRATION_ARGS_STR})"
fi

fetch_huatuo_bamai_metrics() {
	huatuo_bamai_metrics >${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt
}

wait_and_fetch_metrics() {
	wait_until "${WAIT_HUATUO_BAMAI_TIMEOUT}" \
		"${WAIT_HUATUO_BAMAI_INTERVAL}" \
		"metrics endpoint ready" \
		fetch_huatuo_bamai_metrics
}

# Verify all expected metric files and dump metrics on success.
check_procfs_metrics() {
	for f in "${HUATUO_BAMAI_TEST_EXPECTED}"/*.txt; do
		prefix="$(basename "$f" .txt)"

		check_metrics_from_file "${f}"

		log_info "metric prefix ok: huatuo_bamai_${prefix}"
		grep "^huatuo_bamai_${prefix}" "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt" || log_info "(no metrics found)"
	done
}

check_metrics_from_file() {
	local file="$1"

	missing_metrics=$(
		grep -v '^[[:space:]]*\(#\|$\)' "${file}" |
			grep -Fvw -f "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt" || true
	)

	if [[ -z "${missing_metrics}" ]]; then
		return
	fi

	log_info "the missing metrics:"
	log_info "${missing_metrics}"
	log_info "the metrics file ${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt:"
	log_info "$(cat ${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt)"
	exit 1
}

test_huatuo_bamai_metrics() {
	log_info "========== Phase 1: normal metrics =========="
	wait_and_fetch_metrics
	check_procfs_metrics
	log_info "========== Phase 1: normal metrics passed =========="
}

# Vmstat: IncludedOnHost = "thp_split_pmd|thp_split_pud"
check_vmstat_include_filter() {
	local metrics_file="${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt"
	local -a must_present=("memory_vmstat_thp_split_pmd" "memory_vmstat_thp_split_pud")
	local -a must_absent=("memory_vmstat_thp_zero_page_alloc" "memory_vmstat_thp_swpout" "memory_vmstat_balloon_inflate" "memory_vmstat_balloon_deflate" "memory_vmstat_swap_ra " "memory_vmstat_swap_ra_hit")

	for pattern in "${must_present[@]}"; do
		if ! grep -q "huatuo_bamai_${pattern}" "$metrics_file"; then
			fatal "vmstat include filter: expected '${pattern}' present but not found"
			exit 1
		fi
	done
	for pattern in "${must_absent[@]}"; do
		if grep -q "huatuo_bamai_${pattern}" "$metrics_file"; then
			fatal "vmstat include filter: expected '${pattern}' absent but found"
			exit 1
		fi
	done
	log_info "vmstat include filter ok"
}

# Netstat: Included = "Tcp_RetransSegs|TcpExt_TCPLostRetransmit"
check_netstat_include_filter() {
	local metrics_file="${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt"
	local -a must_present=("netstat_Tcp_RetransSegs" "netstat_TcpExt_TCPLostRetransmit")
	local -a must_absent=("netstat_Tcp_ActiveOpens" "netstat_TcpExt_TCPAutoCorking" "netstat_TcpExt_TCPTimeouts" "netstat_Tcp_CurrEstab")

	for pattern in "${must_present[@]}"; do
		if ! grep -q "huatuo_bamai_${pattern}" "$metrics_file"; then
			fatal "netstat include filter: expected '${pattern}' present but not found"
			exit 1
		fi
	done
	for pattern in "${must_absent[@]}"; do
		if grep -q "huatuo_bamai_${pattern}" "$metrics_file"; then
			fatal "netstat include filter: expected '${pattern}' absent but found"
			exit 1
		fi
	done
	log_info "netstat include filter ok"
}

# MountPointStat: MountPointsIncluded = "/boot"
check_mountpoint_include_filter() {
	local metrics_file="${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt"
	local -a must_present=("mountpoint_perm_ro{.*mountpoint=\"/boot\"")
	local -a must_absent=("mountpoint_perm_ro{.*mountpoint=\"/sys/fs/cgroup\"" "mountpoint_perm_ro{.*mountpoint=\"/home/root/containers")

	for pattern in "${must_present[@]}"; do
		if ! grep -qE "huatuo_bamai_${pattern}" "$metrics_file"; then
			fatal "mountpoint include filter: expected '${pattern}' present but not found"
			exit 1
		fi
	done
	for pattern in "${must_absent[@]}"; do
		if grep -qE "huatuo_bamai_${pattern}" "$metrics_file"; then
			fatal "mountpoint include filter: expected '${pattern}' absent but found"
			exit 1
		fi
	done
	log_info "mountpoint include filter ok"
}

# Netdev: DeviceIncluded = "eth0"
check_netdev_include_filter() {
	local metrics_file="${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt"
	local -a must_present=("netdev_.*device=\"eth0\"")
	local -a must_absent=("netdev_.*device=\"eth1\"" "netdev_.*device=\"docker0\"")

	for pattern in "${must_present[@]}"; do
		if ! grep -qE "huatuo_bamai_${pattern}" "$metrics_file"; then
			fatal "netdev include filter: expected '${pattern}' present but not found"
			exit 1
		fi
	done
	for pattern in "${must_absent[@]}"; do
		if grep -qE "huatuo_bamai_${pattern}" "$metrics_file"; then
			fatal "netdev include filter: expected '${pattern}' absent but found"
			exit 1
		fi
	done
	log_info "netdev include filter ok"
}

check_include_filter_metrics() {
	check_vmstat_include_filter
	check_netstat_include_filter
	check_netdev_include_filter
	check_mountpoint_include_filter
}

# Vmstat: ExcludedOnHost = "thp_zero_page_alloc|thp_swpout"
check_vmstat_exclude_filter() {
	local metrics_file="${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt"
	local -a must_present=("memory_vmstat_thp_split_pmd" "memory_vmstat_balloon_inflate")
	local -a must_absent=("memory_vmstat_thp_zero_page_alloc" "memory_vmstat_thp_swpout")

	for pattern in "${must_present[@]}"; do
		if ! grep -q "huatuo_bamai_${pattern}" "$metrics_file"; then
			fatal "vmstat exclude filter: expected '${pattern}' present but not found"
			exit 1
		fi
	done
	for pattern in "${must_absent[@]}"; do
		if grep -q "huatuo_bamai_${pattern}" "$metrics_file"; then
			fatal "vmstat exclude filter: expected '${pattern}' absent but found"
			exit 1
		fi
	done
	log_info "vmstat exclude filter ok"
}

# Netstat: Excluded = "Tcp_ActiveOpens|TcpExt_TCPAutoCorking"
check_netstat_exclude_filter() {
	local metrics_file="${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt"
	local -a must_present=("netstat_Tcp_RetransSegs" "netstat_Tcp_CurrEstab")
	local -a must_absent=("netstat_Tcp_ActiveOpens" "netstat_TcpExt_TCPAutoCorking")

	for pattern in "${must_present[@]}"; do
		if ! grep -q "huatuo_bamai_${pattern}" "$metrics_file"; then
			fatal "netstat exclude filter: expected '${pattern}' present but not found"
			exit 1
		fi
	done
	for pattern in "${must_absent[@]}"; do
		if grep -q "huatuo_bamai_${pattern}" "$metrics_file"; then
			fatal "netstat exclude filter: expected '${pattern}' absent but found"
			exit 1
		fi
	done
	log_info "netstat exclude filter ok"
}

# Netdev: DeviceExcluded = "^(docker\w*)$"
check_netdev_exclude_filter() {
	local metrics_file="${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt"
	local -a must_present=("netdev_.*device=\"eth0\"" "netdev_.*device=\"eth1\"")
	local -a must_absent=("netdev_.*device=\"docker0\"")

	for pattern in "${must_present[@]}"; do
		if ! grep -qE "huatuo_bamai_${pattern}" "$metrics_file"; then
			fatal "netdev exclude filter: expected '${pattern}' present but not found"
			exit 1
		fi
	done
	for pattern in "${must_absent[@]}"; do
		if grep -qE "huatuo_bamai_${pattern}" "$metrics_file"; then
			fatal "netdev exclude filter: expected '${pattern}' absent but found"
			exit 1
		fi
	done
	log_info "netdev exclude filter ok"
}

check_exclude_filter_metrics() {
	check_vmstat_exclude_filter
	check_netstat_exclude_filter
	check_netdev_exclude_filter
}

# Phase 2a: include filter test
test_huatuo_bamai_include_filter_metrics() {
	log_info "========== Phase 2a: include filter tests =========="

	huatuo_bamai_stop

	cp "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
		"${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-phase1.log" 2>/dev/null || true

	integration_test_huatuo_bamai_include_filter_config
	huatuo_bamai_start "${HUATUO_BAMAI_ARGS_INTEGRATION[@]}"
	log_info "huatuo-bamai restarted with include filter config"

	wait_and_fetch_metrics
	check_include_filter_metrics

	huatuo_bamai_stop

	log_info "========== Phase 2a: include filter tests passed =========="
}

# Phase 2b: exclude filter test
test_huatuo_bamai_exclude_filter_metrics() {
	log_info "========== Phase 2b: exclude filter tests =========="

	cp "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
		"${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-phase2a.log" 2>/dev/null || true

	integration_test_huatuo_bamai_exclude_filter_config
	huatuo_bamai_start "${HUATUO_BAMAI_ARGS_INTEGRATION[@]}"
	log_info "huatuo-bamai restarted with exclude filter config"

	wait_and_fetch_metrics
	check_exclude_filter_metrics

	huatuo_bamai_stop

	log_info "========== Phase 2b: exclude filter tests passed =========="
}

# Phase 1: normal metrics
test_huatuo_bamai_metrics

# Phase 2a: include filter metrics
test_huatuo_bamai_include_filter_metrics

# Phase 2b: exclude filter metrics
test_huatuo_bamai_exclude_filter_metrics
