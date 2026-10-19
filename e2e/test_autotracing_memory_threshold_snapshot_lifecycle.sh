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
source "${ROOT_DIR}/e2e/lib_memory_threshold_snapshot.sh"
memory_threshold_snapshot_setup

assert_lifecycle_logs() {
	local file=$1 phase=$2
	go_snapshot_stage="${lifecycle_mode}: validate ${phase} daemon logs"
	if [[ ${phase} != negative && ${phase} != interrupted ]]; then
		# Only this daemon run's validated snapshot can prove sample-loss recovery.
		assert_snapshot_daemon_logs "${file}" "${go_snapshot_case_dir}/snapshot.json" "${go_snapshot_container_id}" \
			|| fatal "unexpected warning/error in ${file}"
	else
		assert_snapshot_daemon_logs "${file}" "" "${go_snapshot_container_id}" \
			"${lifecycle_watch_verified_at}" "${lifecycle_recovery_checkpoint}" \
			|| fatal "unexpected warning/error in ${file}"
	fi
}

lifecycle_write_config() {
	write_memory_threshold_snapshot_config
	sed -i "s/ThresholdPercent = 50/ThresholdPercent = ${lifecycle_threshold}/; s/IntervalTracing = 300/IntervalTracing = 30/" "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf"
	if [[ ${lifecycle_mode} == blacklist ]]; then
		sed -i 's/^BlackList = \[/BlackList = ["memory_threshold_snapshot", /' "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf"
	fi
	if [[ ${lifecycle_mode} == contention_* ]]; then
		sed -i 's/"memburst", //' "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf"
		if [[ ${lifecycle_mode} == contention_burst || ${lifecycle_mode} == contention_target_exit ]]; then
			sed -i 's/RunTracingToolTimeout = 10/RunTracingToolTimeout = 1/' "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf"
		fi
		cat >> "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" << CONFIG
[AutoTracing.MemoryBurst]
 DeltaMemoryBurst = 10
 DeltaAnonThreshold = 0
 Interval = 1
 SlidingWindowLength = 2
 IntervalTracing = 1
 DumpProcessMaxNum = 10
CONFIG
	fi
}

assert_lifecycle_metrics() {
	local enabled=1 running=1
	if [[ ${lifecycle_mode} == contention_* ]]; then
		running=2
		assert_memsnapshot_tracing_metrics memburst e2e 1 "${running}"
	fi
	[[ ${lifecycle_mode} != blacklist ]] || enabled=0
	[[ ${enabled} != 0 ]] || running=0
	assert_memsnapshot_tracing_metrics memory_threshold_snapshot e2e "${enabled}" "${running}"
}

lifecycle_start() {
	integration_huatuo_bamai_start lifecycle_write_config --region e2e --log-debug \
		--bpf-dir "${ROOT_DIR}/_output/bpf"
	if [[ ${lifecycle_mode} != blacklist ]]; then
		wait_until 30 0.1 go_snapshot_watch_is_ready || fatal "limit watch not registered"
	else
		assert_huatuo_bamai_containers_present "${GO_SNAPSHOT_NAMESPACE}" "^${go_snapshot_pod}$" 1
	fi
	assert_lifecycle_metrics
}

lifecycle_stop() {
	local signal=$1 phase=$2
	if [[ ${phase} == negative || ${signal} == KILL ]]; then
		# Recheck after the suppression window, in the same daemon run.
		if [[ ${lifecycle_mode} != blacklist ]]; then
			go_snapshot_watch_is_ready || fatal "could not confirm target watch is present during negative assertion"
			lifecycle_recovery_checkpoint="revalidated target watch"
		else
			huatuo_bamai_containers_present "${GO_SNAPSHOT_NAMESPACE}" "^${go_snapshot_pod}$" 1 \
				|| fatal "target container metadata disappeared during blacklist assertion"
			lifecycle_recovery_checkpoint="revalidated target container metadata"
		fi
		assert_lifecycle_metrics
		lifecycle_watch_verified_at=$(date -u '+%Y-%m-%dT%H:%M:%S.%NZ')
	fi
	huatuo_bamai_stop_with_signal "${signal}"
	mv "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${go_snapshot_case_dir}/daemon-${phase}.log"
	assert_lifecycle_logs "${go_snapshot_case_dir}/daemon-${phase}.log" "${phase}"
}

