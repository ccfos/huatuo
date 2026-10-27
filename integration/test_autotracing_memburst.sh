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

# Host-only integration: private procfs input, real processes, collector and daemon.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"
source "${ROOT_DIR}/integration/lib_memsnapshot.sh"
require_commands go jq mount umount awk unshare timeout
[[ ${EUID} == 0 ]] || skip "requires root and a private mount namespace"
# Never replace procfs in the host mount namespace when invoked manually.
if [[ ${MEMBURST_PRIVATE_PIDNS:-0} != 1 ]]; then
	[[ $(readlink /proc/self/ns/mnt) != $(readlink /proc/1/ns/mnt) ]] || fatal "run through integration/run.sh (private mount namespace required)"
	# Product scenarios use controlled rankings in their own PID namespace.
	export MEMBURST_PRIVATE_PIDNS=1
	exec unshare --pid --fork --mount-proc bash "${BASH_SOURCE[0]}"
fi
readonly MEMBURST_ROOT_WORKSPACE=${HUATUO_BAMAI_TEST_TMPDIR}
readonly MEMBURST_STANDARD_BIN=${HUATUO_BAMAI_BIN}
export MEMSNAP_ACCEPTANCE_ARTIFACTS="${MEMBURST_ROOT_WORKSPACE}/helpers"
export MEMBURST_ACCEPTANCE_FIXTURE="${MEMSNAP_ACCEPTANCE_ARTIFACTS}/go-snapshot"
mkdir -p "${MEMSNAP_ACCEPTANCE_ARTIFACTS}"
cd "${ROOT_DIR}"
if [[ -n ${MEMBURST_ACCEPTANCE_PREBUILT_DIR:-} ]]; then
	cp "${MEMBURST_ACCEPTANCE_PREBUILT_DIR}/go-snapshot" "${MEMBURST_ACCEPTANCE_FIXTURE}"
fi
if [[ ! -x ${MEMBURST_ACCEPTANCE_FIXTURE} ]]; then
	go build -mod=vendor -o "${MEMBURST_ACCEPTANCE_FIXTURE}" ./integration/testdata/test_memory_threshold_snapshot_golang.go
fi
# Test only /proc/meminfo is replaced. RSS rankings and remote memory are real.
for memburst_input_level in low high; do
	memburst_input_value=100000
	[[ ${memburst_input_level} != high ]] || memburst_input_value=400000
	printf 'MemTotal: 1000000 kB\nMemFree: 500000 kB\nMemAvailable: 500000 kB\nActive(anon): %s kB\nInactive(anon): 10000 kB\n' "${memburst_input_value}" > "${MEMBURST_ROOT_WORKSPACE}/meminfo-${memburst_input_level}"
