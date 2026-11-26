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

# Shared controlled-input memburst scenarios; service lifecycle stays in lib.sh.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_memsnapshot.sh"
source "${ROOT_DIR}/integration/config.sh"

burst_setup() {
	readonly MEMBURST_ROOT_WORKSPACE=${HUATUO_BAMAI_TEST_TMPDIR}
	readonly MEMBURST_RUNTIME_DIR="${MEMBURST_ROOT_WORKSPACE}/runtimes"
	export MEMSNAP_ACCEPTANCE_ARTIFACTS="${MEMBURST_ROOT_WORKSPACE}/helpers"
	export MEMBURST_ACCEPTANCE_FIXTURE="${MEMSNAP_ACCEPTANCE_ARTIFACTS}/go-snapshot"
	mkdir -p "${MEMSNAP_ACCEPTANCE_ARTIFACTS}"
	cd "${ROOT_DIR}"
	# Only the detector input is replaced; procfs and runtime memory remain real.
	local level value
	for level in low high; do
		value=100000
		[[ ${level} != high ]] || value=400000
		printf 'MemTotal: 1000000 kB\nMemFree: 500000 kB\nMemAvailable: 500000 kB\nActive(anon): %s kB\nInactive(anon): 10000 kB\n' "${value}" > "${MEMBURST_ROOT_WORKSPACE}/meminfo-${level}"
	done
	burst_mounted=0
	burst_workloads=()
	burst_fds=()
	GO_SNAPSHOT_KUBELET_PORT=10250
	go_snapshot_top_k=100
	KUBELET_CERT=${KUBELET_CERT:-unused}
	KUBELET_KEY=${KUBELET_KEY:-unused}
	burst_top=10
	burst_threshold=10
	burst_anon=0
	burst_blacklist=0
	burst_cooldown=30
	burst_window=2
}

burst_cleanup() {
	local status=$? pid fd
	huatuo_bamai_stop || status=1
	for pid in "${burst_workloads[@]}"; do
		kill -KILL "${pid}" 2> /dev/null || true
		wait "${pid}" 2> /dev/null || true
	done
	burst_workloads=()
	for fd in "${burst_fds[@]}"; do exec {fd}>&-; done
	burst_fds=()
	while ((burst_mounted > 0)); do
		umount /proc/meminfo || {
			status=1
			break
		}
		burst_mounted=$((burst_mounted - 1))
	done
	return "${status}"
}

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

burst_start_runtime() {
	local mode=$1
	local executable=${2:-${MEMBURST_ACCEPTANCE_FIXTURE}}
	mkfifo "${HUATUO_BAMAI_TEST_TMPDIR}/commands"
	exec {burst_runtime_fd}<> "${HUATUO_BAMAI_TEST_TMPDIR}/commands"
	burst_fds+=("${burst_runtime_fd}")
	case ${mode} in
	go) burst_command=("${executable}" 1) ;;
	disabled) burst_command=("${MEMBURST_ACCEPTANCE_FIXTURE}" 0) ;;
	churn) burst_command=(env MEMSNAPSHOT_CHURN=1 "${MEMBURST_ACCEPTANCE_FIXTURE}" 1) ;;
	python)
		burst_command=(python3 "${MEMBURST_RUNTIME_DIR}/snapshot.py")
		if (($# > 1)); then
			burst_command=(env "PYTHONHOME=${memburst_python_home}"
				"LD_LIBRARY_PATH=${executable%/*}/lib${LD_LIBRARY_PATH:+:${LD_LIBRARY_PATH}}"
				"${executable}" "${MEMBURST_RUNTIME_DIR}/snapshot.py")
		fi
		;;
	java) burst_command=("${2:-java}" -XX:+UseG1GC -Xms32m -Xmx256m -cp "${MEMBURST_RUNTIME_DIR}" HeapFixture) ;;
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

burst_build_fixture() {
	if [[ -n ${MEMBURST_ACCEPTANCE_PREBUILT_DIR:-} ]]; then
		cp "${MEMBURST_ACCEPTANCE_PREBUILT_DIR}/go-snapshot" "${MEMBURST_ACCEPTANCE_FIXTURE}"
	fi
	if [[ ! -x ${MEMBURST_ACCEPTANCE_FIXTURE} ]]; then
		go build -mod=vendor -o "${MEMBURST_ACCEPTANCE_FIXTURE}" ./integration/testdata/test_memory_threshold_snapshot_golang.go
	fi
}

burst_prepare_ranked_workloads() {
	local i fd
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

burst_daemon_resources() {
	local pid fd target
	pid=$(< "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")
	local fds=(/proc/"${pid}"/fd/*)
	for fd in "${fds[@]}"; do
		if target=$(readlink "${fd}"); then
			printf '%s -> %q\n' "${fd##*/}" "${target}"
		fi
	done > "${HUATUO_BAMAI_TEST_TMPDIR}/daemon-fds-${1}.log"
	printf '%s ' "${#fds[@]}"
	awk '/^VmRSS:/ {rss=$2} /^Threads:/ {threads=$2} END {print rss, threads}' "/proc/${pid}/status"
}

assert_burst_resource_growth() {
	local fds rss threads
	read -r fds rss threads < <(burst_daemon_resources current) || fatal "cannot read daemon resources"
	log_info "daemon resources: fds=${fds} baseline_fds=${burst_baseline_fds} rss_kib=${rss} baseline_rss_kib=${burst_baseline_rss} threads=${threads} baseline_threads=${burst_baseline_threads}"
	if ((fds > burst_baseline_fds + 2)); then
		cat "${HUATUO_BAMAI_TEST_TMPDIR}/daemon-fds-baseline.log" "${HUATUO_BAMAI_TEST_TMPDIR}/daemon-fds-current.log"
		fatal "daemon FD growth exceeds 2: baseline=${burst_baseline_fds}, current=${fds}"
	fi
	((rss <= burst_baseline_rss + 131072)) || fatal "daemon RSS growth exceeds 128 MiB: baseline=${burst_baseline_rss} KiB, current=${rss} KiB"
	((threads <= burst_baseline_threads + 16)) || fatal "daemon thread growth exceeds 16: baseline=${burst_baseline_threads}, current=${threads}"
}

burst_wait_groups() {
	local pid code status=0
	for pid in "${!burst_group_names[@]}"; do
		if wait "${pid}"; then
			continue
		else
			code=$?
			log_error "memburst group ${burst_group_names[${pid}]} (pid ${pid}) exited with status ${code}"
			status=1
		fi
	done
	return "${status}"
}
