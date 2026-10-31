#!/usr/bin/env bash

# Copyright 2026 The HuaTuo Authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
require_commands docker
# Use a locally available image; never pull during an acceptance test.
export MEMBURST_CONTAINER_IMAGE=${MEMBURST_CONTAINER_IMAGE:-${BUSINESS_POD_IMAGE}}
docker image inspect "${MEMBURST_CONTAINER_IMAGE}" > /dev/null || fatal "container image is not available locally"
docker info > /dev/null || fatal "Docker daemon is unavailable"
source "${ROOT_DIR}/integration/config.sh"
source "${ROOT_DIR}/integration/lib_memsnapshot.sh"
require_commands go jq awk ss

huatuo_bamai_stop
assert_huatuo_bamai_log_clean || fatal "baseline daemon reported an error"
go_snapshot_case_dir="${HUATUO_BAMAI_TEST_TMPDIR}/daemon-container"
HUATUO_BAMAI_TEST_TMPDIR=${go_snapshot_case_dir}
mkdir -p "${go_snapshot_case_dir}"
GO_SNAPSHOT_BAMAI_PORT=$(allocate_available_port)
GO_SNAPSHOT_KUBELET_PORT=10250
go_snapshot_top_k=10
HUATUO_BAMAI_ADDR="http://127.0.0.1:${GO_SNAPSHOT_BAMAI_PORT}"
HUATUO_BAMAI_METRICS_API="${HUATUO_BAMAI_ADDR}/metrics"
memburst_container_name="huatuo-memburst-daemon-${BASHPID}-${RANDOM}"
memburst_container_client=""

container_cleanup() {
	local status=$?
	trap - EXIT
	huatuo_bamai_stop || status=1
	docker rm -f "${memburst_container_name}" > /dev/null 2>&1 || true
	[[ -z ${memburst_container_client} ]] || wait "${memburst_container_client}" 2> /dev/null || true
	return "${status}"
}
trap 'container_cleanup || exit $?' EXIT

# Scale the real anonymous allocation past the detector's 1% threshold,
# retaining headroom for the daemon and other services in the test VM.
memburst_total_kb=$(awk '/^MemTotal:/ {print $2}' /proc/meminfo)
memburst_available_kb=$(awk '/^MemAvailable:/ {print $2}' /proc/meminfo)
memburst_pressure_mib=$(((memburst_total_kb + 102399) / 102400 + 64))
((memburst_pressure_mib <= 512 && memburst_available_kb > (memburst_pressure_mib + 256) * 1024)) \
	|| fatal "insufficient headroom for real container memory burst (${memburst_pressure_mib} MiB)"
memburst_fixture="${go_snapshot_case_dir}/go-snapshot"
go build -mod=vendor -o "${memburst_fixture}" "${ROOT_DIR}/integration/testdata/test_memory_threshold_snapshot_golang.go"
mkfifo "${go_snapshot_case_dir}/commands"
exec {memburst_container_input}<> "${go_snapshot_case_dir}/commands"
docker run --rm -i --name "${memburst_container_name}" --network=none \
	--env MEMSNAPSHOT_PRESSURE_MIB="${memburst_pressure_mib}" \
	--mount "type=bind,src=${memburst_fixture},dst=/go-snapshot,readonly" \
	--entrypoint /go-snapshot "${MEMBURST_CONTAINER_IMAGE}" 1 \
	< "${go_snapshot_case_dir}/commands" > "${go_snapshot_case_dir}/workload.log" 2>&1 &
memburst_container_client=$!
printf a >&"${memburst_container_input}"
wait_until 30 0.1 grep -qx ready "${go_snapshot_case_dir}/workload.log" || fatal "container workload not ready"
docker inspect "${memburst_container_name}" > "${go_snapshot_case_dir}/container.json"
memburst_container_pid=$(jq -er '.[0].State | select(.Running) | .Pid | select(. > 0)' "${go_snapshot_case_dir}/container.json")

assert_memburst_container_event() {
	jq -e --argjson pid "${memburst_container_pid}" '
	 .tracer_name == "memburst" and .tracer_type == "autotracing"
	 and .region == "e2e" and (.hostname | length > 0) and ((.container_id // "") == "")
	 and .uploaded_timestamp >= .started_timestamp
	 and (.tracer_data as $d |
	   ($d.top_memory_usage | length > 0 and length <= 10)
	   and ([$d.top_memory_usage[].MemSize] == ([$d.top_memory_usage[].MemSize] | sort | reverse))
	   and ($d.process_snapshots | length == ([3, ($d.top_memory_usage | length)] | min))
	   and any(range(0; ($d.process_snapshots | length)); . as $i |
	     $d.process_snapshots[$i] as $s |
	     $s.pid == $pid and $s.pid == $d.top_memory_usage[$i].PID
	     and $s.process_name == $d.top_memory_usage[$i].ProcessName
	     and $s.language == "go" and $s.process_memory.rss_bytes >= 23068672
	     and $s.snapshot.status == "complete" and ($s.snapshot.runtime_version | length > 0)
	     and ($s.snapshot.entries | length > 0 and length <= 10)
	     and any($s.snapshot.entries[]; .name == "main.allocateBlock" and .objects == 6 and .bytes == 16777216)))
	' "${go_snapshot_case_dir}/snapshot.json" > /dev/null || fatal "incorrect persisted container memburst snapshot"
}

write_config_bamai_container_memburst() {
	write_config_bamai_memory_threshold_snapshot \
		| sed 's/"memburst", //; s/^BlackList = \[/BlackList = ["memory_threshold_snapshot", /'
	cat << 'CONFIG'
[AutoTracing.MemoryBurst]
 DeltaMemoryBurst = 1
 DeltaAnonThreshold = 0
 Interval = 1
 SlidingWindowLength = 2
 IntervalTracing = 1
 DumpProcessMaxNum = 10
CONFIG
}
integration_huatuo_bamai_start write_config_bamai_container_memburst --region e2e --disable-kubelet --log-debug
assert_memsnapshot_tracing_metrics memburst e2e 1
memsnapshot_wait_memburst_samples 0 2
memburst_triggered_at=$(date -u '+%Y-%m-%dT%H:%M:%S.%NZ')
printf p >&"${memburst_container_input}"
wait_until 30 0.1 grep -qx 'pressure ready' "${go_snapshot_case_dir}/workload.log" || fatal "container pressure did not finish"

container_burst_event() {
	jq -se --argjson pid "${memburst_container_pid}" --arg at "${memburst_triggered_at}" '
  first(.[] | select(.started_timestamp >= $at)
    | select(any(.tracer_data.process_snapshots[]?; .pid == $pid)))
 ' "${go_snapshot_case_dir}/events/memburst" > "${go_snapshot_case_dir}/snapshot.json" \
		2> "${go_snapshot_case_dir}/snapshot-read.log"
}
wait_until 60 0.2 container_burst_event || fatal "container burst did not reach daemon event storage"
assert_memburst_container_event
assert_memsnapshot_tracing_metrics memburst e2e 1
huatuo_bamai_stop
assert_memsnapshot_logs "${go_snapshot_case_dir}/huatuo.log"
printf q >&"${memburst_container_input}"
wait "${memburst_container_client}" || fatal "container workload failed"
memburst_container_client=""
exec {memburst_container_input}>&-
log_info "PASS: real container burst, daemon ranking, runtime snapshot and persistence"
