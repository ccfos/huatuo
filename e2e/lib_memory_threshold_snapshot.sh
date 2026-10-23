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

# Shared memory-threshold discovery, payload assertions and explicit setup.
set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"
source "${ROOT_DIR}/integration/lib_memsnapshot.sh"
source "${ROOT_DIR}/integration/lib_cgroup.sh"
source "${ROOT_DIR}/e2e/lib.sh"

memory_threshold_snapshot_setup() {
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
}

# Handle TERM in PID 1 so successful cases do not spend the Pod grace period.
snapshot_create_pod() {
	kubectl run "${go_snapshot_pod}" --namespace "${GO_SNAPSHOT_NAMESPACE}" \
		--image="${BUSINESS_POD_IMAGE}" --image-pull-policy=Never \
		--restart=Never -l "${go_snapshot_pod_label}" --command -- sh -c '
		sleep infinity &
		child=$!
		stop() {
			kill -TERM "$child" 2>/dev/null || true
			wait "$child" 2>/dev/null || true
			exit 0
		}
		trap stop TERM INT
		wait "$child"
	'
}

memory_threshold_snapshot_cleanup() {
	local status=$? diagnosed=0
	if [[ ${status} -ne 0 ]]; then
		memsnapshot_dump_failure_diagnostics "${go_snapshot_case_dir:-${HUATUO_BAMAI_TEST_TMPDIR}}" \
			"${go_snapshot_cgroup:-}" "${go_snapshot_pid:-}" "${GO_SNAPSHOT_NAMESPACE}" \
			"${go_snapshot_pod:-}" "${go_snapshot_stage:-unknown}" || true
		diagnosed=1
	fi
	huatuo_bamai_stop || status=1
	if [[ ${status} -ne 0 && ${diagnosed} == 0 ]]; then
		memsnapshot_dump_failure_diagnostics "${go_snapshot_case_dir:-${HUATUO_BAMAI_TEST_TMPDIR}}" \
			"${go_snapshot_cgroup:-}" "${go_snapshot_pid:-}" "${GO_SNAPSHOT_NAMESPACE}" \
			"${go_snapshot_pod:-}" "daemon shutdown" || true
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

# Lost lifecycle samples request a full container refresh. A later validated
# event, revalidated watch or container metadata provides a checkpoint.
# Later warnings still fail.
snapshot_log_timestamp() {
	local timestamp=$1
	local pattern='^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}'
	pattern+='(\.[0-9]{1,9})?(Z|[+-][0-9]{2}:[0-9]{2})$'
	if [[ ! ${timestamp} =~ ${pattern} ]]; then
		log_error "invalid log timestamp: ${timestamp}"
		return 1
	fi
	date -u -d "${timestamp}" '+%Y-%m-%dT%H:%M:%S.%NZ'
}

assert_snapshot_daemon_logs() {
	local file=$1 snapshot=$2 recovered_at=${3:-}
	local checkpoint=${4:-revalidated target watch} observed_at line
	local LC_ALL=C
	if [[ -z ${recovered_at} ]]; then
		recovered_at=$(jq -er '.started_timestamp' "${snapshot}") || return 1
		checkpoint="validated snapshot"
	fi
	recovered_at=$(snapshot_log_timestamp "${recovered_at}") || return 1
	awk '{
		gsub(/\033\[[0-9;]*m/, "")
		if (tolower($0) ~ /level="?(warn|warning|error|panic|fatal)"?|"level":"(warn|warning|error|panic|fatal)"|panic:/) print
	}' "${file}" | while IFS= read -r line; do
		if [[ ${line} != *'level="warning"'* ||
			${line} != *'msg="lost BPF perf event samples"'* ||
			${line} != *'func="internal/pod.cgroupCssEventSyncHandler.func1"'* ]] \
			|| ! grep -qE 'error="bpf: [1-9][0-9]* perf event samples lost"' <<< "${line}" \
			|| ! grep -qE 'file="cgroup_lifecycle.go:[0-9]+"' <<< "${line}"; then
			log_error "unexpected daemon warning/error: ${line}"
			return 1
		fi
		observed_at=${line#*time=\"}
		observed_at=${observed_at%%\"*}
		observed_at=$(snapshot_log_timestamp "${observed_at}") || return 1
		if [[ ! ${observed_at} < ${recovered_at} ]]; then
			log_error "unexpected daemon warning/error: ${line}"
			return 1
		fi
		log_info "${checkpoint} after recoverable cgroup sample loss: ${line}"
	done
}

assert_snapshot_logs() {
	huatuo_bamai_log_check || fatal "${go_snapshot_stage}: unexpected daemon error log"
	assert_snapshot_daemon_logs "${go_snapshot_case_dir}/huatuo.log" "${go_snapshot_case_dir}/snapshot.json" \
		|| fatal "${go_snapshot_stage}: unexpected daemon warning/error"
}

assert_snapshot_event() {
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
		assert_go_snapshot_statistics "${mode}"
	else
		assert_snapshot_runtime_payload "${language}"
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
