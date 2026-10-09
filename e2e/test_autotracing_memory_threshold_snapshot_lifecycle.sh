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
source "${ROOT_DIR}/e2e/test_autotracing_memory_threshold_snapshot_golang.sh"

lifecycle_logs() {
	local file=$1 phase=$2
	go_snapshot_stage="${lifecycle_mode}: validate ${phase} daemon logs"
	if [[ ${phase} != negative && ${phase} != interrupted ]]; then
		# Only this daemon run's validated snapshot can prove sample-loss recovery.
		snapshot_assert_daemon_logs "${file}" "${go_snapshot_case_dir}/snapshot.json" \
			|| fatal "unexpected warning/error in ${file}"
	else
		snapshot_assert_daemon_logs "${file}" "" "${lifecycle_watch_verified_at}" "${lifecycle_recovery_checkpoint}" \
			|| fatal "unexpected warning/error in ${file}"
	fi
}

lifecycle_write_config() {
	write_memory_threshold_snapshot_config
	sed -i "s/ThresholdPercent = 50/ThresholdPercent = ${lifecycle_threshold}/; s/IntervalTracing = 300/IntervalTracing = 10/" "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf"
	if [[ ${lifecycle_mode} == blacklist ]]; then
		sed -i 's/^BlackList = \[/BlackList = ["memory_threshold_snapshot", /' "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf"
	fi
}

lifecycle_assert_metrics() {
	local enabled=1
	[[ ${lifecycle_mode} != blacklist ]] || enabled=0
	memsnapshot_assert_tracing_metrics memory_threshold_snapshot e2e "${enabled}"
}

lifecycle_start() {
	integration_huatuo_bamai_start lifecycle_write_config --region e2e --log-debug \
		--bpf-dir "${ROOT_DIR}/_output/bpf"
	if [[ ${lifecycle_mode} != blacklist ]]; then
		wait_until 30 0.1 go_snapshot_watch_is_ready || fatal "limit watch not registered"
	else
		assert_huatuo_bamai_containers_present "${GO_SNAPSHOT_NAMESPACE}" "^${go_snapshot_pod}$" 1
	fi
	lifecycle_assert_metrics
}

lifecycle_stop() {
	local signal=$1 phase=$2 pid status=0
	if [[ ${phase} == negative || ${signal} == KILL ]]; then
		# Recheck after the suppression window, in the same daemon run.
		if [[ ${lifecycle_mode} != blacklist ]]; then
			go_snapshot_watch_is_ready || fatal "target watch disappeared during negative assertion"
			lifecycle_recovery_checkpoint="revalidated target watch"
		else
			huatuo_bamai_containers_present "${GO_SNAPSHOT_NAMESPACE}" "^${go_snapshot_pod}$" 1 \
				|| fatal "target container metadata disappeared during blacklist assertion"
			lifecycle_recovery_checkpoint="revalidated target container metadata"
		fi
		lifecycle_assert_metrics
		lifecycle_watch_verified_at=$(date -u '+%Y-%m-%dT%H:%M:%S.%NZ')
	fi
	pid=$(< "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")
	kill -"${signal}" "${pid}"
	# Reading procfs can race with Bash reaping the child. A failed read means
	# it has exited; never turn that successful exit into a timeout.
	local state i
	for ((i = 0; i < 650; i++)); do
		state=$(awk '{print $3}' "/proc/${pid}/stat" 2> /dev/null) || break
		[[ ${state} == Z ]] && break
		sleep 0.1
	done
	if ((i == 650)); then
		kill -KILL "${pid}" || true
		fatal "daemon did not stop after ${signal} within 65 seconds"
	fi
	wait "${pid}" || status=$?
	if [[ ${signal} == KILL ]]; then
		[[ ${status} == 137 ]] || fatal "SIGKILL daemon exit status ${status}"
	else
		[[ ${status} == 0 ]] || fatal "graceful daemon exit status ${status}"
	fi
	rm -f "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid"
	mv "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${go_snapshot_case_dir}/daemon-${phase}.log"
	lifecycle_logs "${go_snapshot_case_dir}/daemon-${phase}.log" "${phase}"
}

lifecycle_count() {
	local file="${go_snapshot_case_dir}/events/memory_threshold_snapshot"
	if [[ ! -e ${file} ]]; then
		printf '0\n'
		return
	fi
	jq -s --arg id "${go_snapshot_container_id}" '[.[] | select(.container_id == $id)] | length' "${file}"
}

