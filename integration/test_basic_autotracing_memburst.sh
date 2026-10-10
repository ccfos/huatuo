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
require_commands go jq mount umount awk unshare
[[ ${EUID} == 0 ]] || skip "requires root and a private mount namespace"
# Never replace procfs in the host mount namespace when invoked manually.
if [[ ${MEMBURST_PRIVATE_PIDNS:-0} != 1 ]]; then
	[[ $(readlink /proc/self/ns/mnt) != $(readlink /proc/1/ns/mnt) ]] || fatal "run through integration/run.sh (private mount namespace required)"
	# Host scenarios need controlled rankings, including the active-read target.
	# Docker scenarios inspect host PIDs and therefore retain the runner PID view.
	if [[ ${MEMBURST_CONTAINER_ONLY:-0} != 1 ]]; then
		export MEMBURST_PRIVATE_PIDNS=1
		exec unshare --pid --fork --mount-proc bash "${BASH_SOURCE[0]}"
	fi
fi
root_workspace=${HUATUO_BAMAI_TEST_TMPDIR}
export MEMSNAP_ACCEPTANCE_ARTIFACTS="${root_workspace}/helpers"
export MEMBURST_ACCEPTANCE_FIXTURE="${MEMSNAP_ACCEPTANCE_ARTIFACTS}/go-snapshot"
mkdir -p "${MEMSNAP_ACCEPTANCE_ARTIFACTS}"
cd "${ROOT_DIR}"
if [[ -n ${MEMBURST_ACCEPTANCE_PREBUILT_DIR:-} ]]; then
	cp "${MEMBURST_ACCEPTANCE_PREBUILT_DIR}/go-snapshot" "${MEMBURST_ACCEPTANCE_FIXTURE}"
	cp "${MEMBURST_ACCEPTANCE_PREBUILT_DIR}/acceptance.test" "${MEMSNAP_ACCEPTANCE_ARTIFACTS}/acceptance.test"
	cp "${MEMBURST_ACCEPTANCE_PREBUILT_DIR}/huatuo-bamai" "${MEMSNAP_ACCEPTANCE_ARTIFACTS}/huatuo-bamai"
fi
if [[ ! -x ${MEMBURST_ACCEPTANCE_FIXTURE} ]]; then
	go build -mod=vendor -o "${MEMBURST_ACCEPTANCE_FIXTURE}" ./e2e/testdata/memory_threshold_snapshot_golang.go
fi
snapshot_build_read_barrier
export MEMSNAP_READ_BARRIER_ENABLED=1
overlay="${MEMSNAP_ACCEPTANCE_ARTIFACTS}/overlay.json"
jq -n --arg virtual "${ROOT_DIR}/core/autotracing/memburst_acceptance_integration_test.go" \
	--arg source "${ROOT_DIR}/integration/testdata/test_basic_autotracing_memburst_linux_test.go" \
	--arg syscall "${ROOT_DIR}/vendor/golang.org/x/sys/unix/zsyscall_linux.go" --arg read_source "${read_source}" \
	'{Replace:{($virtual):$source,($syscall):$read_source}}' > "${overlay}"
if [[ ! -x ${MEMSNAP_ACCEPTANCE_ARTIFACTS}/acceptance.test ]]; then
	go test -race -mod=vendor -overlay="${overlay}" -tags=integration -c -o "${MEMSNAP_ACCEPTANCE_ARTIFACTS}/acceptance.test" ./core/autotracing
fi
# Instrument remote-read timing only in the acceptance binary, never production.
if [[ ! -x ${MEMSNAP_ACCEPTANCE_ARTIFACTS}/huatuo-bamai ]]; then
	go build -mod=vendor -overlay="${overlay}" -tags 'netgo osusergo' -o "${MEMSNAP_ACCEPTANCE_ARTIFACTS}/huatuo-bamai" ./cmd/huatuo-bamai
fi
HUATUO_BAMAI_BIN="${MEMSNAP_ACCEPTANCE_ARTIFACTS}/huatuo-bamai"
# Reuse the runtime workloads already maintained by the snapshot E2E suite.
export MEMBURST_RUNTIME_FIXTURES="${root_workspace}/runtimes"
mkdir -p "${MEMBURST_RUNTIME_FIXTURES}"
if [[ ${MEMBURST_CONTAINER_ONLY:-0} != 1 ]]; then
	MEMSNAP_OPTIONAL_RUNTIMES=1 HUATUO_BAMAI_TEST_TMPDIR="${MEMBURST_RUNTIME_FIXTURES}" snapshot_build_runtime_fixtures
