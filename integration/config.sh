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
	cat > "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" << 'EOF'
BlackList = ["metax_gpu", "ascend_npu", "softlockup", "ethtool", "netstat_hw", "iolatency", "memory_free", "memory_reclaim", "reschedipi", "softirq", "iotracing"]
EOF
}

# write_include_filter_config writes a config with metric include filters.
write_include_filter_config() {
	cat > "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" << 'EOF'
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
	cat > "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" << 'EOF'
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

# Unquoted heredoc: ${HUATUO_BAMAI_TEST_TMPDIR} must expand into Path,
# unlike the sibling write_*_config helpers which quote to prevent expansion.
write_net_rx_latency_config() {
	cat > "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" << EOF
BlackList = ["metax_gpu", "ascend_npu", "softlockup", "ethtool", "netstat_hw", "iolatency", "memory_free", "memory_reclaim", "reschedipi", "softirq", "iotracing", "dropwatch"]

[EventTracing.NetRxLatency]
    Driver2NetRx = 1
    Driver2TCP = 1
    Driver2Userspace = 1
    ExcludedHostNetnamespace = false

[Storage.LocalFile]
    Path = "${HUATUO_BAMAI_TEST_TMPDIR}/events"
EOF
}

# The apiserver port and workspace paths are allocated by the caller.
write_apiserver_apis_config() {
	cat > "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.conf" << EOF
[APIServer]
    TCPAddr = "127.0.0.1:${APISERVER_PORT}"

[TaskConfig]
    JobStoreDSN = "${HUATUO_BAMAI_TEST_TMPDIR}/jobs.db"

[[Auth.users]]
    ID = "${API_USER}"
    Name = "Integration administrator"
    IsAdmin = true
EOF
}

# The caller owns the API port, user ID, and expected profiling values.
write_apiserver_profile_capabilities_config() {
	cat > "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.conf" << EOF
[APIServer]
    TCPAddr = "127.0.0.1:${APISERVER_PORT}"

[TaskConfig]
    JobStoreDSN = "${HUATUO_BAMAI_TEST_TMPDIR}/jobs.db"

[[Auth.users]]
    ID = "${API_USER}"
    Name = "Integration administrator"
    IsAdmin = true

[Profiling]
    AggregationInterval = ${CAPABILITIES_AGGREGATION_INTERVAL_SECONDS}
    ExecutionTimeout = ${CAPABILITIES_EXECUTION_TIMEOUT_SECONDS}
    MaxProfilerProcs = ${CAPABILITIES_MAX_CONCURRENT_PROFILERS}
EOF
}

# The storage address and credentials are initialized by the calling test.
write_continuous_profiling_bamai_config() {
	cat > "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" << EOF
BlackList = ["metax_gpu", "ascend_npu", "softlockup", "ethtool", "netstat_hw", "iolatency", "memory_free", "memory_reclaim", "reschedipi", "softirq", "iotracing", "dropwatch"]

[Storage.ES]
    Address = "${ELASTICSEARCH_ADDR}"
    Username = "elastic"
    Password = "${ES_PASSWORD}"
    Index = "huatuo_continuous_profiling_test"

[Storage.LocalFile]
    Path = ""
EOF
}

# The API port, users, profiling interval, and storage are owned by the test.
write_continuous_profiling_apiserver_config() {
	cat > "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.conf" << EOF
[APIServer]
    TCPAddr = "127.0.0.1:${APISERVER_PORT}"

[ElasticSearch]
    Address = "${ELASTICSEARCH_ADDR}"
    Username = "elastic"
    Password = "${ES_PASSWORD}"
    Index = "huatuo_continuous_profiling_test"

[[Auth.users]]
    ID = "${API_USER}"
    Name = "Integration administrator"
    IsAdmin = true

[[Auth.users]]
    ID = "${OTHER_USER}"
    Name = "Integration user"
    Permissions = ["/v1/profiles", "/v1/profiles/**"]

[Profiling]
    AggregationInterval = ${PROFILE_INTERVAL}
    ExecutionTimeout = 20
    MaxProfilerProcs = 1
    FlameGraphBaseURL = "http://grafana.invalid/d"
EOF
}