lifecycle_no_event() {
	local expected=$1 seconds=$2 end
	end=$((SECONDS + seconds))
	while ((SECONDS < end)); do
		huatuo_bamai_ready || fatal "daemon unavailable during negative assertion"
		kill -0 "${go_snapshot_pid}" || fatal "workload exited during negative assertion"
		[[ $(lifecycle_count) == "${expected}" ]] || fatal "unexpected event during suppression window"
		sleep 0.2
	done
}

lifecycle_workload() {
	local phase=$1
	mkfifo "${go_snapshot_case_dir}/commands-${phase}"
	exec {go_snapshot_input_fd}<> "${go_snapshot_case_dir}/commands-${phase}"
	(
		echo "${BASHPID}" > "${go_snapshot_cgroup}/cgroup.procs"
		echo 1000 > /proc/self/oom_score_adj
		exec "${GO_SNAPSHOT_BIN}" 1
	) < "${go_snapshot_case_dir}/commands-${phase}" > "${go_snapshot_case_dir}/workload-${phase}.log" 2>&1 &
	go_snapshot_pid=$!
	printf a >&"${go_snapshot_input_fd}"
	wait_until 30 0.1 grep -qx ready "${go_snapshot_case_dir}/workload-${phase}.log" || fatal "workload not ready"
	[[ $(< "${lifecycle_usage}") -lt ${GO_SNAPSHOT_THRESHOLD} ]] || fatal "initial usage exceeds 50 percent"
}

lifecycle_trigger() {
	go_snapshot_stage="${lifecycle_mode}: trigger pressure (phase $1)"
	local phase=$1 before
	before=$(lifecycle_count)
	if [[ -e ${go_snapshot_cgroup}/memory.max ]]; then
		lifecycle_high_before=$(awk '$1=="high" {print $2}' "${go_snapshot_cgroup}/memory.events")
		echo $((72 * 1024 * 1024)) > "${go_snapshot_cgroup}/memory.high"
	fi
	lifecycle_triggered_at=$(date -u '+%Y-%m-%dT%H:%M:%S.%NZ')
	printf p >&"${go_snapshot_input_fd}"
	if [[ -e ${go_snapshot_cgroup}/memory.max ]]; then
		wait_until 15 0.1 awk -v n="${lifecycle_high_before}" '$1=="high" {v=$2} END {exit !(v>n)}' "${go_snapshot_cgroup}/memory.events" || fatal "no real memory.high notification"
		echo max > "${go_snapshot_cgroup}/memory.high"
	fi
	wait_until 30 0.1 grep -qx 'pressure ready' "${go_snapshot_case_dir}/workload-${phase}.log" || fatal "pressure incomplete"
	[[ $(< "${lifecycle_usage}") -ge ${GO_SNAPSHOT_THRESHOLD} ]] || fatal "pressure did not cross 50 percent"
	lifecycle_assert_metrics
}

lifecycle_finish_workload() {
	printf q >&"${go_snapshot_input_fd}"
	wait "${go_snapshot_pid}" || fatal "workload failed"
	go_snapshot_pid=""
	exec {go_snapshot_input_fd}>&-
	wait_until 20 0.1 lifecycle_usage_low || fatal "container usage did not drop after workload exit"
}

lifecycle_usage_low() { [[ $(< "${lifecycle_usage}") -lt ${GO_SNAPSHOT_THRESHOLD} ]]; }
lifecycle_expected_count() { [[ $(lifecycle_count) == "$1" ]]; }

lifecycle_assert_latest() {
	go_snapshot_stage="${lifecycle_mode}: wait for snapshot count $1"
	local expected=$1
	wait_until 30 0.1 lifecycle_expected_count "${expected}" || fatal "snapshot count did not reach ${expected}"
	jq -s --arg id "${go_snapshot_container_id}" '[.[] | select(.container_id == $id)] | last' "${go_snapshot_case_dir}/events/memory_threshold_snapshot" > "${go_snapshot_case_dir}/snapshot.json"
	jq -e --arg id "${go_snapshot_container_id}" --argjson pid "${go_snapshot_pid}" --arg pod "${go_snapshot_pod}" --arg ns "${GO_SNAPSHOT_NAMESPACE}" --arg at "${lifecycle_triggered_at}" --arg cgroup "${go_snapshot_cgroup_path}" '
  .tracer_name == "memory_threshold_snapshot" and .tracer_type == "autotracing"
  and .container_id == $id and .container_hostname == $pod and .container_host_namespace == $ns
  and .tracer_data.victim_pid == $pid and .tracer_data.language == "go"
  and .tracer_data.cgroup_path == $cgroup
  and .tracer_data.victim_process_name == "go-snapshot" and .tracer_data.victim_oom_score_adj == 1000
  and .tracer_data.memory_usage_percent >= 50
  and .tracer_data.memory_max == 134217728 and .tracer_data.memory_current >= 67108864
  and .tracer_data.process_memory.rss_bytes >= 23068672
  and .started_timestamp >= $at and .observed_timestamp >= .started_timestamp
  and .uploaded_timestamp >= .observed_timestamp
 ' "${go_snapshot_case_dir}/snapshot.json" > /dev/null || fatal "incorrect lifecycle event metadata"
	lifecycle_assert_metrics
	go_snapshot_assert_statistics aggregation
	cp "${go_snapshot_case_dir}/snapshot.json" "${go_snapshot_case_dir}/snapshot-${expected}.json"
}