fi
"${MEMSNAP_ACCEPTANCE_ARTIFACTS}/acceptance.test" -test.run "${MEMBURST_ACCEPTANCE_TEST_PATTERN:-^TestMemburstAcceptance}" -test.v -test.timeout=3m > "${MEMSNAP_ACCEPTANCE_ARTIFACTS}/results.log" 2>&1 || {
	cat "${MEMSNAP_ACCEPTANCE_ARTIFACTS}/results.log"
	fatal "memburst helper integration failed"
}
cat "${MEMSNAP_ACCEPTANCE_ARTIFACTS}/results.log"
if [[ ${MEMBURST_CONTAINER_ONLY:-0} == 1 ]]; then
	log_info "PASS: memburst container capture and failure isolation"
	exit 0
fi

# Test only /proc/meminfo is replaced. RSS rankings and remote memory are real.
meminfo_low="${root_workspace}/meminfo-low"
meminfo_high="${root_workspace}/meminfo-high"
for level in low high; do
	value=100000
	[[ ${level} != high ]] || value=400000
	printf 'MemTotal: 1000000 kB\nMemFree: 500000 kB\nMemAvailable: 500000 kB\nActive(anon): %s kB\nInactive(anon): 10000 kB\n' "${value}" > "${root_workspace}/meminfo-${level}"
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
	mount --bind "${root_workspace}/meminfo-$1" /proc/meminfo
	burst_mounted=$((burst_mounted + 1))
}

