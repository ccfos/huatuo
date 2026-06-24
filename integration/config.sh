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

# write_default_config writes the baseline integration test config.
write_default_config() {
	cat >"${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" <<'EOF'
BlackList = ["metax_gpu", "ascend_npu", "softlockup", "ethtool", "netstat_hw", "iolatency", "memory_free", "memory_reclaim", "reschedipi", "softirq", "iotracing"]
EOF
}

# write_include_filter_config writes a config with metric include filters.
write_include_filter_config() {
	cat >"${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" <<'EOF'
BlackList = ["metax_gpu", "ascend_npu", "softlockup", "ethtool", "netstat_hw", "iolatency", "memory_free", "memory_reclaim", "reschedipi", "softirq", "iotracing"]

[MetricCollector.Vmstat]
    IncludedOnHost = "thp_split_pmd|thp_split_pud"
    ExcludedOnHost = ""
    IncludedOnContainer = ""
    ExcludedOnContainer = ""

[MetricCollector.Netstat]
    Included = "Tcp_RetransSegs|TcpExt_TCPLostRetransmit"
    Excluded = ""

[MetricCollector.NetdevStats]
    DeviceExcluded = ""
    DeviceIncluded = "eth0"

[MetricCollector.MountPointStat]
    MountPointsIncluded = "/boot"
EOF
}

# write_exclude_filter_config writes a config with metric exclude filters.
write_exclude_filter_config() {
	cat >"${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" <<'EOF'
BlackList = ["metax_gpu", "ascend_npu", "softlockup", "ethtool", "netstat_hw", "iolatency", "memory_free", "memory_reclaim", "reschedipi", "softirq", "iotracing"]

[MetricCollector.Vmstat]
    IncludedOnHost = ""
    ExcludedOnHost = "thp_zero_page_alloc|thp_swpout"
    IncludedOnContainer = ""
    ExcludedOnContainer = ""

[MetricCollector.Netstat]
    Included = ""
    Excluded = "Tcp_ActiveOpens|TcpExt_TCPAutoCorking"

[MetricCollector.NetdevStats]
    DeviceExcluded = "^(docker\\w*)$"
    DeviceIncluded = ""

[MetricCollector.MountPointStat]
    MountPointsIncluded = ""
EOF
}
