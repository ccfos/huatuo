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

# Configuration fragments write to stdout; callers own output files and resources.
write_config_blacklist() {
	local separator="" collector
	printf 'BlackList = ['
	for collector in "$@"; do
		printf '%s"%s"' "${separator}" "${collector}"
		separator=", "
	done
	printf ']\n'
}

# Keep collector isolation in one place; cases name the collectors they retain.
# Preserve explicit exceptions such as xfs and tracing_status at each call site.
write_config_blacklist_except() {
	local collector enabled
	local -a blacklist=()
	for collector in \
		arp ascend_npu blk_throtl cpu_stat cpu_util \
		cpuidle cpusys diskio dload dropwatch \
		hungtask io_health iocost iolatency iotracing \
		irqtracing loadavg memburst memory_buddyinfo memory_events \
		memory_free memory_oom_kill memory_others memory_reclaim memory_reclaim_events \
		memory_threshold_snapshot memory_vmstat metax_gpu mountpoint_perm mthreads_gpu \
		mthreads_xid net_conntrack net_rx_latency netdev netdev_bonding_lacp \
		netdev_dcb netdev_events netdev_hw netdev_qdisc netdev_rdma_link \
		netdev_txqueue_timeout netstat ras runqlat sched-blame \
		sched_tick sockstat softirq softlockup tcp_memory \
		tcp_retransmit tracing_status xfs; do
		for enabled in "$@"; do
			[[ ${collector} != "${enabled}" ]] || continue 2
		done
		blacklist+=("${collector}")
	done
	write_config_blacklist "${blacklist[@]}"
}

write_config_bamai_kubelet() {
	local port=$1 certificate=$2 key=$3
	cat << EOF

[Pod]
    KubeletReadOnlyPort = 0
    KubeletAuthorizedPort = ${port}
    KubeletClientCertPath = "${certificate},${key}"
EOF
}

write_config_bamai_auth() {
	local token=$1
	cat << EOF

[HTTPServer.Auth]
    BearerToken = "${token}"
EOF
}

