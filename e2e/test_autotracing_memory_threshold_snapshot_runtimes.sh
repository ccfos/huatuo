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

# Verify Go, Python, Java, C and C++ snapshots through real Kubernetes pressure.
set -euo pipefail
source "${ROOT_DIR}/e2e/lib_memory_threshold_snapshot.sh"
memory_threshold_snapshot_setup

snapshot_runtime_dependencies_available() {
	local mode=$1 command
	local -a commands=()
	case ${mode} in
	python) commands=(python3) ;;
	java) commands=(java javac) ;;
	c) commands=(gcc) ;;
	cpp) commands=(g++) ;;
	esac
	for command in "${commands[@]}"; do
		if ! command -v "${command}" > /dev/null 2>&1; then
			log_info "SKIP: ${mode} snapshot requires ${command}"
			return 1
		fi
	done
}

snapshot_run_case() (
	local mode=$1 rate=1 usage_file cgroup_root triggered_at pod_name high_before
	go_snapshot_stage="${mode}: create Pod and discover container"
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
	trap 'memory_threshold_snapshot_cleanup || exit $?' EXIT
	mkdir -p "${go_snapshot_case_dir}"

	snapshot_require_pod_image
	snapshot_create_pod
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
		snapshot_set_memory_high $((72 * 1024 * 1024))
	fi

	go_snapshot_stage="${mode}: start daemon and prepare workload"
	# Set a finite limit before discovery; unavailable watches are not retried.
	# A fresh daemon also isolates the node-wide cooldown between cases.
	integration_huatuo_bamai_start write_memory_threshold_snapshot_config --region e2e --log-debug
	assert_memsnapshot_tracing_metrics memory_threshold_snapshot e2e 1
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
	go_snapshot_stage="${mode}: register cgroup watch"
	wait_until 30 0.1 go_snapshot_watch_is_ready || fatal "${mode}: memory cgroup watch was not registered"
	if [[ -e "${go_snapshot_cgroup}/memory.max" ]]; then
		high_before=$(awk '$1 == "high" { print $2 }' "${go_snapshot_cgroup}/memory.events")
	fi

	go_snapshot_stage="${mode}: trigger memory pressure"
	triggered_at=$(date -u '+%Y-%m-%dT%H:%M:%S.%NZ')
	printf p >&"${go_snapshot_input_fd}"
	if [[ -e "${go_snapshot_cgroup}/memory.max" ]]; then
		wait_until 15 0.1 awk -v before="${high_before}" '$1 == "high" { high = $2 } END { exit !(high > before) }' \
			"${go_snapshot_cgroup}/memory.events" || fatal "${mode}: no kernel memory.high event"
		# Release reclaim throttling after notification so the workload can finish.
		snapshot_set_memory_high max
	fi
	go_snapshot_stage="${mode}: wait for persisted snapshot"
	wait_until 30 0.1 go_snapshot_read_event || fatal "${mode}: daemon did not persist a memory snapshot"
	wait_until 30 0.2 grep -qx 'pressure ready' "${go_snapshot_case_dir}/workload.log" || fatal "${mode}: pressure allocation did not finish"
	assert_memsnapshot_tracing_metrics memory_threshold_snapshot e2e 1
	huatuo_bamai_stop

	go_snapshot_stage="${mode}: validate snapshot payload and logs"
	assert_snapshot_event "${mode}" "${triggered_at}"
	assert_snapshot_logs
	printf q >&"${go_snapshot_input_fd}"
	wait "${go_snapshot_pid}" || fatal "${mode}: Go workload exited unsuccessfully"
	go_snapshot_pid=""
	log_info "${mode}: container memory pressure produced the expected runtime snapshot"
	jq '.tracer_data.snapshot | .entries = [.entries[]? | select(.name == "main.allocateBlock")]' \
		"${go_snapshot_case_dir}/snapshot.json"
)

go build -mod=vendor -o "${GO_SNAPSHOT_BIN}" "${ROOT_DIR}/integration/testdata/test_memory_threshold_snapshot_golang.go"
# The runner's baseline daemon serves the other e2e cases. This case needs
# isolated configuration and a fresh node-wide cooldown for every scenario.
huatuo_bamai_stop
huatuo_bamai_log_check || fatal "baseline daemon reported an error before the snapshot scenario"
for snapshot_mode in aggregation topk disabled; do
	snapshot_run_case "${snapshot_mode}"
done
MEMSNAP_OPTIONAL_RUNTIMES=1 snapshot_build_runtime_fixtures
for snapshot_mode in python java c cpp; do
	if snapshot_runtime_dependencies_available "${snapshot_mode}"; then
		snapshot_run_case "${snapshot_mode}"
	fi
done
