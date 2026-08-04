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

# Verify metric exclude filters: matched items are absent from output.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"

integration_huatuo_bamai_start write_exclude_filter_config

# The metrics endpoint can become ready before the netdev collector publishes
# its first sample on slower VMs. Wait for both fixture interfaces so the
# filter assertions do not race collector initialization.
collect_exclude_filter_metrics() {
	huatuo_bamai_collect_metrics || return 1
	grep -qE 'huatuo_bamai_netdev_.*device="eth0"' "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt" \
		&& grep -qE 'huatuo_bamai_netdev_.*device="eth1"' "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt"
}

wait_until "${WAIT_HUATUO_BAMAI_TIMEOUT}" \
	"${WAIT_HUATUO_BAMAI_INTERVAL}" \
	collect_exclude_filter_metrics

check_metrics "exclude filter" \
	"memory_vmstat_thp_split_pmd" \
	'netdev_.*device="eth0"' 'netdev_.*device="eth1"' \
	-- \
	"memory_vmstat_thp_zero_page_alloc" "memory_vmstat_thp_swpout" \
	"netstat_Tcp_ActiveOpens" "netstat_TcpExt_TCPAutoCorking" \
	'netdev_.*device="docker0"'
