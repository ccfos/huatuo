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

huatuo_bamai_await_metrics

check_metrics "exclude filter" \
	"memory_vmstat_thp_split_pmd" "memory_vmstat_balloon_inflate" \
	"netstat_Tcp_RetransSegs" "netstat_Tcp_CurrEstab" \
	'netdev_.*device="eth0"' 'netdev_.*device="eth1"' \
	-- \
	"memory_vmstat_thp_zero_page_alloc" "memory_vmstat_thp_swpout" \
	"netstat_Tcp_ActiveOpens" "netstat_TcpExt_TCPAutoCorking" \
	'netdev_.*device="docker0"'
