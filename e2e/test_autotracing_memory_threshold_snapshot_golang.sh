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
	if [[ -n "${go_snapshot_pid}" ]]; then
		stop_and_wait_by_pid "${go_snapshot_pid}" || true
		go_snapshot_pid=""
	fi
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

go_snapshot_assert_statistics() {
	local mode=$1
	jq -e --arg mode "${mode}" '
		def allocation($caller; $bytes; $objects):
			.kind == "allocation_site"
			and .name == "main.allocateBlock"
			and .bytes == $bytes
			and .objects == $objects
			and .average_bytes == ($bytes / $objects)
			and ([.stack[] | select(startswith("main."))]
				== ["main.allocateBlock", $caller, "main.main"]);
		.tracer_data.snapshot as $snapshot |
		if $mode == "disabled" then
			$snapshot.status == "unavailable"
			and ($snapshot.reason | contains("MemProfileRate=0"))
			and (($snapshot.entries // []) | length == 0)
			and (($snapshot.output_truncated // false) == false)
		else
			$snapshot.status == "complete"
			and ($snapshot.reason // "") == ""
			and ($snapshot.entries[0] | allocation("main.allocatePrimary"; 16777216; 6))
			and ([$snapshot.entries[].bytes] == ([$snapshot.entries[].bytes] | sort | reverse))
			and ([$snapshot.entries[].stack[] | select(. == "main.allocateReleased")] | length == 0)
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
	local mode=$1 triggered_at=$2
	jq -s -e --arg id "${go_snapshot_container_id}" \
		'[.[] | select(.container_id == $id)] | length == 1' \
		"${go_snapshot_case_dir}/events/memory_threshold_snapshot" > /dev/null \
		|| fatal "${mode}: expected exactly one snapshot for the workload container"
	jq -e --arg id "${go_snapshot_container_id}" --argjson pid "${go_snapshot_pid}" \
		--arg pod "${go_snapshot_pod}" --arg ns "${GO_SNAPSHOT_NAMESPACE}" \
		--arg cgroup "${go_snapshot_cgroup_path}" \
		--argjson limit "${GO_SNAPSHOT_LIMIT}" --arg triggered_at "${triggered_at}" '
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
		and $data.victim_process_name == "go-snapshot"
		and $data.victim_oom_score_adj == 1000
		and $data.language == "go"
		and $data.memory_max == $limit
		and $data.memory_current >= ($limit / 2)
		and $data.memory_usage_percent >= 50
		and $data.process_memory.rss_bytes >= 23068672
		and ($data.snapshot.runtime_version | startswith("go1."))
	' "${go_snapshot_case_dir}/snapshot.json" > /dev/null \
		|| fatal "${mode}: incorrect snapshot metadata: $(< "${go_snapshot_case_dir}/snapshot.json")"
	go_snapshot_assert_statistics "${mode}"
}

go_snapshot_run_case() (
	local mode=$1 rate=1 usage_file cgroup_root triggered_at pod_name high_before
	go_snapshot_pid=""
	go_snapshot_cgroup=""
	go_snapshot_top_k=100
	[[ ${mode} != topk ]] || go_snapshot_top_k=1
	[[ ${mode} != disabled ]] || rate=0
	go_snapshot_case_dir="${HUATUO_BAMAI_TEST_TMPDIR}/${mode}"
	HUATUO_BAMAI_TEST_TMPDIR=${go_snapshot_case_dir}
	pod_name="go-snapshot-${BASHPID}-${RANDOM}-${mode}"
	go_snapshot_pod="${pod_name}-1"
	go_snapshot_pod_label="app=${pod_name}"
	trap 'cleanup || exit $?' EXIT
	mkdir -p "${go_snapshot_case_dir}"

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
	mkfifo "${go_snapshot_case_dir}/commands"
	# The case subshell closes this descriptor on exit.
	exec {go_snapshot_input_fd}<> "${go_snapshot_case_dir}/commands"
	(
		echo "${BASHPID}" > "${go_snapshot_cgroup}/cgroup.procs"
		# Match the BestEffort Pod's sleep process so the larger Go RSS wins.
		echo 1000 > /proc/self/oom_score_adj
		exec "${GO_SNAPSHOT_BIN}" "${rate}"
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
	huatuo_bamai_stop

	go_snapshot_assert_event "${mode}" "${triggered_at}"
	huatuo_bamai_log_check || fatal "${mode}: unexpected daemon error log"
	printf q >&"${go_snapshot_input_fd}"
	wait "${go_snapshot_pid}" || fatal "${mode}: Go workload exited unsuccessfully"
	go_snapshot_pid=""
	log_info "${mode}: container memory pressure produced the expected Go snapshot"
	jq '.tracer_data.snapshot | .entries = [.entries[]? | select(.name == "main.allocateBlock")]' \
		"${go_snapshot_case_dir}/snapshot.json"
)

go build -mod=vendor -o "${GO_SNAPSHOT_BIN}" "${ROOT_DIR}/e2e/testdata/memory_threshold_snapshot_golang.go"
# The runner's baseline daemon serves the other e2e cases. This case needs
# isolated configuration and a fresh node-wide cooldown for every scenario.
huatuo_bamai_stop
huatuo_bamai_log_check || fatal "baseline daemon reported an error before the snapshot scenario"
for go_snapshot_mode in aggregation topk disabled; do
	go_snapshot_run_case "${go_snapshot_mode}"
done