lifecycle_count() {
	local file="${go_snapshot_case_dir}/events/memory_threshold_snapshot"
	if [[ ! -e ${file} ]]; then
		printf '0\n'
		return
	fi
	jq -s --arg id "${go_snapshot_container_id}" '[.[] | select(.container_id == $id)] | length' "${file}"
}

lifecycle_suppression_window_complete() {
	local expected=$1 deadline=$2
	kill -0 "${go_snapshot_pid}" || fatal "workload exited during negative assertion"
	[[ $(lifecycle_count) == "${expected}" ]] || fatal "unexpected event during suppression window"
	((SECONDS >= deadline))
}

assert_lifecycle_no_event() {
	local expected=$1 seconds=$2
	local deadline=$((SECONDS + seconds))
	wait_until "$((seconds + 5))" .2 lifecycle_suppression_window_complete "${expected}" "${deadline}" \
		|| fatal "suppression observation window did not finish"
}

lifecycle_workload() {
	local phase=$1
	mkfifo "${go_snapshot_case_dir}/commands-${phase}"
	exec {go_snapshot_input_fd}<> "${go_snapshot_case_dir}/commands-${phase}"
	(
		snapshot_join_container_cgroup
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
	local phase=$1
	if [[ -e ${go_snapshot_cgroup}/memory.max ]]; then
		lifecycle_high_before=$(awk '$1=="high" {print $2}' "${go_snapshot_cgroup}/memory.events")
		snapshot_set_memory_high $((72 * 1024 * 1024))
	fi
	lifecycle_triggered_at=$(date -u '+%Y-%m-%dT%H:%M:%S.%NZ')
	printf p >&"${go_snapshot_input_fd}"
	if [[ -e ${go_snapshot_cgroup}/memory.max ]]; then
		wait_until 15 0.1 awk -v n="${lifecycle_high_before}" '$1=="high" {v=$2} END {exit !(v>n)}' "${go_snapshot_cgroup}/memory.events" || fatal "no real memory.high notification"
		snapshot_set_memory_high max
	fi
	wait_until 30 0.1 grep -qx 'pressure ready' "${go_snapshot_case_dir}/workload-${phase}.log" || fatal "pressure incomplete"
	[[ $(< "${lifecycle_usage}") -ge ${GO_SNAPSHOT_THRESHOLD} ]] || fatal "pressure did not cross 50 percent"
	assert_lifecycle_metrics
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

assert_lifecycle_latest() {
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
	assert_lifecycle_metrics
	assert_go_snapshot_statistics aggregation
	cp "${go_snapshot_case_dir}/snapshot.json" "${go_snapshot_case_dir}/snapshot-${expected}.json"
}

assert_lifecycle_interruption_preserved_events() {
	assert_eq "$(lifecycle_count)" 1 "SIGKILL must not persist an unfinished capture" || fatal "SIGKILL persisted an unfinished capture"
	cmp "${go_snapshot_case_dir}/before-kill.jsonl" "${go_snapshot_case_dir}/events/memory_threshold_snapshot" || fatal "SIGKILL changed completed events"
}

assert_lifecycle_saved_events() {
	jq -s -e 'length > 0' "${go_snapshot_case_dir}/events/memory_threshold_snapshot" > /dev/null || fatal "completed events missing after shutdown"
}

lifecycle_contention_input() {
	local level=$1
	lifecycle_sample_offset=0
	if [[ -f ${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log ]]; then
		lifecycle_sample_offset=$(wc -l < "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log")
	fi
	mount --bind "${go_snapshot_case_dir}/meminfo-${level}" /proc/meminfo
	lifecycle_meminfo_mounts=$((lifecycle_meminfo_mounts + 1))
}

lifecycle_contention_cleanup() {
	local status=$?
	[[ -z ${MEMSNAP_READ_BARRIER_DIR:-} ]] || touch "${MEMSNAP_READ_BARRIER_DIR}/release"
	huatuo_bamai_stop || status=1
	stop_and_wait_by_pid "${lifecycle_host_pid:-}" || true
	while ((lifecycle_meminfo_mounts > 0)); do
		umount /proc/meminfo || {
			status=1
			break
		}
		lifecycle_meminfo_mounts=$((lifecycle_meminfo_mounts - 1))
	done
	memory_threshold_snapshot_cleanup || status=1
	return "${status}"
}

lifecycle_burst_count() {
	[[ -s ${go_snapshot_case_dir}/events/memburst ]] || return 1
	[[ $(jq -s length "${go_snapshot_case_dir}/events/memburst") == "$1" ]]
}

assert_contention_burst() {
	local expected=$1 state=$2
	wait_until 15 .1 lifecycle_burst_count "${expected}" || fatal "contention memburst event missing"
	jq -se --argjson pid "${lifecycle_host_pid}" --arg state "${state}" '
 last as $e | $e.tracer_data as $d |
 $e.tracer_name == "memburst" and $e.tracer_type == "autotracing"
 and ($d.top_memory_usage | length > 0)
 and any($d.process_snapshots[]; .pid == $pid and
  (if $state == "busy" then
   .snapshot.status == "failed" and (.snapshot.status_reason | contains("another memory snapshot is in progress"))
  elif $state == "recovered" then
   .language == "go" and .snapshot.status == "complete"
   and any(.snapshot.entries[]; .name == "main.allocateBlock" and .bytes == 16777216 and .objects == 6)
  else (.snapshot.status | IN("complete", "failed")) end))
 ' "${go_snapshot_case_dir}/events/memburst" > /dev/null || fatal "incorrect contention result: ${state}"
}

assert_contention_threshold_pending() {
	assert_eq "$(lifecycle_count)" 0 "threshold persisted while collector was held" || fatal "unexpected threshold event"
	[[ ! -e ${MEMSNAP_READ_BARRIER_DIR}/expired ]] || fatal "read barrier expired before contention"
}

lifecycle_host_workload() {
	# Keep the host target above ordinary Kubernetes services in the RSS ranking.
	mkfifo "${go_snapshot_case_dir}/host-commands"
	exec {lifecycle_host_fd}<> "${go_snapshot_case_dir}/host-commands"
	MEMSNAPSHOT_PRESSURE_MIB=512 "${GO_SNAPSHOT_BIN}" 1 < "${go_snapshot_case_dir}/host-commands" > "${go_snapshot_case_dir}/host.log" 2>&1 &
	lifecycle_host_pid=$!
	printf a >&"${lifecycle_host_fd}"
	wait_until 30 .1 grep -qx ready "${go_snapshot_case_dir}/host.log" || fatal "host workload not ready"
	printf p >&"${lifecycle_host_fd}"
	wait_until 30 .1 grep -qx 'pressure ready' "${go_snapshot_case_dir}/host.log" || fatal "host pressure not ready"
}

assert_contention_canceled() {
	jq -se 'length == 1 and (last.tracer_data |
	 (.top_memory_usage | length > 0) and
	 (.snapshot_reason | contains("context canceled")) and
	 ((.process_snapshots // []) | length == 0))' "${go_snapshot_case_dir}/events/memburst" > /dev/null || fatal "worker cancellation did not retain the base event"
}

assert_contention_target_exit() {
	[[ -f ${MEMSNAP_READ_BARRIER_DIR}/target-exited ]] || fatal "remote read did not observe target exit"
	jq -se --argjson pid "${lifecycle_host_pid}" '
	 last.tracer_data | any(.process_snapshots[];
	 .pid == $pid and .snapshot.status == "failed" and
	 (.snapshot.status_reason | length > 0))' "${go_snapshot_case_dir}/events/memburst" > /dev/null || fatal "target exit lost its failure reason"
}

lifecycle_threshold_wait_expired() {
	awk -v cgroup="${go_snapshot_cgroup_path}" '
	 index($0, cgroup) && /msg="memory threshold snapshot capture finished"/ &&
	 /another memory snapshot is in progress/ && /wait budget exceeded/ { found=1 }
	 END { exit !found }
	' "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log"
}

lifecycle_contention() {
	local level active available
	available=$(awk '/^MemAvailable:/ {print $2}' /proc/meminfo)
	((available > 1048576)) || skip "contention requires at least 1 GiB available memory"
	lifecycle_meminfo_mounts=0
	lifecycle_host_pid=""
	require_commands mount umount
	[[ $(readlink /proc/self/ns/mnt) != $(readlink /proc/1/ns/mnt) ]] || fatal "contention requires the runner mount namespace"
	trap lifecycle_contention_cleanup EXIT
	for level in low high; do
		active=100000
		[[ ${level} != high ]] || active=400000
		printf 'MemTotal: 1000000 kB\nMemFree: 500000 kB\nMemAvailable: 500000 kB\nActive(anon): %s kB\nInactive(anon): 10000 kB\n' "${active}" > "${go_snapshot_case_dir}/meminfo-${level}"
	done
	lifecycle_contention_input low
	lifecycle_workload 1
	lifecycle_host_workload
	export MEMSNAP_READ_BARRIER_DIR="${go_snapshot_case_dir}/barrier"
	export MEMSNAP_READ_BARRIER_PID=${go_snapshot_pid}
	[[ ${lifecycle_mode} == contention_threshold ]] || MEMSNAP_READ_BARRIER_PID=${lifecycle_host_pid}
	mkdir -p "${MEMSNAP_READ_BARRIER_DIR}"
	HUATUO_BAMAI_BIN=${lifecycle_capture_bin}
	lifecycle_start
	memsnapshot_wait_memburst_samples 0 2 100000
	if [[ ${lifecycle_mode} == contention_threshold ]]; then
		lifecycle_trigger 1
		wait_until 10 .01 snapshot_read_barrier_ready || fatal "threshold did not enter remote read"
		lifecycle_contention_input high
		assert_contention_burst 1 busy
	else
		lifecycle_contention_input high
		wait_until 10 .01 snapshot_read_barrier_ready || fatal "host target was not captured among the top three processes"
		if [[ ${lifecycle_mode} == contention_cancel ]]; then
			go_snapshot_watch_is_ready || fatal "could not confirm target watch is present before cancellation"
			lifecycle_watch_verified_at=$(date -u '+%Y-%m-%dT%H:%M:%S.%NZ')
			kill -TERM "$(cat "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")"
			wait_until 10 .01 grep -Fq 'msg="Caller request to stop"' "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" || fatal "memburst did not observe context cancellation"
			touch "${MEMSNAP_READ_BARRIER_DIR}/release"
			huatuo_bamai_stop_with_signal TERM sent
			assert_contention_canceled
			assert_snapshot_daemon_logs "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "" "${go_snapshot_container_id}" \
				"${lifecycle_watch_verified_at}" "revalidated target watch" || fatal "unexpected cancellation warning/error"
			mv "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${go_snapshot_case_dir}/daemon-canceled.log"
			lifecycle_contention_input low
			lifecycle_start
			memsnapshot_wait_memburst_samples 0 2 100000
			lifecycle_contention_input high
			assert_contention_burst 2 recovered
			lifecycle_trigger 1
			assert_lifecycle_latest 1
			lifecycle_stop TERM recovered
			lifecycle_finish_workload
			return
		fi
		if [[ ${lifecycle_mode} == contention_target_exit ]]; then
			kill -KILL "${lifecycle_host_pid}"
			wait "${lifecycle_host_pid}" 2> /dev/null || true
		fi
		lifecycle_trigger 1
		# The capture result log identifies the real queue wait-budget expiry.
		wait_until 10 .01 lifecycle_threshold_wait_expired || fatal "threshold did not encounter collector contention"
	fi
	assert_contention_threshold_pending
	touch "${MEMSNAP_READ_BARRIER_DIR}/release"
	wait_until 10 .1 lifecycle_expected_count 1 || fatal "threshold did not retry before its 30-second cooldown"
	assert_lifecycle_latest 1
	assert_contention_burst 1 released
	if [[ ${lifecycle_mode} == contention_target_exit ]]; then
		assert_contention_target_exit
		exec {lifecycle_host_fd}>&-
		rm "${go_snapshot_case_dir}/host-commands"
		lifecycle_host_workload
	fi
	cp "${go_snapshot_case_dir}/events/memburst" "${go_snapshot_case_dir}/before-recovery"
	lifecycle_contention_input low
	memsnapshot_wait_memburst_samples "${lifecycle_sample_offset}" 2 100000
	lifecycle_contention_input high
	assert_contention_burst 2 recovered
	assert_memsnapshot_event_prefix "${go_snapshot_case_dir}/before-recovery" "${go_snapshot_case_dir}/events/memburst" "recovery changed previous contention event"
	lifecycle_stop TERM contention
	lifecycle_finish_workload
	printf q >&"${lifecycle_host_fd}"
	wait "${lifecycle_host_pid}" || fatal "host workload failed"
	lifecycle_host_pid=""
	exec {lifecycle_host_fd}>&-
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
	trap 'memory_threshold_snapshot_cleanup || exit $?' EXIT
	mkdir -p "${go_snapshot_case_dir}"
	snapshot_require_pod_image
	snapshot_create_pod
	assert_kubelet_pod_count "${GO_SNAPSHOT_NAMESPACE}" "^${go_snapshot_pod}$" 1
	go_snapshot_container_id=$(kubelet_container_ids "${GO_SNAPSHOT_NAMESPACE}" "^${go_snapshot_pod}$")
	cgroup_root=$(cgroup_memory_root) || fatal "memory cgroup mount missing"
	go_snapshot_cgroup=$(cgroup_find_container "${cgroup_root}" "${go_snapshot_container_id}") || fatal "container memory cgroup missing"
	if [[ ${go_snapshot_cgroup} == *.scope ]]; then
		require_commands busctl
	fi
	go_snapshot_cgroup_path=${go_snapshot_cgroup#"${cgroup_root}"}
	cgroup_configure_memory "${go_snapshot_cgroup}" "${GO_SNAPSHOT_LIMIT}" || fatal "cannot configure test cgroup"
	go_snapshot_limit_file="${go_snapshot_cgroup}/memory.limit_in_bytes"
	lifecycle_usage="${go_snapshot_cgroup}/memory.usage_in_bytes"
	if [[ -e ${go_snapshot_cgroup}/memory.max ]]; then
		go_snapshot_limit_file="${go_snapshot_cgroup}/memory.max"
		lifecycle_usage="${go_snapshot_cgroup}/memory.current"
	fi
	if [[ ${lifecycle_mode} == contention_* ]]; then
		lifecycle_contention
		log_info "PASS: lifecycle ${lifecycle_mode}"
		return
	fi
	[[ ${lifecycle_mode} != threshold ]] || lifecycle_threshold=99
	if [[ ${lifecycle_mode} == sigkill ]]; then
		# The fault overlay pauses after a successful remote read in this test only.
		HUATUO_BAMAI_BIN=${lifecycle_capture_bin}
	fi
	lifecycle_start
	lifecycle_workload 1
	assert_lifecycle_no_event 0 2
	lifecycle_trigger 1
	case ${lifecycle_mode} in
	sigkill)
		assert_lifecycle_latest 1
		lifecycle_finish_workload
		cp "${go_snapshot_case_dir}/events/memory_threshold_snapshot" "${go_snapshot_case_dir}/before-kill.jsonl"
		lifecycle_stop TERM before
		lifecycle_workload 2
		export MEMSNAP_READ_BARRIER_DIR="${go_snapshot_case_dir}/barrier"
		export MEMSNAP_READ_BARRIER_PID="${go_snapshot_pid}"
		mkdir -p "${MEMSNAP_READ_BARRIER_DIR}"
		lifecycle_start
		lifecycle_trigger 2
		wait_until 15 0.01 snapshot_read_barrier_ready || fatal "capture did not reach remote-read barrier"
		lifecycle_stop KILL interrupted
		assert_lifecycle_interruption_preserved_events
		unset MEMSNAP_READ_BARRIER_DIR MEMSNAP_READ_BARRIER_PID
		lifecycle_finish_workload
		lifecycle_start
		lifecycle_workload 3
		lifecycle_trigger 3
		assert_lifecycle_latest 2
		assert_memsnapshot_event_prefix "${go_snapshot_case_dir}/before-kill.jsonl" \
			"${go_snapshot_case_dir}/events/memory_threshold_snapshot" "completed events changed after SIGKILL recovery"
		lifecycle_finish_workload
		lifecycle_stop TERM recovered
		;;
	threshold | blacklist)
		[[ $(< "${lifecycle_usage}") -lt $((GO_SNAPSHOT_LIMIT * 99 / 100)) ]] || fatal "pressure too high for negative threshold"
		assert_lifecycle_no_event 0 5
		lifecycle_finish_workload
		lifecycle_stop TERM negative
		# A positive control on the same Pod distinguishes suppression from a broken setup.
		lifecycle_mode=enabled
		lifecycle_threshold=50
		lifecycle_start
		lifecycle_workload 2
		lifecycle_trigger 2
		assert_lifecycle_latest 1
		lifecycle_finish_workload
		lifecycle_stop TERM positive
		;;
	restart)
		assert_lifecycle_latest 1
		lifecycle_finish_workload
		cp "${go_snapshot_case_dir}/events/memory_threshold_snapshot" "${go_snapshot_case_dir}/before-restart.jsonl"
		lifecycle_stop TERM before
		assert_lifecycle_saved_events
		lifecycle_start
		lifecycle_workload 2
		lifecycle_trigger 2
		assert_lifecycle_latest 2
		assert_memsnapshot_event_prefix "${go_snapshot_case_dir}/before-restart.jsonl" \
			"${go_snapshot_case_dir}/events/memory_threshold_snapshot" "previously completed events changed across restart"
		lifecycle_finish_workload
		lifecycle_stop TERM after
		;;
	cooldown)
		assert_lifecycle_latest 1
		lifecycle_first_observed=${SECONDS}
		lifecycle_first_started=$(date -d "$(jq -r .started_timestamp "${go_snapshot_case_dir}/snapshot.json")" +%s)
		lifecycle_finish_workload
		lifecycle_workload 2
		lifecycle_trigger 2
		# Fail if setup consumed the cooldown; otherwise a missing event proves nothing.
		(($(date +%s) - lifecycle_first_started < 28)) || fatal "setup exceeded cooldown test budget"
		assert_lifecycle_no_event 1 1
		lifecycle_finish_workload
		wait_until 35 .2 memsnapshot_cooldown_elapsed "${lifecycle_first_observed}" 31 \
			|| fatal "threshold snapshot cooldown deadline was not reached"
		lifecycle_workload 3
		lifecycle_trigger 3
		assert_lifecycle_latest 2
		lifecycle_finish_workload
		lifecycle_stop TERM cooldown
		;;
	esac
	log_info "PASS: lifecycle ${1}"
)

go build -mod=vendor -o "${GO_SNAPSHOT_BIN}" "${ROOT_DIR}/integration/testdata/test_memory_threshold_snapshot_golang.go"
huatuo_bamai_stop
huatuo_bamai_log_check || fatal "baseline daemon reported an error"
export MEMSNAP_ACCEPTANCE_ARTIFACTS="${HUATUO_BAMAI_TEST_TMPDIR}/helpers"
mkdir -p "${MEMSNAP_ACCEPTANCE_ARTIFACTS}"
snapshot_build_read_barrier
lifecycle_capture_bin="${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai-read-barrier"
lifecycle_overlay="${HUATUO_BAMAI_TEST_TMPDIR}/daemon-overlay.json"
jq -n --arg syscall "${ROOT_DIR}/vendor/golang.org/x/sys/unix/zsyscall_linux.go" --arg source "${memsnapshot_read_source}" '{Replace:{($syscall):$source}}' > "${lifecycle_overlay}"
go build -mod=vendor -overlay="${lifecycle_overlay}" -tags 'netgo osusergo' -o "${lifecycle_capture_bin}" ./cmd/huatuo-bamai
for lifecycle_mode in threshold blacklist restart cooldown sigkill contention_threshold contention_burst contention_target_exit contention_cancel; do
	lifecycle_case "${lifecycle_mode}"
done