lifecycle_case() (
	local lifecycle_mode=$1 lifecycle_threshold=50 cgroup_root pod_name i lifecycle_usage
	go_snapshot_stage="${lifecycle_mode}: create Pod and discover container"
	go_snapshot_case_dir="${HUATUO_BAMAI_TEST_TMPDIR}/${lifecycle_mode}"
	HUATUO_BAMAI_TEST_TMPDIR=${go_snapshot_case_dir}
	go_snapshot_pid=""
	go_snapshot_cgroup=""
	go_snapshot_top_k=100
	pod_name="snapshot-life-${BASHPID}-${RANDOM}"
	go_snapshot_pod="${pod_name}-1"
	go_snapshot_pod_label="app=${pod_name}"
	trap 'cleanup || exit $?' EXIT
	mkdir -p "${go_snapshot_case_dir}"
	snapshot_require_pod_image
	k8s_create_pod "${GO_SNAPSHOT_NAMESPACE}" "${pod_name}" "${BUSINESS_POD_IMAGE}" "${go_snapshot_pod_label}" 1
	assert_kubelet_pod_count "${GO_SNAPSHOT_NAMESPACE}" "^${go_snapshot_pod}$" 1
	go_snapshot_container_id=$(kubelet_container_ids "${GO_SNAPSHOT_NAMESPACE}" "^${go_snapshot_pod}$")
	cgroup_root=$(cgroup_memory_root) || fatal "memory cgroup mount missing"
	go_snapshot_cgroup=$(cgroup_find_container "${cgroup_root}" "${go_snapshot_container_id}") || fatal "container memory cgroup missing"
	go_snapshot_cgroup_path=${go_snapshot_cgroup#"${cgroup_root}"}
	cgroup_configure_memory "${go_snapshot_cgroup}" "${GO_SNAPSHOT_LIMIT}" || fatal "cannot configure test cgroup"
	go_snapshot_limit_file="${go_snapshot_cgroup}/memory.limit_in_bytes"
	lifecycle_usage="${go_snapshot_cgroup}/memory.usage_in_bytes"
	if [[ -e ${go_snapshot_cgroup}/memory.max ]]; then
		go_snapshot_limit_file="${go_snapshot_cgroup}/memory.max"
		lifecycle_usage="${go_snapshot_cgroup}/memory.current"
	fi
	[[ ${lifecycle_mode} != threshold ]] || lifecycle_threshold=99
	if [[ ${lifecycle_mode} == sigkill ]]; then
		# The overlay pauses after a successful remote read in this test only.
		HUATUO_BAMAI_BIN=${lifecycle_capture_bin}
	fi
	lifecycle_start
	lifecycle_workload 1
	lifecycle_no_event 0 2
	lifecycle_trigger 1
	case ${lifecycle_mode} in
	sigkill)
		lifecycle_assert_latest 1
		lifecycle_finish_workload
		cp "${go_snapshot_case_dir}/events/memory_threshold_snapshot" "${go_snapshot_case_dir}/before-kill.jsonl"
		lifecycle_stop TERM before
		lifecycle_workload 2
		export MEMSNAP_READ_BARRIER_DIR="${go_snapshot_case_dir}/barrier"
		export MEMSNAP_READ_BARRIER_PID="${go_snapshot_pid}"
		mkdir -p "${MEMSNAP_READ_BARRIER_DIR}"
		lifecycle_start
		lifecycle_trigger 2
		wait_until 15 0.01 test -f "${MEMSNAP_READ_BARRIER_DIR}/reached" || fatal "capture did not reach remote-read barrier"
		lifecycle_stop KILL interrupted
		[[ $(lifecycle_count) == 1 ]] || fatal "SIGKILL persisted an unfinished capture"
		cmp "${go_snapshot_case_dir}/before-kill.jsonl" "${go_snapshot_case_dir}/events/memory_threshold_snapshot" || fatal "SIGKILL changed completed events"
		unset MEMSNAP_READ_BARRIER_DIR MEMSNAP_READ_BARRIER_PID
		lifecycle_finish_workload
		lifecycle_start
		lifecycle_workload 3
		lifecycle_trigger 3
		lifecycle_assert_latest 2
		head -c "$(wc -c < "${go_snapshot_case_dir}/before-kill.jsonl")" "${go_snapshot_case_dir}/events/memory_threshold_snapshot" \
			| cmp - "${go_snapshot_case_dir}/before-kill.jsonl" || fatal "completed events changed after SIGKILL recovery"
		lifecycle_finish_workload
		lifecycle_stop TERM recovered
		;;
	threshold | blacklist)
		[[ $(< "${lifecycle_usage}") -lt $((GO_SNAPSHOT_LIMIT * 99 / 100)) ]] || fatal "pressure too high for negative threshold"
		lifecycle_no_event 0 5
		lifecycle_finish_workload
		lifecycle_stop TERM negative
		# A positive control on the same Pod distinguishes suppression from a broken setup.
		lifecycle_mode=enabled
		lifecycle_threshold=50
		lifecycle_start
		lifecycle_workload 2
		lifecycle_trigger 2
		lifecycle_assert_latest 1
		lifecycle_finish_workload
		lifecycle_stop TERM positive
		;;
	restart)
		lifecycle_assert_latest 1
		lifecycle_finish_workload
		cp "${go_snapshot_case_dir}/events/memory_threshold_snapshot" "${go_snapshot_case_dir}/before-restart.jsonl"
		lifecycle_stop TERM before
		jq -s -e 'length > 0' "${go_snapshot_case_dir}/events/memory_threshold_snapshot" > /dev/null
		lifecycle_start
		lifecycle_workload 2
		lifecycle_trigger 2
		lifecycle_assert_latest 2
		head -c "$(wc -c < "${go_snapshot_case_dir}/before-restart.jsonl")" "${go_snapshot_case_dir}/events/memory_threshold_snapshot" | cmp - "${go_snapshot_case_dir}/before-restart.jsonl" || fatal "previously completed events changed across restart"
		lifecycle_finish_workload
		lifecycle_stop TERM after
		;;
	cooldown)
		lifecycle_assert_latest 1
		lifecycle_first_observed=${SECONDS}
		lifecycle_finish_workload
		lifecycle_workload 2
		lifecycle_trigger 2
		# Fail if setup consumed the cooldown; otherwise a missing event proves nothing.
		((SECONDS - lifecycle_first_observed < 8)) || fatal "setup exceeded cooldown test budget"
		lifecycle_no_event 1 1
		lifecycle_finish_workload
		while ((SECONDS - lifecycle_first_observed < 11)); do sleep 0.2; done
		lifecycle_workload 3
		lifecycle_trigger 3
		lifecycle_assert_latest 2
		lifecycle_finish_workload
		lifecycle_stop TERM cooldown
		;;
	esac
	log_info "PASS: lifecycle ${1}"
)

go build -mod=vendor -o "${GO_SNAPSHOT_BIN}" "${ROOT_DIR}/e2e/testdata/memory_threshold_snapshot_golang.go"
huatuo_bamai_stop
huatuo_bamai_log_check || fatal "baseline daemon reported an error"
export MEMSNAP_ACCEPTANCE_ARTIFACTS="${HUATUO_BAMAI_TEST_TMPDIR}/helpers"
mkdir -p "${MEMSNAP_ACCEPTANCE_ARTIFACTS}"
snapshot_build_read_barrier
lifecycle_capture_bin="${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai-read-barrier"
overlay="${HUATUO_BAMAI_TEST_TMPDIR}/daemon-overlay.json"
jq -n --arg syscall "${ROOT_DIR}/vendor/golang.org/x/sys/unix/zsyscall_linux.go" \
	--arg source "${read_source}" '{Replace:{($syscall):$source}}' > "${overlay}"
go build -mod=vendor -overlay="${overlay}" -tags 'netgo osusergo' -o "${lifecycle_capture_bin}" ./cmd/huatuo-bamai
for lifecycle_mode in threshold blacklist restart cooldown sigkill; do
	lifecycle_case "${lifecycle_mode}"
done