done
burst_mounted=0
burst_workloads=()
burst_fds=()
burst_cleanup() {
	local status=$? pid fd
	huatuo_bamai_stop || status=1
	for pid in "${burst_workloads[@]}"; do
		kill -KILL "${pid}" 2> /dev/null || true
		wait "${pid}" 2> /dev/null || true
	done
	for fd in "${burst_fds[@]}"; do eval "exec ${fd}>&-"; done
	while ((burst_mounted > 0)); do
		umount /proc/meminfo || {
			status=1
			break
		}
		burst_mounted=$((burst_mounted - 1))
	done
	return "${status}"
}
trap burst_cleanup EXIT
# Stacked binds switch inputs atomically, without exposing host meminfo between samples.
burst_input() {
	burst_sample_offset=0
	if [[ -f ${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log ]]; then
		burst_sample_offset=$(wc -l < "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log")
	fi
	mount --bind "${MEMBURST_ROOT_WORKSPACE}/meminfo-$1" /proc/meminfo
	burst_mounted=$((burst_mounted + 1))
}

burst_wait_samples() {
	local memburst_input_level=$1 count=${2:-2} active=100000
	[[ ${memburst_input_level} != high ]] || active=400000
	memsnapshot_wait_memburst_samples "${burst_sample_offset}" "${count}" "${active}"
}

burst_count() {
	if [[ ! -s ${HUATUO_BAMAI_TEST_TMPDIR}/events/memburst ]]; then
		echo 0
		return
	fi
	jq -s length "${HUATUO_BAMAI_TEST_TMPDIR}/events/memburst" 2> /dev/null
}
burst_expected_count() { [[ $(burst_count) == "$1" ]]; }
assert_burst_count() {
	assert_eq "$(burst_count)" "$1" "${2:-memburst event count}"
}

assert_burst_cooldown() {
	(($(date +%s) - memburst_first_started < 28)) || fatal "cooldown assertion exceeded budget"
	assert_burst_count 1 "cooldown did not suppress burst"
}

assert_burst_configuration_rejected() {
	local name=$1 status=$2 file=$3
	[[ ${status} != 0 && ${status} != 124 && ${status} != 137 ]] || fatal "invalid ${name} did not fail promptly (status=${status})"
	grep -Fq "${memburst_config_error}" "${file}" || fatal "missing ${name} diagnostic: ${memburst_config_error}"
	! grep -q 'started successfully' "${file}" || fatal "daemon started with invalid ${name}"
}

GO_SNAPSHOT_BAMAI_PORT=$(allocate_available_port)
GO_SNAPSHOT_KUBELET_PORT=10250
go_snapshot_top_k=100
KUBELET_CERT=${KUBELET_CERT:-unused}
KUBELET_KEY=${KUBELET_KEY:-unused}
HUATUO_BAMAI_ADDR="http://127.0.0.1:${GO_SNAPSHOT_BAMAI_PORT}"
HUATUO_BAMAI_METRICS_API="${HUATUO_BAMAI_ADDR}/metrics"
burst_top=10
burst_threshold=10
burst_anon=0
burst_blacklist=0
burst_cooldown=30
burst_window=2
write_config_bamai_memburst() {
	local go_snapshot_case_dir=${HUATUO_BAMAI_TEST_TMPDIR}
	local blacklist_filter=''
	if [[ ${burst_blacklist} == 1 ]]; then
		blacklist_filter='s/^BlackList = \[/BlackList = ["memburst", /'
	fi
	write_config_bamai_memory_threshold_snapshot \
		| sed -e 's/"memburst", //; s/, "tracing_status"//; s/^BlackList = \[/BlackList = ["memory_threshold_snapshot", /' \
			-e "${blacklist_filter}"
	cat << CONFIG
[AutoTracing.MemoryBurst]
 DeltaMemoryBurst = ${burst_threshold}
 DeltaAnonThreshold = ${burst_anon}
 Interval = 1
 IntervalTracing = ${burst_cooldown}
 DumpProcessMaxNum = ${burst_top}
CONFIG
	if [[ -n ${burst_window} ]]; then
		printf ' SlidingWindowLength = %s\n' "${burst_window}"
	fi
}
burst_start() {
	local pid
	for pid in "${burst_workloads[@]}"; do kill -0 "${pid}" || fatal "workload exited before scenario"; done
	integration_huatuo_bamai_start write_config_bamai_memburst --region integration --disable-kubelet --log-debug
	local enabled=1
	[[ ${burst_blacklist} == 0 ]] || enabled=0
	assert_memsnapshot_tracing_metrics memburst integration "${enabled}"
}
burst_stop() {
	local signal=$1
	assert_huatuo_bamai_shutdown_with_signal "${signal}" "${2:-}"
	if [[ -f ${HUATUO_BAMAI_TEST_TMPDIR}/expected-storage-failure ]]; then
		# Only the deliberate directory obstruction may produce a save warning.
		awk '!(/level="warning"/ && /msg="save memburst snapshot"/ && /not a directory/)' \
			"${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" > "${HUATUO_BAMAI_TEST_TMPDIR}/checked.log"
		assert_memsnapshot_logs "${HUATUO_BAMAI_TEST_TMPDIR}/checked.log"
	else
		assert_memsnapshot_logs "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log"
	fi
	mv "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${HUATUO_BAMAI_TEST_TMPDIR}/daemon-${signal}-${SECONDS}.log"
}

assert_burst_event() {
	assert_memsnapshot_tracing_metrics memburst integration 1
	jq -s -e --argjson n "${burst_top}" --argjson fixtures "$(printf '%s\n' "${burst_workloads[@]}" | jq -s .)" '
 last as $e | $e.tracer_data as $d |
 $e.tracer_name == "memburst" and $e.tracer_type == "autotracing"
 and $e.region == "integration" and ($e.hostname|length>0)
 and (($e.container_id // "") == "")
 and ($e.started_timestamp|type=="string") and $e.uploaded_timestamp >= $e.started_timestamp
 and ($d.top_memory_usage|length>0 and length<=$n)
 and ([$d.top_memory_usage[].MemSize] == ([$d.top_memory_usage[].MemSize]|sort|reverse))
 and ($d.process_snapshots|length==([3, ($d.top_memory_usage|length)]|min))
 and any($d.process_snapshots[]; .pid as $pid | ($fixtures | index($pid)) != null)
 and all(range(0;($d.process_snapshots|length)); . as $i | $d.process_snapshots[$i] as $s |
   $s.pid == $d.top_memory_usage[$i].PID and $s.process_name == $d.top_memory_usage[$i].ProcessName
   and ($s.snapshot.status | IN("complete", "partial", "unavailable", "failed"))
   and (if ($fixtures | index($s.pid)) != null then
   $s.language == "go" and $s.snapshot.status == "complete"
   and $s.process_memory.rss_bytes >= 23068672
   and ($s.snapshot.runtime_version|length>0)
   and ($s.snapshot.entries|length>0 and length<=10)
   and any($s.snapshot.entries[]; .name=="main.allocateBlock" and .objects==6 and .bytes==16777216)
   and any($s.snapshot.entries[]; .name=="main.allocateBlock" and .objects==3 and .bytes==6291456)
   else true end))
 ' "${HUATUO_BAMAI_TEST_TMPDIR}/events/memburst" > /dev/null || fatal "incorrect persisted memburst event"
}

# Exercise the real daemon entry point, not just the TOML loader and validator.
while IFS='|' read -r memburst_invalid memburst_config_field memburst_input_value memburst_config_error; do
	HUATUO_BAMAI_TEST_TMPDIR="${MEMBURST_ROOT_WORKSPACE}/invalid-${memburst_invalid}"
	mkdir -p "${HUATUO_BAMAI_TEST_TMPDIR}"
	write_config_bamai_memburst > "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf"
	sed -i "s/^ ${memburst_config_field} = .*/ ${memburst_config_field} = ${memburst_input_value}/" "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf"
	status=0
	timeout --kill-after=2s 10s "${HUATUO_BAMAI_BIN}" \
		--config-dir "${HUATUO_BAMAI_TEST_TMPDIR}" --config bamai.conf \
		--region integration --disable-kubelet > "${HUATUO_BAMAI_TEST_TMPDIR}/startup.log" 2>&1 || status=$?
	assert_burst_configuration_rejected "${memburst_invalid}" "${status}" "${HUATUO_BAMAI_TEST_TMPDIR}/startup.log"
	log_info "PASS: daemon rejects ${memburst_invalid} configuration"
done << 'CASES'
delta-zero|DeltaMemoryBurst|0|memory burst delta must be positive
delta-negative|DeltaMemoryBurst|-1|memory burst delta must be positive
anon-negative|DeltaAnonThreshold|-1|memory burst anon threshold must be in [0, 100]
anon-over|DeltaAnonThreshold|101|memory burst anon threshold must be in [0, 100]
interval-zero|Interval|0|memory burst interval must be positive
interval-negative|Interval|-1|memory burst interval must be positive
cooldown-zero|IntervalTracing|0|memory burst tracing interval must be positive
cooldown-negative|IntervalTracing|-1|memory burst tracing interval must be positive
window-zero|SlidingWindowLength|0|memory burst sliding window length must be positive
window-negative|SlidingWindowLength|-1|memory burst sliding window length must be positive
window-over|SlidingWindowLength|86401|memory burst sliding window length must not exceed 86400 samples
ranking-zero|DumpProcessMaxNum|0|memory burst dump process max num must be positive
ranking-negative|DumpProcessMaxNum|-1|memory burst dump process max num must be positive
wrong-type|DumpProcessMaxNum|"invalid"|invalid(string) to int
CASES
# Keep three profiled processes above the daemon in real RSS rankings.
for i in 1 2 3; do
	mkfifo "${MEMBURST_ROOT_WORKSPACE}/commands-${i}"
	exec {fd}<> "${MEMBURST_ROOT_WORKSPACE}/commands-${i}"
	burst_fds+=("${fd}")
	"${MEMBURST_ACCEPTANCE_FIXTURE}" 1 < "${MEMBURST_ROOT_WORKSPACE}/commands-${i}" > "${MEMBURST_ROOT_WORKSPACE}/workload-${i}.log" 2>&1 &
	burst_workloads+=("$!")
	printf a >&"${fd}"
	wait_until 20 .1 grep -qx ready "${MEMBURST_ROOT_WORKSPACE}/workload-${i}.log" || fatal "workload not ready"
	printf p >&"${fd}"
	wait_until 20 .1 grep -qx 'pressure ready' "${MEMBURST_ROOT_WORKSPACE}/workload-${i}.log" || fatal "real allocation pressure failed"
done
huatuo_bamai_stop
for memburst_scenario in normal threshold anon_threshold anon_maximum window_minimum blacklist top1 top2 top3 top11 default_window; do
	HUATUO_BAMAI_TEST_TMPDIR="${MEMBURST_ROOT_WORKSPACE}/${memburst_scenario}"
	mkdir -p "${HUATUO_BAMAI_TEST_TMPDIR}"
	burst_top=10
	burst_threshold=10
	burst_anon=0
	burst_blacklist=0
	burst_window=2
	[[ ${memburst_scenario} != window_minimum ]] || burst_window=1
	[[ ${memburst_scenario} != default_window ]] || burst_window=""
	[[ ${memburst_scenario} != anon_maximum ]] || burst_anon=100
	[[ ${memburst_scenario} != top1 ]] || burst_top=1
	[[ ${memburst_scenario} != top2 ]] || burst_top=2
	[[ ${memburst_scenario} != top3 ]] || burst_top=3
	[[ ${memburst_scenario} != top11 ]] || burst_top=11
	[[ ${memburst_scenario} != threshold ]] || burst_threshold=1000
	[[ ${memburst_scenario} != anon_threshold ]] || burst_anon=70
	[[ ${memburst_scenario} != blacklist ]] || burst_blacklist=1
	burst_input low
	burst_start
	if [[ ${memburst_scenario} == default_window ]]; then
		# Omit the setting: an early burst must not bypass the default history.
		burst_wait_samples low
		burst_input high
		burst_wait_samples high 3
		assert_burst_count 0 "default history triggered before filling"
		burst_input low
		wait_until 90 .2 memsnapshot_memburst_samples_ready "${burst_sample_offset}" 60 100000 \
			|| fatal "default memburst history did not fill"
	fi
	[[ ${burst_blacklist} == 1 ]] || burst_wait_samples low
	assert_burst_count 0 "event without burst"
	burst_input high
	if [[ ${memburst_scenario} == threshold || ${memburst_scenario} == anon_threshold || ${memburst_scenario} == anon_maximum || ${memburst_scenario} == window_minimum || ${memburst_scenario} == blacklist ]]; then
		[[ ${burst_blacklist} == 1 ]] || burst_wait_samples high 3
		huatuo_bamai_ready || fatal "daemon unavailable during suppression assertion"
		assert_burst_count 0 "suppressed burst produced event"
		local_enabled=1
		[[ ${burst_blacklist} == 0 ]] || local_enabled=0
		assert_memsnapshot_tracing_metrics memburst integration "${local_enabled}"
	else
		wait_until 20 .1 burst_expected_count 1 || fatal "burst did not persist"
		assert_burst_event
		if [[ ${memburst_scenario} == normal ]]; then
			memburst_first_at=${SECONDS}
			memburst_first_started=$(date -d "$(jq -sr 'last.started_timestamp' "${HUATUO_BAMAI_TEST_TMPDIR}/events/memburst")" +%s)
			burst_input low
			burst_wait_samples low
			burst_input high
			burst_wait_samples high 3
			assert_burst_cooldown
			burst_input low
			wait_until 35 .2 memsnapshot_cooldown_elapsed "${memburst_first_at}" 31 \
				|| fatal "memburst cooldown deadline was not reached"
			burst_input high
			wait_until 20 .1 burst_expected_count 2 || fatal "capture did not recover after cooldown"
			assert_burst_event
		fi
	fi
	burst_stop TERM
	log_info "PASS: memburst ${memburst_scenario}"
done
log_info "PASS: memburst controlled-input scenarios"
burst_window=2

# Restart acceptance uses the standard daemon.
# The standard daemon must retain completed events and recover after TERM/KILL.
for memburst_signal in TERM KILL; do
	HUATUO_BAMAI_TEST_TMPDIR="${MEMBURST_ROOT_WORKSPACE}/restart-${memburst_signal}"
	mkdir -p "${HUATUO_BAMAI_TEST_TMPDIR}"
	burst_top=10
	burst_threshold=10
	burst_anon=0
	burst_blacklist=0
	burst_input low
	burst_start
	burst_wait_samples low
	burst_input high
	wait_until 20 .1 burst_expected_count 1 || fatal "daemon did not persist event before ${memburst_signal}"
	assert_burst_event
	cp "${HUATUO_BAMAI_TEST_TMPDIR}/events/memburst" "${HUATUO_BAMAI_TEST_TMPDIR}/before-stop"
	burst_stop "${memburst_signal}"
	assert_memsnapshot_event_prefix "${HUATUO_BAMAI_TEST_TMPDIR}/before-stop" "${HUATUO_BAMAI_TEST_TMPDIR}/events/memburst" \
		"${memburst_signal} restart changed an existing event"
	burst_input low
	burst_start
	burst_wait_samples low
	burst_input high
	wait_until 20 .1 burst_expected_count 2 || fatal "daemon did not recover after ${memburst_signal}"
	assert_burst_event
	assert_memsnapshot_event_prefix "${HUATUO_BAMAI_TEST_TMPDIR}/before-stop" "${HUATUO_BAMAI_TEST_TMPDIR}/events/memburst" \
		"${memburst_signal} restart changed an existing event"
	burst_stop TERM
	log_info "PASS: standard memburst daemon ${memburst_signal}, event retention and restart"
done

# Exercise the shared collector through the standard daemon instead of Go APIs.
# Isolate runtime fixtures so each remains among the first three RSS-ranked PIDs.
for pid in "${burst_workloads[@]}"; do
	kill -KILL "${pid}"
	wait "${pid}" 2> /dev/null || true
done
burst_workloads=()
readonly MEMBURST_RUNTIME_DIR="${MEMBURST_ROOT_WORKSPACE}/runtimes"
mkdir -p "${MEMBURST_RUNTIME_DIR}"
MEMSNAP_OPTIONAL_RUNTIMES=1 HUATUO_BAMAI_TEST_TMPDIR="${MEMBURST_RUNTIME_DIR}" snapshot_build_runtime_fixtures

burst_start_runtime() {
	local mode=$1
	mkfifo "${HUATUO_BAMAI_TEST_TMPDIR}/commands"
	exec {burst_runtime_fd}<> "${HUATUO_BAMAI_TEST_TMPDIR}/commands"
	burst_fds+=("${burst_runtime_fd}")
	case ${mode} in
	go) burst_command=("${MEMBURST_ACCEPTANCE_FIXTURE}" 1) ;;
	disabled) burst_command=("${MEMBURST_ACCEPTANCE_FIXTURE}" 0) ;;
	churn) burst_command=(env MEMSNAPSHOT_CHURN=1 "${MEMBURST_ACCEPTANCE_FIXTURE}" 1) ;;
	python) burst_command=(python3 "${MEMBURST_RUNTIME_DIR}/snapshot.py") ;;
	java) burst_command=(java -XX:+UseG1GC -Xms32m -Xmx256m -cp "${MEMBURST_RUNTIME_DIR}" HeapFixture) ;;
	c | cpp) burst_command=("${MEMBURST_RUNTIME_DIR}/${mode}-snapshot") ;;
	esac
	"${burst_command[@]}" < "${HUATUO_BAMAI_TEST_TMPDIR}/commands" > "${HUATUO_BAMAI_TEST_TMPDIR}/workload.log" 2>&1 &
	burst_runtime_pid=$!
	burst_workloads=("${burst_runtime_pid}")
	printf a >&"${burst_runtime_fd}"
	wait_until 30 .1 grep -qx ready "${HUATUO_BAMAI_TEST_TMPDIR}/workload.log" || fatal "${mode}: allocation not ready"
	printf p >&"${burst_runtime_fd}"
	wait_until 30 .1 grep -qx 'pressure ready' "${HUATUO_BAMAI_TEST_TMPDIR}/workload.log" || fatal "${mode}: pressure not ready"
}