write_config_bamai_http() {
	local address=${HUATUO_BAMAI_DEFAULT_ADDR:-}
	address=${address#http://}
	if (($#)); then
		[[ -n $1 ]] || {
			log_error "explicit bamai HTTP listen address must not be empty"
			return 1
		}
		address=$1
	fi
	printf '\n[HTTPServer]\n'
	# Prepared configs may precede address initialization in the shared launcher.
	[[ -z $address ]] || printf '    ListenAddress = "%s"\n' "$address"
}

write_config_storage_local() {
	local path=$1
	cat << EOF

[Storage.LocalFile]
    Path = "${path}"
EOF
}

# Missing Index and an explicit empty Index retain their distinct TOML values.
write_config_elasticsearch() {
	local section=$1 address=$2 username=$3 password=$4
	cat << EOF

[${section}]
    Address = "${address}"
    Username = "${username}"
    Password = "${password}"
EOF
	if [[ $# -eq 5 ]]; then
		printf '    Index = "%s"\n' "$5"
	fi
}

write_config_bamai_elasticsearch() {
	write_config_elasticsearch Storage.Elasticsearch "$@"
}

write_config_apiserver_elasticsearch() {
	write_config_elasticsearch Elasticsearch "$@"
}

write_config_apiserver_http() {
	local address=$1
	cat << EOF

[APIServer]
    ListenAddress = "${address}"
EOF
}

write_config_apiserver_agent() {
	local port=$1 token=$2
	cat << EOF

[Agent]
    HTTPPort = ${port}

[Agent.Auth]
    BearerToken = "${token}"
EOF
}

write_config_apiserver_admin() {
	local token=$1
	cat << EOF

[[Auth.Users]]
    ID = "integration-admin-user"
    BearerToken = "${token}"
    Admin = true
EOF
}

write_config_bamai_default() {
	write_config_blacklist \
		"memory_threshold_snapshot" "metax_gpu" "ascend_npu" "softlockup" "ethtool" \
		"netstat_hw" "iolatency" "memory_free" "memory_reclaim" "reschedipi" \
		"softirq" "iotracing" "iocost" "blk_throtl" "sched-blame"
	write_config_bamai_auth "integration-node-token"
}

write_config_bamai_netdev() {
	local port=$1
	write_config_bamai_default
	write_config_bamai_http "127.0.0.1:${port}"
}

# The case supplies its port and device selection before startup.
write_config_bamai_netdev_hw() {
	write_config_bamai_netdev "${NETDEV_HW_PORT}"
	cat << EOF
[MetricCollector.NetdevHW]
    DeviceList = ["${netdev_hw_pattern}"]
EOF
}

write_config_bamai_netdev_rdma_link() {
	write_config_bamai_netdev "${RDMA_PORT}"
}

write_config_bamai_include_filter() {
	write_config_bamai_default
	cat << 'EOF'
[MetricCollector.Vmstat]
    IncludedOnHost = "pgfault"
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

write_config_bamai_exclude_filter() {
	write_config_bamai_default
	cat << 'EOF'
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

write_config_bamai_net_rx_latency() {
	write_config_blacklist \
		"memory_threshold_snapshot" "metax_gpu" "ascend_npu" "softlockup" "ethtool" \
		"netstat_hw" "iolatency" "memory_free" "memory_reclaim" "reschedipi" \
		"softirq" "iotracing" "dropwatch" "iocost" "blk_throtl" \
		"sched-blame"
	write_config_bamai_auth "integration-node-token"
	cat << EOF

[EventTracing.NetRxLatency]
    Driver2NetRx = 1
    Driver2TCP = 1
    Driver2Userspace = 1
    ExcludedHostNetnamespace = false

EOF
	write_config_storage_local "${HUATUO_BAMAI_TEST_TMPDIR}/events"
}

write_config_bamai_lacp_events() {
	# The caller initializes and owns HUATUO_BAMAI_TEST_TMPDIR; expand its storage path.
	write_config_blacklist_except netdev_bonding_lacp
	write_config_bamai_auth "integration-node-token"
	write_config_storage_local "${HUATUO_BAMAI_TEST_TMPDIR}/events"
}

# Keep sched_tick tests isolated while allowing each scenario to set its threshold.
write_config_bamai_sched_tick_threshold() {
	local interval_threshold=$1

	write_config_blacklist_except mthreads_gpu mthreads_xid net_conntrack sched_tick tracing_status xfs

	write_config_bamai_auth "integration-node-token"

	cat << EOF

[EventTracing.SchedTick]
    IntervalThreshold = ${interval_threshold}

EOF

	write_config_storage_local "${HUATUO_BAMAI_TEST_TMPDIR}/events"
}

# Keep the lifecycle test focused on load and attachment.
write_config_bamai_sched_tick() {
	write_config_bamai_sched_tick_threshold 60000000000
}

# Let normal ticks exercise the irqoff reporting path without disabling IRQs.
write_config_bamai_sched_tick_irqoff() {
	write_config_bamai_sched_tick_threshold 1
}

write_config_bamai_storage_events() {
	# Tests supply isolated ports, packet filter, event limits and optional backend credentials.
	write_config_blacklist_except dropwatch net_conntrack xfs
	write_config_bamai_http
	write_config_bamai_auth "integration-node-token"
	cat << EOF
[EventTracing.Dropwatch]
    Filter = "udp and dst host 127.0.0.99 and dst port ${storage_target_port}"
    MaxEventsPerSecond = ${STORAGE_EVENT_RATE}
EOF
	write_config_storage_local "${HUATUO_BAMAI_TEST_TMPDIR}/events"
	cat << EOF
    RotationSizeMiB = ${STORAGE_ROTATION_MIB}
    MaxRotatedFiles = ${STORAGE_MAX_ROTATED_FILES}
EOF
	write_config_bamai_elasticsearch "${STORAGE_ADDR:-}" "${STORAGE_USERNAME:-}" "${STORAGE_PASSWORD:-}" "${STORAGE_INDEX:-huatuo_storage_consistency}"
}

write_config_bamai_tcp_events() {
	# Ports, filter and storage path belong to this test workspace.
	# The case disables the container manager required by threshold snapshots.
	write_config_blacklist_except irqtracing mthreads_xid net_conntrack tcp_retransmit xfs
	write_config_bamai_http
	write_config_bamai_auth "integration-node-token"
	cat << EOF

[EventTracing.TCPRetransmit]
    Filter = "${TCP_RETRANS_FILTER}"
    EnableTLP = false
    EnableDropwatch = false

EOF
	write_config_storage_local "${HUATUO_BAMAI_TEST_TMPDIR}/events"
}

# The cpusys test controls proc/stat and perf through its isolated fixture root.
write_config_bamai_cpusys_autotracing() {
	local tool=${1:-perf}
	write_config_blacklist_except cpusys net_conntrack
	write_config_bamai_http
	write_config_bamai_auth "integration-node-token"
	cat << EOF

[AutoTracing.CPUSys]
    Tool = "${tool}"
    SysThreshold = 45
    DeltaSysThreshold = 20
    Interval = 1
    RunTracingToolTimeout = 1

EOF
	write_config_storage_local "${HUATUO_BAMAI_TEST_TMPDIR}/events"
}

# The CPU profiler test supplies storage and uses the same trigger configuration.
write_config_bamai_cpusys_profiler() {
	write_config_bamai_cpusys_autotracing profiler
	write_config_bamai_elasticsearch "${STORAGE_ADDR}" "integration" "integration" "cpu-profiler-integration"
}

# The iotracing test controls proc/diskstats and the toolstream subprocess.
write_config_bamai_iotracing_autotracing() {
	write_config_blacklist_except diskio io_health iotracing irqtracing mthreads_gpu mthreads_xid net_conntrack tcp_retransmit xfs
	write_config_bamai_auth "integration-node-token"
	cat << EOF

[AutoTracing.IOTracing]
    RbpsThreshold = 1000
    WbpsThreshold = 1000
    UtilThreshold = 1
    AwaitThreshold = 1000
    RunTracingToolTimeout = 1
    MaxProcDump = 1
    MaxFilesPerProcDump = 1

EOF
	write_config_storage_local "${HUATUO_BAMAI_TEST_TMPDIR}/events"
}

# The irqtracing fixture has no container manager or unrelated BPF objects.
write_config_bamai_irqtracing_autotracing() {
	write_config_blacklist_except irqtracing mthreads_xid net_conntrack xfs
	write_config_bamai_http
	write_config_bamai_auth "integration-node-token"
	cat << EOF

[AutoTracing.IRQTracing]
    Interval = 1
    IntervalTracing = 60
    RunTracingToolTimeout = 1
    MaxEventsPerSecond = 1000
    MinCPUs = 1
    DeltaUsageThreshold = 1
    RelativeIncreaseThreshold = 1
    SustainedIntervals = 10
    UsageThreshold = 100

EOF
	write_config_bamai_elasticsearch "${STORAGE_ADDR}" "integration" "integration" "$(storage_test_index irqtracing-integration)"
}

# The apiserver port and workspace paths are allocated by the caller.
write_config_apiserver_base() {
	write_config_apiserver_http "127.0.0.1:${APISERVER_PORT}"
	cat << EOF

[Jobs]
    StoreDSN = "${HUATUO_BAMAI_TEST_TMPDIR}/jobs.db"

EOF
	write_config_apiserver_agent "${HUATUO_BAMAI_ADDR##*:}" "integration-node-token"
	write_config_apiserver_admin "${API_TOKEN}"
}

# The profile filter case supplies its API settings and isolated storage index.
write_config_apiserver_profile_query() {
	write_config_apiserver_base
	write_config_apiserver_elasticsearch "${STORAGE_ADDR}" "elastic" "profile-filter-test" "${PROFILE_FILTER_INDEX}"
}

# The storage address and credentials are initialized by the calling test.
write_config_bamai_continuous_profiling() {
	local tool_dir=${1:-}
	write_config_blacklist \
		"metax_gpu" "ascend_npu" "softlockup" "ethtool" "netstat_hw" \
		"iolatency" "memory_free" "memory_reclaim" "reschedipi" "softirq" \
		"iotracing" "dropwatch" "memory_threshold_snapshot" "sched-blame"
	write_config_bamai_auth "integration-node-token"
	cat << EOF

[Profiling]
    AggregationIntervalSeconds = ${PROFILE_INTERVAL}
    MaxConcurrentProcesses = 10
EOF
	if [[ $# -gt 0 ]]; then
		printf '    ToolDir = "%s"\n' "${tool_dir}"
	fi
	write_config_bamai_elasticsearch "${STORAGE_ADDR}" "elastic" "${ES_PASSWORD}" "$(storage_test_index huatuo_continuous_profiling_test)"
	write_config_storage_local ""
}

# The API port, users, profiling interval, and storage are owned by the test.
write_config_apiserver_continuous_profiling() {
	write_config_apiserver_http "127.0.0.1:${APISERVER_PORT}"
	write_config_apiserver_elasticsearch "${STORAGE_ADDR}" "elastic" "${ES_PASSWORD}" "$(storage_test_index huatuo_continuous_profiling_test)"
	write_config_apiserver_admin "${API_TOKEN}"
	cat << EOF

[[Auth.Users]]
    ID = "integration-readonly-user"
    BearerToken = "${OTHER_API_TOKEN}"
    Permissions = ["/v1/profiling", "/v1/profiling/**"]

EOF
	write_config_apiserver_agent "${HUATUO_BAMAI_ADDR##*:}" "integration-node-token"
	cat << EOF

[Profiling]
    DashboardBaseURL = "http://grafana.invalid/d"
EOF
}

write_config_bamai_python_memory() {
	# Keep Toolstream, the Node API and kubelet discovery, not unrelated tracers.
	cat << EOF
BlackList = ["arp", "ascend_npu", "cpu_stat", "cpu_util", "cpuidle", "cpusys", "diskio", "dload", "dropwatch", "hungtask", "iolatency", "iotracing", "irqtracing", "loadavg", "memburst", "memory_buddyinfo", "memory_events", "memory_free", "memory_others", "memory_reclaim", "memory_reclaim_events", "memory_threshold_snapshot", "memory_vmstat", "metax_gpu", "mountpoint_perm", "mthreads_gpu", "mthreads_xid", "net_conntrack", "net_rx_latency", "netdev", "netdev_bonding_lacp", "netdev_dcb", "netdev_events", "netdev_hw", "netdev_qdisc", "netdev_rdma_link", "netdev_txqueue_timeout", "netstat", "memory_oom_kill", "ras", "runqlat", "sched-blame", "sched_tick", "sockstat", "softirq", "softlockup", "tcp_memory", "tcp_retransmit", "xfs", "iocost", "blk_throtl", "io_health"]
[HTTPServer.Auth]
    BearerToken = "integration-node-token"
[Pod]
    KubeletReadOnlyPort = 0
    KubeletClientCertPath = "${KUBELET_CERT},${KUBELET_KEY}"
[Profiling]
    ToolDir = "${MEMORY_API_TOOL_DIR:-}"
    AggregationIntervalSeconds = ${PROFILE_INTERVAL}
    MaxConcurrentProcesses = 10
[Storage.Elasticsearch]
    Address = "${STORAGE_ADDR}"
    Username = "elastic"
    Password = "${ES_PASSWORD}"
    Index = "$(storage_test_index huatuo_continuous_profiling_test)"
[Storage.LocalFile]
    Path = ""
EOF
}

write_config_apiserver_python_memory() {
	write_config_apiserver_continuous_profiling
	cat << EOF
[Jobs.Profiling]
    MaxConcurrentPerHost = 3
[Jobs.Controller]
    StatusPollIntervalSeconds = 1
EOF
}

# Profiling workflow cases supply controller limits and isolated service ports.
write_config_apiserver_profiling() {
	write_config_apiserver_base
	cat << CONFIG
[Jobs.Profiling]
    MaxConcurrentPerHost = ${MAX_HOST}
    MaxConcurrent = ${MAX_TOTAL}
[Jobs.Controller]
    StatusPollIntervalSeconds = 1
    PendingTimeoutSeconds = 10
    CompletionGracePeriodSeconds = 20
    NodeUnavailableGracePeriodSeconds = 5
[Profiling]
    DashboardBaseURL = "http://grafana.invalid/d"
CONFIG
	if [[ $WITH_STORAGE == true ]]; then
		write_config_apiserver_elasticsearch "${STORAGE_ADDR}" "elastic" "${ES_PASSWORD}" "$(storage_test_index huatuo_continuous_profiling_test)"
	fi
}

# The matrix supplies kubelet credentials; host-only cases leave POD_NAMESPACE empty.
write_config_bamai_apiserver_profiling() {
	# External runtimes use the same bundled tools as direct profiler tests.
	write_config_bamai_continuous_profiling "${PROFILER_TOOL_DIR}"
	write_config_bamai_http
	cat << CONFIG
[Operations]
    MaxConcurrent = ${NODE_CAPACITY}
CONFIG
	if [[ -n "$POD_NAMESPACE" ]]; then
		write_config_bamai_kubelet 10250 "${KUBELET_CERT}" "${KUBELET_KEY}"
	fi
}

# The caller supplies the API port, monitored device and isolated storage path.
write_config_bamai_netdev_events() {
	write_config_blacklist_except net_conntrack netdev_events tracing_status xfs
	write_config_bamai_http
	write_config_bamai_auth "integration-node-token"
	cat << EOF

[EventTracing.Netdev]
    DeviceList = ["${NETDEV_DEVICE}"]

EOF
	write_config_storage_local "${HUATUO_BAMAI_TEST_TMPDIR}/events"
}

# The memory metric cases share a collector-only agent configuration.
write_config_bamai_memory_metrics() {
	write_config_blacklist_except memory_buddyinfo memory_events memory_others memory_vmstat net_conntrack tracing_status xfs
	write_config_bamai_auth "integration-node-token"
}

# The caller provides kubelet connection details and metric filter parameters.
write_config_container_memory_metrics() {
	write_config_bamai_memory_metrics
	write_config_bamai_kubelet "${kubelet_port}" "${KUBELET_CERT}" "${KUBELET_KEY}"
	cat << CONFIG
[MetricCollector.Vmstat]
 IncludedOnHost = "^$"
 IncludedOnContainer = "${vmstat_include}"
 ExcludedOnContainer = "${vmstat_exclude}"
[MetricCollector.MemoryEvents]
 Included = "${events_include}"
 Excluded = "${events_exclude}"
CONFIG
}

# OOM capture uses host memory data; threshold snapshots need a container manager.
write_config_bamai_memory_oom_kill() {
	write_config_blacklist_except irqtracing memory_oom_kill mthreads_gpu mthreads_xid net_conntrack tracing_status xfs
	write_config_bamai_auth "integration-node-token"
	write_config_storage_local "${HUATUO_BAMAI_TEST_TMPDIR}/events"
}

# The caller supplies the real kubelet endpoint, certificates, and output limits.
write_config_bamai_memory_threshold_snapshot() {
	write_config_blacklist_except memory_threshold_snapshot net_conntrack tracing_status xfs
	write_config_bamai_http
	write_config_bamai_auth "integration-node-token"
	write_config_bamai_kubelet "${GO_SNAPSHOT_KUBELET_PORT}" "${KUBELET_CERT}" "${KUBELET_KEY}"
	cat << EOF

[AutoTracing.MemoryThresholdSnapshot]
    ThresholdPercent = 50
    IntervalTracing = 300
    RunTracingToolTimeout = 10
    MaxMemoryObjectEntries = ${go_snapshot_top_k}

EOF
	write_config_storage_local "${go_snapshot_case_dir}/events"
}

# The Elasticsearch config test supplies the credential fields.
write_config_bamai_elasticsearch_validation() {
	write_config_bamai_default
	write_config_bamai_http
	write_config_bamai_elasticsearch "${storage_address:-}" "${storage_username:-}" "${storage_password:-}"
	write_config_storage_local ""
}

# The baseline keeps XFS enabled so its device fixture can gate initialization.
write_config_bamai_runtime_limits() {
	local startup_cpu=$1 cpu=$2 memory_mib=$3

	write_config_bamai_default
	cat << EOF

[Runtime]
    StartupCPULimitCores = ${startup_cpu}
    CPULimitCores = ${cpu}
    MemoryLimitMiB = ${memory_mib}
EOF
	write_config_bamai_http
}

# The test owns the sender; bamai only receives and persists its toolstream events.
write_config_bamai_toolstream_receiver() {
	# The caller supplies the execution address and workspace.
	write_config_blacklist_except net_conntrack xfs
	write_config_bamai_http
	write_config_bamai_auth "integration-node-token"
	write_config_storage_local "${HUATUO_BAMAI_TEST_TMPDIR}/events"
}

write_config_bamai_cpu_util() {
	local -a enabled=()
	[[ ${cpu_util_disabled} == true ]] || enabled+=(cpu_util)
	write_config_blacklist_except "${enabled[@]}"
	write_config_bamai_http "127.0.0.1:${CPU_UTIL_PORT}"
	write_config_bamai_auth "integration-node-token"
	write_config_bamai_kubelet "${CPU_UTIL_KUBELET_PORT}" "${KUBELET_CERT}" "${KUBELET_KEY}"
}

write_config_bamai_cpu_stat() {
	local -a enabled=()
	[[ ${cpu_stat_disabled} == true ]] || enabled+=(cpu_stat)
	write_config_blacklist_except "${enabled[@]}"
	write_config_bamai_http "127.0.0.1:${CPU_STAT_PORT}"
	write_config_bamai_auth "integration-node-token"
	write_config_bamai_kubelet "${CPU_STAT_KUBELET_PORT}" "${KUBELET_CERT}" "${KUBELET_KEY}"
}

write_config_bamai_runqlat() {
	local -a enabled=(tracing_status)
	[[ ${runqlat_disabled} == true ]] || enabled+=(runqlat)
	write_config_blacklist_except "${enabled[@]}"
	write_config_bamai_http "127.0.0.1:${RUNQLAT_PORT}"
	write_config_bamai_auth "integration-node-token"
	write_config_bamai_kubelet "${RUNQLAT_KUBELET_PORT}" "${KUBELET_CERT}" "${KUBELET_KEY}"
}

write_config_bamai_softirq() {
	local -a enabled=(tracing_status)
	[[ ${softirq_disabled} == true ]] || enabled+=(softirq)
	write_config_blacklist_except "${enabled[@]}"
	write_config_bamai_http "${HUATUO_BAMAI_ADDR#http://}"
	write_config_bamai_auth "integration-node-token"
}

write_config_bamai_cpuidle_autotracing() {
	local -a enabled=(tracing_status)
	[[ ${cpuidle_disabled} == true ]] || enabled+=(cpuidle)
	write_config_blacklist_except "${enabled[@]}"
	write_config_bamai_http "127.0.0.1:${CPUIDLE_PORT}"
	write_config_bamai_auth "integration-node-token"
	write_config_bamai_kubelet "${CPUIDLE_KUBELET_PORT}" "${KUBELET_CERT}" "${KUBELET_KEY}"
	cat << EOF

[AutoTracing.CPUIdle]
    UserThreshold = ${cpuidle_thresholds[0]}
    SysThreshold = ${cpuidle_thresholds[1]}
    UsageThreshold = ${cpuidle_thresholds[2]}
    DeltaUserThreshold = ${cpuidle_thresholds[3]}
    DeltaSysThreshold = ${cpuidle_thresholds[4]}
    DeltaUsageThreshold = ${cpuidle_thresholds[5]}
    Interval = ${cpuidle_interval}
    IntervalTracing = ${cpuidle_cooldown}
    RunTracingToolTimeout = ${cpuidle_duration}
[[AutoTracing.CPUIdle.Filter.Included]]
    Field = "${cpuidle_include_field}"
    Pattern = "${cpuidle_include}"
[[AutoTracing.CPUIdle.Filter.Excluded]]
    Field = "${cpuidle_exclude_field}"
    Pattern = "${cpuidle_exclude}"
EOF
	write_config_storage_local "${HUATUO_BAMAI_TEST_TMPDIR}/events"
	if [[ -n ${STORAGE_ADDR} ]]; then
		write_config_bamai_elasticsearch "${STORAGE_ADDR}" "${STORAGE_USERNAME}" "${STORAGE_PASSWORD}" "${CPUIDLE_INDEX}"
	fi
}

write_config_bamai_dload_autotracing() {
	local -a enabled=(tracing_status)
	[[ ${dload_disabled} == true ]] || enabled+=(dload)
	write_config_blacklist_except "${enabled[@]}"
	write_config_bamai_http "127.0.0.1:${DLOAD_PORT}"
	write_config_bamai_auth "integration-node-token"
	write_config_bamai_kubelet "${DLOAD_KUBELET_PORT}" "${KUBELET_CERT}" "${KUBELET_KEY}"
	cat << EOF

[AutoTracing]
    IssuesList = ${dload_issues}
[AutoTracing.Dload]
    Interval = ${dload_interval}
    IntervalTracing = ${dload_cooldown}
    ThresholdLoad = ${dload_threshold}
    EnableDebug = ${dload_debug}
EOF
	write_config_storage_local "${HUATUO_BAMAI_TEST_TMPDIR}/events"
	if [[ -n ${STORAGE_ADDR} ]]; then
		write_config_bamai_elasticsearch "${STORAGE_ADDR}" "${STORAGE_USERNAME}" "${STORAGE_PASSWORD}" "${DLOAD_INDEX}"
	fi
}
