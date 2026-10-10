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

# Verify real Pod discovery, memory pressure, Go heap capture, and persistence.
# A host Go workload joins the memory cgroup of a real Kubernetes container.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"
source "${ROOT_DIR}/integration/lib_memsnapshot.sh"
source "${ROOT_DIR}/integration/lib_cgroup.sh"
source "${ROOT_DIR}/e2e/lib.sh"

readonly GO_SNAPSHOT_BIN="${HUATUO_BAMAI_TEST_TMPDIR}/go-snapshot"
readonly GO_SNAPSHOT_LIMIT=$((128 * 1024 * 1024))
readonly GO_SNAPSHOT_THRESHOLD=$((GO_SNAPSHOT_LIMIT / 2))
readonly GO_SNAPSHOT_NAMESPACE="${BUSINESS_POD_NS}"

require_commands go jq kubectl findmnt ss
[[ ${EUID} -eq 0 ]] || skip "requires root for BPF, process inspection, and memory.high"
require_readable "${KUBELET_CERT}" "${KUBELET_KEY}"

GO_SNAPSHOT_KUBELET_PORT=${KUBELET_PODS_API##*:}
GO_SNAPSHOT_KUBELET_PORT=${GO_SNAPSHOT_KUBELET_PORT%%/*}
GO_SNAPSHOT_BAMAI_PORT=$(allocate_available_port) || fatal "cannot allocate bamai port"
readonly GO_SNAPSHOT_KUBELET_PORT GO_SNAPSHOT_BAMAI_PORT
HUATUO_BAMAI_ADDR="http://127.0.0.1:${GO_SNAPSHOT_BAMAI_PORT}"
HUATUO_BAMAI_METRICS_API="${HUATUO_BAMAI_ADDR}/metrics"

cleanup() {
	local status=$? file
	huatuo_bamai_stop || status=1
	if [[ ${status} -ne 0 ]]; then
		kubectl --request-timeout=10s describe pod -n "${GO_SNAPSHOT_NAMESPACE}" "${go_snapshot_pod}" >&2 || true
		for file in memory.current memory.usage_in_bytes memory.events memory.events.local cgroup.procs; do
			[[ ! -r "${go_snapshot_cgroup}/${file}" ]] || cat "${go_snapshot_cgroup}/${file}" >&2 || true
		done
	fi
	stop_and_wait_by_pid "${go_snapshot_pid}" || true
	go_snapshot_pid=""
	k8s_delete_pod "${GO_SNAPSHOT_NAMESPACE}" "${go_snapshot_pod_label}" || status=1
	return "${status}"
}

# API discovery precedes asynchronous watch registration. Wait for the actual
# limit watch before allocating pressure, so registration cannot trigger the test.
go_snapshot_watch_is_ready() {
	local inode
	inode=$(stat -c '%i' "${go_snapshot_limit_file}") || return 1
	printf -v inode '%x' "${inode}"
	grep -qE "^inotify wd:.*ino:${inode} " \
		/proc/"$(< "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")"/fdinfo/* 2> /dev/null
}

go_snapshot_read_event() {
	jq -se --arg id "${go_snapshot_container_id}" 'first(.[] | select(.container_id == $id))' \
		"${go_snapshot_case_dir}/events/memory_threshold_snapshot" \
		> "${go_snapshot_case_dir}/snapshot.json" 2> "${go_snapshot_case_dir}/snapshot-read.log"
}

snapshot_assert_runtime_payload() {
	local language=$1 object_bytes=0 python_version=""
	if [[ ${language} == python ]]; then
		object_bytes=$(sed -n "s/^object_bytes=//p" "${go_snapshot_case_dir}/workload.log")
		[[ ${object_bytes} =~ ^[1-9][0-9]*$ ]] || fatal "Python fixture did not report object size"
	fi
	# CPython 3.6 predates _PyRuntime; newer interpreters must expose it.
	if [[ ${language} == python ]]; then
		python_version=$(python3 -c 'import sys; print("%d.%d" % sys.version_info[:2])')
	fi
	# Accept only independently established capability gaps, never arbitrary errors.
	if [[ ${language} == python ]] && jq -e '
  .tracer_data.snapshot.status == "unavailable"
  and .tracer_data.snapshot.status_reason == "CPython runtime is unsupported: Py_Version and a versioned libpython name are unavailable"
 ' "${go_snapshot_case_dir}/snapshot.json" > /dev/null || {
		[[ ${language} == python && ${python_version} == 3.6 ]] && jq -e '
  .tracer_data.snapshot.status == "unavailable"
  and .tracer_data.snapshot.status_reason == "CPython runtime is unsupported: no mapped module exports _PyRuntime"
 ' "${go_snapshot_case_dir}/snapshot.json" > /dev/null
	}; then
		jq -e '
  .tracer_data.language == "python"
  and .tracer_data.process_memory.rss_bytes >= 8388608
  and (.tracer_data.snapshot.duration_ms | type == "number" and . >= 0)
  and ((.tracer_data.snapshot.entries // []) | length == 0)
 ' "${go_snapshot_case_dir}/snapshot.json" > /dev/null || fatal "invalid unsupported CPython payload"
		log_info "PASS: CPython unsupported-runtime result; object statistics unavailable on this runtime"
		return
	fi
	jq -e --arg language "${language}" --argjson object_bytes "${object_bytes}" '
  .tracer_data as $data | $data.snapshot as $s |
  $data.language == $language
  and $data.process_memory.rss_bytes >= 8388608
  and ($s.duration_ms | type == "number")
  and (if $language == "native" or $language == "cpp" then
   $s.status == "unavailable" and ($s.status_reason | length > 0)
   and (($s.entries // []) | length == 0)
  else
   ($s.status == "complete" or $s.status == "partial")
   and ($s.runtime_version | length > 0)
   and ($s.entries | length > 0 and length <= 100)
   and ([$s.entries[].bytes] == ([$s.entries[].bytes] | sort | reverse))
   and all($s.entries[]; (.name | length > 0) and (.kind | length > 0)
    and .objects > 0 and .average_bytes == (.bytes / .objects))
   and (if $language == "python" then
    any($s.entries[]; (.name | contains("MemsnapshotPayload")) and .objects == 20000 and .bytes == (20000 * $object_bytes) and .average_bytes == $object_bytes)
   else
    $s.status == "partial" and ($s.status_reason | contains("concurrently"))
    and any($s.entries[]; .name == "HeapFixture$Payload"
     and .objects > 0 and .objects <= 200000 and .bytes == (.objects * 80) and .average_bytes == 80)
    and any($s.entries[]; .name == "HeapFixture$Payload[]"
     and .objects == 6 and .bytes == 7200096 and .average_bytes == 1200016)
   end)
  end)
 ' "${go_snapshot_case_dir}/snapshot.json" > /dev/null || fatal "${language}: invalid persisted runtime payload"
}

# Lost lifecycle samples request a full container refresh. A later validated
# event proves recovery; warnings after that event and all other warnings fail.
snapshot_assert_daemon_logs() {
	python3 - "$1" "$2" << 'PYTHON'
import calendar
import json
import re
import sys
from decimal import Decimal
import time

def timestamp(value):
    match = re.fullmatch(r"(.{19})(\.\d+)?(Z|[+-]\d\d:\d\d)", value)
    if not match:
        raise ValueError("invalid log timestamp: " + value)
    seconds = Decimal(calendar.timegm(time.strptime(match[1], "%Y-%m-%dT%H:%M:%S")))
    if match[3] != "Z":
        zone = match[3]
        offset = int(zone[1:3]) * 3600 + int(zone[4:6]) * 60
        seconds -= offset if zone[0] == "+" else -offset
    return seconds + Decimal(match[2] or "0")

with open(sys.argv[2]) as source:
    recovered_at = timestamp(json.load(source)["started_timestamp"])
with open(sys.argv[1]) as source:
    for raw in source:
        line = re.sub(r"\x1b\[[0-9;]*m", "", raw)
        if not re.search(r'level="?(warn|warning|error|panic|fatal)"?|"level":"(warn|warning|error|panic|fatal)"|panic:', line, re.I):
            continue
        observed = re.search(r'time="([^"]+)"', line)
        known = ('level="warning"' in line and
                 'msg="lost BPF perf event samples"' in line and
                 re.search(r'error="bpf: [1-9][0-9]* perf event samples lost"', line) and
                 re.search(r'file="cgroup_lifecycle.go:[0-9]+"', line) and
                 'func="internal/pod.cgroupCssEventSyncHandler.func1"' in line)
        if not known or not observed or timestamp(observed[1]) >= recovered_at:
            sys.exit("unexpected daemon warning/error: " + line)
        print("validated snapshot after recoverable cgroup sample loss: " + line.strip())
PYTHON
}

go_snapshot_assert_statistics() {
	local mode=$1
	jq -e --arg mode "${mode}" '
		def allocation($caller; $bytes; $objects):
			.kind == "inuse_space_objects"
			and .name == "main.allocateBlock"
			and .bytes == $bytes
			and .objects == $objects
			and .average_bytes == ($bytes / $objects)
			and ([.stack[] | select(startswith("main."))] as $frames |
				[$frames[] | split(", ")[0]] == ["main.allocateBlock", $caller, "main.main"]
				and all($frames[]; test(", .*/memory_threshold_snapshot_golang\\.go:[1-9][0-9]*$")));
		.tracer_data.snapshot as $snapshot |
		if $mode == "disabled" then
			$snapshot.status == "unavailable"
			and ($snapshot.status_reason | contains("MemProfileRate=0"))
			and (($snapshot.entries // []) | length == 0)
			and (($snapshot.output_truncated // false) == false)
		else
			$snapshot.status == "complete"
			and ($snapshot.status_reason // "") == ""
			and ($snapshot.entries[0] | allocation("main.allocatePrimary"; 16777216; 6))
			and ([$snapshot.entries[].bytes] == ([$snapshot.entries[].bytes] | sort | reverse))
			and ([$snapshot.entries[].stack[] | select(startswith("main.allocateReleased, "))] | length == 0)
			and (if $mode == "topk" then
				($snapshot.entries | length == 1) and $snapshot.output_truncated == true
			else
				($snapshot.entries[1] | allocation("main.allocateSecondary"; 6291456; 3))
				and ([$snapshot.entries[] | select(.name == "main.allocateBlock")] | length == 2)
			end)
		end
	' "${go_snapshot_case_dir}/snapshot.json" > /dev/null \
		|| fatal "${mode}: incorrect Go stack statistics: $(< "${go_snapshot_case_dir}/snapshot.json")"
}

go_snapshot_assert_event() {
	local mode=$1 triggered_at=$2 language=go
	case ${mode} in python | java) language=${mode} ;; c) language=native ;; cpp) language=cpp ;; esac
	jq -s -e --arg id "${go_snapshot_container_id}" \
		'[.[] | select(.container_id == $id)] | length == 1' \
		"${go_snapshot_case_dir}/events/memory_threshold_snapshot" > /dev/null \
		|| fatal "${mode}: expected exactly one snapshot for the workload container"
	jq -e --arg id "${go_snapshot_container_id}" --argjson pid "${go_snapshot_pid}" \
		--arg pod "${go_snapshot_pod}" --arg ns "${GO_SNAPSHOT_NAMESPACE}" \
		--arg cgroup "${go_snapshot_cgroup_path}" \
		--argjson limit "${GO_SNAPSHOT_LIMIT}" --arg triggered_at "${triggered_at}" --arg language "${language}" '
		.tracer_data as $data |
		.tracer_name == "memory_threshold_snapshot"
		and .tracer_type == "autotracing"
		and .container_id == $id
		and .container_hostname == $pod
		and .container_host_namespace == $ns
		and (.started_timestamp | type == "string")
		and (.observed_timestamp | type == "string")
		and .started_timestamp >= $triggered_at
		and .observed_timestamp >= .started_timestamp
		and .uploaded_timestamp >= .observed_timestamp
		and $data.cgroup_path == $cgroup
		and $data.victim_pid == $pid
		and ($data.victim_process_name | length > 0)
		and $data.victim_oom_score_adj == 1000
		and $data.language == $language
		and $data.memory_max == $limit
		and $data.memory_current >= ($limit / 2)
		and $data.memory_usage_percent >= 50
		and $data.process_memory.rss_bytes >= 8388608
        and (if $language == "go" then
            $data.victim_process_name == "go-snapshot"
            and $data.process_memory.rss_bytes >= 23068672
            and ($data.snapshot.runtime_version | startswith("go1."))
        else true end)
	' "${go_snapshot_case_dir}/snapshot.json" > /dev/null \
		|| fatal "${mode}: incorrect snapshot metadata: $(< "${go_snapshot_case_dir}/snapshot.json")"
	if [[ ${language} == go ]]; then
		go_snapshot_assert_statistics "${mode}"
	else
		snapshot_assert_runtime_payload "${language}"
	fi
}

snapshot_require_pod_image() {
	# Kubelet may have collected an unused preloaded image during earlier tests.
	# Restore from Docker's local copy without relying on a registry connection.
	require_commands crictl
	if ! crictl inspecti "${BUSINESS_POD_IMAGE}" > /dev/null 2>&1; then
		require_commands docker ctr
		docker image inspect "${BUSINESS_POD_IMAGE}" > /dev/null \
			|| fatal "snapshot Pod image is unavailable locally: ${BUSINESS_POD_IMAGE}"
		docker image save "${BUSINESS_POD_IMAGE}" | ctr -n k8s.io images import - \
			|| fatal "cannot restore snapshot Pod image to containerd"
		crictl inspecti "${BUSINESS_POD_IMAGE}" > /dev/null \
			|| fatal "restored snapshot Pod image is unavailable through CRI"
	fi
}

go_snapshot_run_case() (
	local mode=$1 rate=1 usage_file cgroup_root triggered_at pod_name high_before
	local fixture_root=${HUATUO_BAMAI_TEST_TMPDIR}
	local -a workload=("${GO_SNAPSHOT_BIN}" 1)
	local -a java_flags=()
	if [[ ${mode} == java ]] && java -XX:-UseCompactObjectHeaders -version > /dev/null 2>&1; then
		java_flags=(-XX:-UseCompactObjectHeaders)
	fi
	case ${mode} in
	python) workload=(python3 "${fixture_root}/snapshot.py") ;;
	java) workload=(java "${java_flags[@]}" -XX:+UseG1GC -XX:G1HeapRegionSize=1m -XX:+UseCompressedOops -XX:+UseCompressedClassPointers -XX:-UseTLAB -Xms32m -Xmx112m -cp "${fixture_root}" HeapFixture) ;;
	c | cpp) workload=("${fixture_root}/${mode}-snapshot") ;;
	esac
	go_snapshot_pid=""
	go_snapshot_cgroup=""
	go_snapshot_top_k=100
	[[ ${mode} != topk ]] || go_snapshot_top_k=1
	[[ ${mode} != disabled ]] || {
		rate=0
		workload=("${GO_SNAPSHOT_BIN}" "${rate}")
	}
	go_snapshot_case_dir="${HUATUO_BAMAI_TEST_TMPDIR}/${mode}"
	HUATUO_BAMAI_TEST_TMPDIR=${go_snapshot_case_dir}
	pod_name="go-snapshot-${BASHPID}-${RANDOM}-${mode}"
	go_snapshot_pod="${pod_name}-1"
	go_snapshot_pod_label="app=${pod_name}"
	trap 'cleanup || exit $?' EXIT
	mkdir -p "${go_snapshot_case_dir}"

	snapshot_require_pod_image
	k8s_create_pod "${GO_SNAPSHOT_NAMESPACE}" "${pod_name}" "${BUSINESS_POD_IMAGE}" "${go_snapshot_pod_label}" 1
	assert_kubelet_pod_count "${GO_SNAPSHOT_NAMESPACE}" "^${go_snapshot_pod}$" 1
	go_snapshot_container_id=$(kubelet_container_ids "${GO_SNAPSHOT_NAMESPACE}" "^${go_snapshot_pod}$")
	cgroup_root=$(cgroup_memory_root) || fatal "${mode}: memory cgroup mount not found"
	go_snapshot_cgroup=$(cgroup_find_container "${cgroup_root}" "${go_snapshot_container_id}") \
		|| fatal "${mode}: memory cgroup not found for container ${go_snapshot_container_id}"
	go_snapshot_cgroup_path=${go_snapshot_cgroup#"${cgroup_root}"}
	cgroup_configure_memory "${go_snapshot_cgroup}" "${GO_SNAPSHOT_LIMIT}" \
		|| fatal "${mode}: cannot set container memory limit with swap disabled"
	go_snapshot_limit_file="${go_snapshot_cgroup}/memory.limit_in_bytes"
	usage_file="${go_snapshot_cgroup}/memory.usage_in_bytes"
	if [[ -e "${go_snapshot_cgroup}/memory.max" ]]; then
		go_snapshot_limit_file="${go_snapshot_cgroup}/memory.max"
		usage_file="${go_snapshot_cgroup}/memory.current"
		# V2 requires a high/max notification. Only this test Pod gets a high limit.
		echo $((72 * 1024 * 1024)) > "${go_snapshot_cgroup}/memory.high"
	fi

	# Set a finite limit before discovery; unavailable watches are not retried.
	# A fresh daemon also isolates the node-wide cooldown between cases.
	integration_huatuo_bamai_start write_memory_threshold_snapshot_config --region e2e --log-debug
	memsnapshot_assert_tracing_metrics memory_threshold_snapshot e2e 1
	mkfifo "${go_snapshot_case_dir}/commands"
	# The case subshell closes this descriptor on exit.
	exec {go_snapshot_input_fd}<> "${go_snapshot_case_dir}/commands"
	(
		echo "${BASHPID}" > "${go_snapshot_cgroup}/cgroup.procs"
		# Match the BestEffort Pod's sleep process so the larger Go RSS wins.
		echo 1000 > /proc/self/oom_score_adj
		exec "${workload[@]}"
	) < "${go_snapshot_case_dir}/commands" > "${go_snapshot_case_dir}/workload.log" 2>&1 &
	go_snapshot_pid=$!

	printf a >&"${go_snapshot_input_fd}"
	wait_until 30 0.2 grep -qx ready "${go_snapshot_case_dir}/workload.log" || fatal "${mode}: Go allocations and GC did not finish"
	[[ $(< "${usage_file}") -lt ${GO_SNAPSHOT_THRESHOLD} ]] \
		|| fatal "${mode}: workload already exceeds threshold before pressure: $(< "${usage_file}")"
	wait_until 30 0.1 go_snapshot_watch_is_ready || fatal "${mode}: memory cgroup watch was not registered"
	if [[ -e "${go_snapshot_cgroup}/memory.max" ]]; then
		high_before=$(awk '$1 == "high" { print $2 }' "${go_snapshot_cgroup}/memory.events")
	fi

	triggered_at=$(date -u '+%Y-%m-%dT%H:%M:%S.%NZ')
	printf p >&"${go_snapshot_input_fd}"
	if [[ -e "${go_snapshot_cgroup}/memory.max" ]]; then
		wait_until 15 0.1 awk -v before="${high_before}" '$1 == "high" { high = $2 } END { exit !(high > before) }' \
			"${go_snapshot_cgroup}/memory.events" || fatal "${mode}: no kernel memory.high event"
		# Release reclaim throttling after notification so the workload can finish.
		echo max > "${go_snapshot_cgroup}/memory.high"
	fi
	wait_until 30 0.1 go_snapshot_read_event || fatal "${mode}: daemon did not persist a Go snapshot"
	wait_until 30 0.2 grep -qx 'pressure ready' "${go_snapshot_case_dir}/workload.log" || fatal "${mode}: pressure allocation did not finish"
	memsnapshot_assert_tracing_metrics memory_threshold_snapshot e2e 1
	huatuo_bamai_stop

	go_snapshot_assert_event "${mode}" "${triggered_at}"
	huatuo_bamai_log_check || fatal "${mode}: unexpected daemon error log"
	require_readable "${go_snapshot_case_dir}/huatuo.log"
	snapshot_assert_daemon_logs "${go_snapshot_case_dir}/huatuo.log" "${go_snapshot_case_dir}/snapshot.json" \
		|| fatal "${mode}: unexpected daemon warning/error"
	printf q >&"${go_snapshot_input_fd}"
	wait "${go_snapshot_pid}" || fatal "${mode}: Go workload exited unsuccessfully"
	go_snapshot_pid=""
	log_info "${mode}: container memory pressure produced the expected runtime snapshot"
	jq '.tracer_data.snapshot | .entries = [.entries[]? | select(.name == "main.allocateBlock")]' \
		"${go_snapshot_case_dir}/snapshot.json"
)

# The lifecycle suite reuses the same metadata and exact allocation assertions.
if [[ ${BASH_SOURCE[0]} == "${0}" ]]; then
	go build -mod=vendor -o "${GO_SNAPSHOT_BIN}" "${ROOT_DIR}/e2e/testdata/memory_threshold_snapshot_golang.go"
	# The runner's baseline daemon serves the other e2e cases. This case needs
	# isolated configuration and a fresh node-wide cooldown for every scenario.
	huatuo_bamai_stop
	huatuo_bamai_log_check || fatal "baseline daemon reported an error before the snapshot scenario"
	snapshot_build_runtime_fixtures
	for go_snapshot_mode in aggregation topk disabled python java c cpp; do
		go_snapshot_run_case "${go_snapshot_mode}"
	done
fi