assert_burst_runtime() {
	local mode=$1 language=${1} count=$2
	go_snapshot_case_dir=${HUATUO_BAMAI_TEST_TMPDIR}
	case ${mode} in go | disabled | churn) language=go ;; c) language=native ;; esac
	jq -se --argjson pid "${burst_runtime_pid}" --arg language "${language}" '
 last.tracer_data as $d |
 ($d.top_memory_usage | length > 0 and length <= 10)
 and ($d.process_snapshots | length == ([3, ($d.top_memory_usage | length)] | min))
 and all(range(0; ($d.process_snapshots | length)); . as $i |
  $d.process_snapshots[$i].pid == $d.top_memory_usage[$i].PID)
 and any($d.process_snapshots[]; .pid == $pid and .language == $language
  and .process_memory.rss_bytes >= 8388608)
 and (($d | tojson | utf8bytelength) <= 2097152)
 ' "${go_snapshot_case_dir}/events/memburst" > /dev/null || fatal "${mode}: invalid runtime identity or event budget"
	jq -s --argjson pid "${burst_runtime_pid}" \
		'{tracer_data:(last.tracer_data.process_snapshots[] | select(.pid == $pid))}' \
		"${go_snapshot_case_dir}/events/memburst" > "${go_snapshot_case_dir}/snapshot.json"
	case ${mode} in
	go | disabled) assert_go_snapshot_statistics "${mode}" ;;
	churn)
		jq -e '.tracer_data.snapshot | (.status == "complete" or .status == "partial")
 and (.entries | length > 0 and length <= 10)
 and all(.entries[]; .objects > 0 and .average_bytes == (.bytes / .objects))' \
			"${go_snapshot_case_dir}/snapshot.json" > /dev/null || fatal "invalid concurrent allocation snapshot"
		;;
	*) assert_snapshot_runtime_payload "${language}" ;;
	esac
	assert_burst_count "${count}"
	assert_memsnapshot_tracing_metrics memburst integration 1
}