burst_count() {
	if [[ ! -s ${HUATUO_BAMAI_TEST_TMPDIR}/events/memburst ]]; then
		echo 0
		return
	fi
	jq -s length "${HUATUO_BAMAI_TEST_TMPDIR}/events/memburst" 2> /dev/null
}
burst_expected_count() { [[ $(burst_count) == "$1" ]]; }
burst_logs() {
	if grep -qiE 'level="?(warn|warning|error|panic|fatal)"?|"level":"(warn|warning|error|panic|fatal)"|panic:' "$1"; then
		cat "$1" >&2
		fatal "unexpected warning/error in $1"
	fi
}
GO_SNAPSHOT_BAMAI_PORT=$(allocate_available_port)
GO_SNAPSHOT_KUBELET_PORT=10250
go_snapshot_top_k=100
KUBELET_CERT=${KUBELET_CERT:-unused}
KUBELET_KEY=${KUBELET_KEY:-unused}
HUATUO_BAMAI_ADDR="http://127.0.0.1:${GO_SNAPSHOT_BAMAI_PORT}"
HUATUO_BAMAI_METRICS_API="${HUATUO_BAMAI_ADDR}/metrics"
burst_top=3
burst_threshold=10
burst_anon=0
burst_blacklist=0
burst_write_config() {
	go_snapshot_case_dir=${HUATUO_BAMAI_TEST_TMPDIR}
	write_memory_threshold_snapshot_config
	sed -i 's/"memburst", //; s/, "tracing_status"//; s/^BlackList = \[/BlackList = ["memory_threshold_snapshot", /' "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf"
	if [[ ${burst_blacklist} == 1 ]]; then sed -i 's/^BlackList = \[/BlackList = ["memburst", /' "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf"; fi
	cat >> "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" << CONFIG
[AutoTracing.MemoryBurst]
 DeltaMemoryBurst = ${burst_threshold}
 DeltaAnonThreshold = ${burst_anon}
 Interval = 1
 SlidingWindowLength = 2
 IntervalTracing = 10
 DumpProcessMaxNum = 10
 SnapshotProcessMaxNum = ${burst_top}
CONFIG
}
burst_start() {
	local pid
	for pid in "${burst_workloads[@]}"; do kill -0 "${pid}" || fatal "workload exited before scenario"; done
	integration_huatuo_bamai_start burst_write_config --region integration --disable-kubelet --log-debug
	local enabled=1
	[[ ${burst_blacklist} == 0 ]] || enabled=0
	memsnapshot_assert_tracing_metrics memburst integration "${enabled}"
}
burst_stop() {
	local signal=$1 pid status=0 state i
	pid=$(< "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")
	[[ ${2:-} == sent ]] || kill -"${signal}" "${pid}"
	for ((i = 0; i < 650; i++)); do
		state=$(awk '{print $3}' "/proc/${pid}/stat" 2> /dev/null) || break
		[[ ${state} == Z ]] && break
		sleep .1
	done
	((i < 650)) || fatal "daemon shutdown exceeded 65 seconds"
	wait "${pid}" || status=$?
	if [[ ${signal} == TERM ]]; then [[ ${status} == 0 ]] || fatal "TERM exit status ${status}"; else [[ ${status} == 137 ]] || fatal "KILL exit status ${status}"; fi
	rm "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid"
	burst_logs "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log"
	mv "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${HUATUO_BAMAI_TEST_TMPDIR}/daemon-${signal}-${SECONDS}.log"
}
burst_assert_event() {
	memsnapshot_assert_tracing_metrics memburst integration 1
	jq -s -e --argjson n "${burst_top}" --argjson fixtures "$(printf '%s\n' "${burst_workloads[@]}" | jq -s .)" '
 last as $e | $e.tracer_data as $d |
 $e.tracer_name == "memburst" and $e.tracer_type == "autotracing"
 and $e.region == "integration" and ($e.hostname|length>0)
 and (($e.container_id // "") == "")
 and ($e.started_timestamp|type=="string") and $e.uploaded_timestamp >= $e.started_timestamp
 and ($d.top_memory_usage|length>0 and length<=10)
 and ([$d.top_memory_usage[].MemSize] == ([$d.top_memory_usage[].MemSize]|sort|reverse))
 and ($d.process_snapshots|length==([$n, ($d.top_memory_usage|length)]|min))
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
# Keep three profiled processes above the daemon in real RSS rankings.
for i in 1 2 3; do
	mkfifo "${root_workspace}/commands-${i}"
	exec {fd}<> "${root_workspace}/commands-${i}"
	burst_fds+=("${fd}")
	"${MEMBURST_ACCEPTANCE_FIXTURE}" 1 < "${root_workspace}/commands-${i}" > "${root_workspace}/workload-${i}.log" 2>&1 &
	burst_workloads+=("$!")
	printf a >&"${fd}"
	wait_until 20 .1 grep -qx ready "${root_workspace}/workload-${i}.log" || fatal "workload not ready"
	printf p >&"${fd}"
	wait_until 20 .1 grep -qx 'pressure ready' "${root_workspace}/workload-${i}.log" || fatal "real allocation pressure failed"
done
huatuo_bamai_stop
for scenario in normal threshold anon_threshold blacklist top1; do
	HUATUO_BAMAI_TEST_TMPDIR="${root_workspace}/${scenario}"
	mkdir -p "${HUATUO_BAMAI_TEST_TMPDIR}"
	burst_top=3
	burst_threshold=10
	burst_anon=0
	burst_blacklist=0
	[[ ${scenario} != top1 ]] || burst_top=1
	[[ ${scenario} != threshold ]] || burst_threshold=1000
	[[ ${scenario} != anon_threshold ]] || burst_anon=70
	[[ ${scenario} != blacklist ]] || burst_blacklist=1
	burst_input low
	burst_start
	sleep 2
	[[ $(burst_count) == 0 ]] || fatal "event without burst"
	burst_input high
	if [[ ${scenario} == threshold || ${scenario} == anon_threshold || ${scenario} == blacklist ]]; then
		sleep 3
		huatuo_bamai_ready || fatal "daemon unavailable during suppression assertion"
		[[ $(burst_count) == 0 ]] || fatal "suppressed burst produced event"
		local_enabled=1
		[[ ${burst_blacklist} == 0 ]] || local_enabled=0
		memsnapshot_assert_tracing_metrics memburst integration "${local_enabled}"
	else
		wait_until 20 .1 burst_expected_count 1 || fatal "burst did not persist"
		burst_assert_event
		if [[ ${scenario} == normal ]]; then
			first_at=${SECONDS}
			burst_input low
			sleep 2
			burst_input high
			sleep 2
			((SECONDS - first_at < 10)) || fatal "cooldown assertion exceeded budget"
			[[ $(burst_count) == 1 ]] || fatal "cooldown did not suppress burst"
			burst_input low
			while ((SECONDS - first_at < 11)); do sleep .1; done
			burst_input high
			wait_until 20 .1 burst_expected_count 2 || fatal "capture did not recover after cooldown"
			burst_assert_event
			for signal in TERM KILL; do
				cp "${HUATUO_BAMAI_TEST_TMPDIR}/events/memburst" "${HUATUO_BAMAI_TEST_TMPDIR}/before-${signal}"
				before=$(burst_count)
				burst_stop "${signal}"
				burst_input low
				burst_start
				sleep 2
				burst_input high
				wait_until 20 .1 burst_expected_count "$((before + 1))" || fatal "capture did not recover after ${signal}"
				head -c "$(wc -c < "${HUATUO_BAMAI_TEST_TMPDIR}/before-${signal}")" "${HUATUO_BAMAI_TEST_TMPDIR}/events/memburst" | cmp - "${HUATUO_BAMAI_TEST_TMPDIR}/before-${signal}" || fatal "previous events changed after ${signal}"
				burst_assert_event
			done
		fi
	fi
	burst_stop TERM
	log_info "PASS: memburst ${scenario}"
done
burst_logs "${MEMSNAP_ACCEPTANCE_ARTIFACTS}/results.log"
log_info "PASS: memburst controlled-input scenarios"

# Kill the daemon only after a successful read and entry into the next read.
for signal in TERM KILL; do
	HUATUO_BAMAI_TEST_TMPDIR="${root_workspace}/active-${signal}"
	mkdir -p "${HUATUO_BAMAI_TEST_TMPDIR}/barrier"
	export MEMSNAP_READ_BARRIER_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/barrier"
	export MEMSNAP_READ_BARRIER_PID="${burst_workloads[0]}"
	burst_top=3
	burst_threshold=10
	burst_anon=0
	burst_blacklist=0
	burst_input low
	burst_start
	sleep 2
	burst_input high
	wait_until 10 .01 test -f "${MEMSNAP_READ_BARRIER_DIR}/reached" || fatal "daemon capture did not reach remote-read barrier"
	if [[ ${signal} == TERM ]]; then
		kill -TERM "$(< "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")"
		wait_until 5 .01 grep -q 'received signal.*shutting down' "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" || fatal "daemon did not receive TERM"
		sleep .1
		touch "${MEMSNAP_READ_BARRIER_DIR}/release"
		burst_stop TERM sent
		[[ $(burst_count) == 1 ]] || fatal "TERM during capture lost basic event"
		jq -se 'last.tracer_data | (.top_memory_usage|length>0 and length<=10) and .snapshot_reason=="snapshot canceled: context canceled"' "${HUATUO_BAMAI_TEST_TMPDIR}/events/memburst" > /dev/null || fatal "TERM did not persist canceled basic event"
	else
		burst_stop KILL
		[[ $(burst_count) == 0 ]] || fatal "KILL unexpectedly persisted in-flight capture"
	fi
	unset MEMSNAP_READ_BARRIER_DIR MEMSNAP_READ_BARRIER_PID
	before=$(burst_count)
	burst_input low
	burst_start
	sleep 2
	burst_input high
	wait_until 20 .1 burst_expected_count "$((before + 1))" || fatal "daemon did not recover after active ${signal}"
	burst_assert_event
	burst_stop TERM
	log_info "PASS: memburst daemon ${signal} during remote read and restart"
done

# Exercise the real detector without substituting meminfo or risking host OOM.
while ((burst_mounted > 0)); do
	umount /proc/meminfo
	burst_mounted=$((burst_mounted - 1))
done
HUATUO_BAMAI_TEST_TMPDIR="${root_workspace}/real-pressure"
mkdir -p "${HUATUO_BAMAI_TEST_TMPDIR}"
burst_top=3
burst_threshold=1
burst_anon=0
burst_blacklist=0
available_kb=$(awk '/^MemAvailable:/ {print $2}' /proc/meminfo)
((available_kb > 1048576)) || fatal "real pressure scenario requires at least 1 GiB available"
burst_start
sleep 3
real_before=$(burst_count)
for i in 4 5 6 7 8 9; do
	mkfifo "${root_workspace}/commands-${i}"
	exec {fd}<> "${root_workspace}/commands-${i}"
	burst_fds+=("${fd}")
	"${MEMBURST_ACCEPTANCE_FIXTURE}" 1 < "${root_workspace}/commands-${i}" > "${root_workspace}/workload-${i}.log" 2>&1 &
	burst_workloads+=("$!")
	printf a >&"${fd}"
	wait_until 20 .1 grep -qx ready "${root_workspace}/workload-${i}.log" || fatal "real workload not ready"
	printf p >&"${fd}"
	wait_until 20 .1 grep -qx 'pressure ready' "${root_workspace}/workload-${i}.log" || fatal "real memory allocation failed"
done
real_pressure_event() { (($(burst_count) > real_before)); }
wait_until 30 .1 real_pressure_event || fatal "real anonymous memory burst did not trigger"
burst_assert_event
burst_stop TERM
log_info "PASS: memburst real anonymous memory pressure"