burst_daemon_resources() {
	local pid
	pid=$(< "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")
	local fds=(/proc/"${pid}"/fd/*)
	printf '%s ' "${#fds[@]}"
	awk '/^VmRSS:/ {rss=$2} /^Threads:/ {threads=$2} END {print rss, threads}' "/proc/${pid}/status"
}

assert_burst_resource_growth() {
	local fds rss threads
	read -r fds rss threads < <(burst_daemon_resources)
	log_info "daemon resources: fds=${fds} rss_kib=${rss} threads=${threads}"
	((fds <= burst_baseline_fds + 2)) || fatal "daemon FD growth exceeds 2"
	((rss <= burst_baseline_rss + 131072)) || fatal "daemon RSS growth exceeds 128 MiB"
	((threads <= burst_baseline_threads + 16)) || fatal "daemon thread growth exceeds 16"
}

assert_burst_storage_failure() {
	grep -F 'msg="save memburst snapshot"' "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" | grep -Fq 'not a directory' || fatal "storage failure diagnostic missing expected cause"
}

for memburst_runtime in go disabled churn python java c cpp; do
	case ${memburst_runtime} in
	python) command -v python3 > /dev/null || {
		log_info 'SKIP: Python runtime unavailable'
		continue
	} ;;
	java) [[ -f ${MEMBURST_RUNTIME_DIR}/HeapFixture.class ]] || {
		log_info 'SKIP: Java runtime unavailable'
		continue
	} ;;
	c | cpp) [[ -x ${MEMBURST_RUNTIME_DIR}/${memburst_runtime}-snapshot ]] || {
		log_info "SKIP: ${memburst_runtime} compiler unavailable"
		continue
	} ;;
	esac
	HUATUO_BAMAI_TEST_TMPDIR="${MEMBURST_ROOT_WORKSPACE}/runtime-${memburst_runtime}"
	mkdir -p "${HUATUO_BAMAI_TEST_TMPDIR}"
	burst_start_runtime "${memburst_runtime}"
	burst_top=10
	burst_threshold=10
	burst_anon=0
	burst_blacklist=0
	burst_cooldown=1
	burst_input low
	burst_start
	if [[ ${memburst_runtime} == go ]]; then
		if [[ -d ${HUATUO_BAMAI_TEST_TMPDIR}/events ]]; then rmdir "${HUATUO_BAMAI_TEST_TMPDIR}/events"; fi
		: > "${HUATUO_BAMAI_TEST_TMPDIR}/events"
		burst_wait_samples low
		burst_input high
		wait_until 30 .1 grep -Fq 'msg="save memburst snapshot"' "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
			|| fatal "storage obstruction did not produce a diagnostic"
		assert_burst_storage_failure
		touch "${HUATUO_BAMAI_TEST_TMPDIR}/expected-storage-failure"
		rm "${HUATUO_BAMAI_TEST_TMPDIR}/events"
		mkdir "${HUATUO_BAMAI_TEST_TMPDIR}/events"
		burst_input low
	fi
	burst_rounds=1
	[[ ${memburst_runtime} != churn ]] || burst_rounds=12
	for ((burst_round = 1; burst_round <= burst_rounds; burst_round++)); do
		burst_wait_samples low
		burst_input high
		wait_until 30 .1 burst_expected_count "${burst_round}" || fatal "${memburst_runtime}: capture ${burst_round} missing"
		assert_burst_runtime "${memburst_runtime}" "${burst_round}"
		if [[ ${memburst_runtime} == churn ]]; then
			if ((burst_round == 1)); then
				read -r burst_baseline_fds burst_baseline_rss burst_baseline_threads < <(burst_daemon_resources)
			else
				assert_burst_resource_growth
			fi
		fi
		burst_input low
	done
	# A dead target must not poison later captures in the same daemon.
	cp "${HUATUO_BAMAI_TEST_TMPDIR}/events/memburst" "${HUATUO_BAMAI_TEST_TMPDIR}/before-target-exit"
	kill -KILL "${burst_runtime_pid}"
	wait "${burst_runtime_pid}" 2> /dev/null || true
	burst_workloads=()
	mv "${HUATUO_BAMAI_TEST_TMPDIR}/commands" "${HUATUO_BAMAI_TEST_TMPDIR}/old-commands"
	burst_start_runtime go
	burst_wait_samples low
	burst_input high
	wait_until 30 .1 burst_expected_count "$((burst_rounds + 1))" || fatal "capture did not recover after ${memburst_runtime} exit"
	assert_burst_runtime go "$((burst_rounds + 1))"
	assert_memsnapshot_event_prefix "${HUATUO_BAMAI_TEST_TMPDIR}/before-target-exit" \
		"${HUATUO_BAMAI_TEST_TMPDIR}/events/memburst" 'target exit changed completed events'
	burst_stop TERM
	kill -KILL "${burst_runtime_pid}"
	wait "${burst_runtime_pid}" 2> /dev/null || true
	burst_workloads=()
	log_info "PASS: standard daemon ${memburst_runtime} capture and target-exit recovery"
done

# Long argv[0] values exercise the real ranking serialization, not synthetic API data.
assert_burst_payload_budget() {
	jq -se '
 last.tracer_data |
 .snapshot_reason == "memburst payload truncated to fit event budget"
 and (.top_memory_usage | length > 0)
 and ([.top_memory_usage[] | select(.ProcessName | startswith("snapshot-budget"))] | length < 24)
 and (tojson | utf8bytelength) <= 2097152
 ' "${HUATUO_BAMAI_TEST_TMPDIR}/events/memburst" > /dev/null || fatal "oversized ranking was not bounded with a truncation reason"
}

HUATUO_BAMAI_TEST_TMPDIR="${MEMBURST_ROOT_WORKSPACE}/payload-budget"
mkdir -p "${HUATUO_BAMAI_TEST_TMPDIR}"
# The fixture waits for its first command; no system utility dispatch is involved.
cp "${MEMBURST_ACCEPTANCE_FIXTURE}" "${HUATUO_BAMAI_TEST_TMPDIR}/snapshot-budget"
mkfifo "${HUATUO_BAMAI_TEST_TMPDIR}/budget-commands"
exec {burst_budget_fd}<> "${HUATUO_BAMAI_TEST_TMPDIR}/budget-commands"
burst_fds+=("${burst_budget_fd}")
printf -v burst_long_name '%100000s' ''
burst_long_name="snapshot-budget${burst_long_name// /x}"
for ((burst_index = 0; burst_index < 24; burst_index++)); do
	(exec -a "${burst_long_name}" "${HUATUO_BAMAI_TEST_TMPDIR}/snapshot-budget" 0 < "${HUATUO_BAMAI_TEST_TMPDIR}/budget-commands") &
	burst_workloads+=("$!")
done
for burst_pid in "${burst_workloads[@]}"; do
	wait_until 5 .1 grep -qx snapshot-budget "/proc/${burst_pid}/comm" || fatal "long-name workload not ready"
done
burst_top=1000
burst_threshold=10
burst_anon=0
burst_blacklist=0
burst_cooldown=1
burst_input low
burst_start
burst_wait_samples low
burst_input high
wait_until 30 .1 burst_expected_count 1 || fatal "oversized-ranking event missing"
assert_burst_payload_budget
burst_stop TERM
for burst_pid in "${burst_workloads[@]}"; do
	kill -TERM "${burst_pid}"
	wait "${burst_pid}" 2> /dev/null || true
done
burst_workloads=()
log_info "PASS: real oversized ranking is truncated to the 2 MiB event budget"

# Exercise the real detector without substituting meminfo or risking host OOM.
HUATUO_BAMAI_BIN=${MEMBURST_STANDARD_BIN}
while ((burst_mounted > 0)); do
	umount /proc/meminfo
	burst_mounted=$((burst_mounted - 1))
done
HUATUO_BAMAI_TEST_TMPDIR="${MEMBURST_ROOT_WORKSPACE}/real-pressure"
mkdir -p "${HUATUO_BAMAI_TEST_TMPDIR}"
burst_top=10
burst_threshold=1
burst_anon=0
burst_blacklist=0
available_kb=$(awk '/^MemAvailable:/ {print $2}' /proc/meminfo)
((available_kb > 1048576)) || fatal "real pressure scenario requires at least 1 GiB available"
# A background burst during baseline must not suppress this smoke scenario.
# Dedicated controlled-input cases above verify the longer cooldown separately.
burst_cooldown=1
pressure_fds=()
for i in 4 5 6 7 8 9; do
	mkfifo "${MEMBURST_ROOT_WORKSPACE}/commands-${i}"
	exec {fd}<> "${MEMBURST_ROOT_WORKSPACE}/commands-${i}"
	burst_fds+=("${fd}")
	"${MEMBURST_ACCEPTANCE_FIXTURE}" 1 < "${MEMBURST_ROOT_WORKSPACE}/commands-${i}" > "${MEMBURST_ROOT_WORKSPACE}/workload-${i}.log" 2>&1 &
	burst_workloads+=("$!")
	printf a >&"${fd}"
	wait_until 20 .1 grep -qx ready "${MEMBURST_ROOT_WORKSPACE}/workload-${i}.log" || fatal "real workload not ready"
	pressure_fds+=("${fd}")
done
burst_start
memsnapshot_wait_memburst_samples 0 2
real_before=$(burst_count)
real_triggered_at=$(date -u '+%Y-%m-%dT%H:%M:%S.%NZ')
# Synchronize allocation instead of spreading process startup across samples.
for fd in "${pressure_fds[@]}"; do printf p >&"${fd}"; done
for i in 4 5 6 7 8 9; do
	wait_until 20 .1 grep -qx 'pressure ready' "${MEMBURST_ROOT_WORKSPACE}/workload-${i}.log" || fatal "real memory allocation failed"
done
real_pressure_event() {
	(($(burst_count) > real_before)) && jq -se --arg at "${real_triggered_at}" \
		'last.started_timestamp >= $at' "${HUATUO_BAMAI_TEST_TMPDIR}/events/memburst" > /dev/null
}
wait_until 30 .1 real_pressure_event || fatal "real anonymous memory burst did not trigger"
assert_burst_event
burst_stop TERM
log_info "PASS: memburst real anonymous memory pressure"
