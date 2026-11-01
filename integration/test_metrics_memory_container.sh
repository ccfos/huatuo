#!/usr/bin/env bash

# Copyright 2026 The HuaTuo Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Real Pod discovery, real v2 events, and deterministic cgroup metric inputs.
set -euo pipefail
# Mount fixtures must remain private even with the serial integration runner.
if [[ ${MEMORY_METRICS_MOUNT_NS:-0} != 1 ]]; then
	exec unshare --mount env MEMORY_METRICS_MOUNT_NS=1 bash "$0"
fi
mount --make-rprivate /
source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"
source "${ROOT_DIR}/integration/lib_memory_metrics.sh"
source "${ROOT_DIR}/integration/lib_cgroup.sh"
source "${ROOT_DIR}/e2e/lib.sh"
require_commands kubectl jq findmnt timeout awk sort diff cp mount umount mkfifo setsid
require_readable "${KUBELET_CERT}" "${KUBELET_KEY}"
WAIT_HUATUO_BAMAI_TIMEOUT=30

pod_name="memory-metrics-${BASHPID}-${RANDOM}"
pod_created=0
memory_mount=""
memory_high_set=0
expected_samples="${HUATUO_BAMAI_TEST_TMPDIR}/memory-samples.txt"
config_case=all
cleanup() {
	local status=$?
	trap - EXIT
	huatuo_bamai_stop || status=1
	if [[ ${memory_high_set} == 1 ]]; then set_container_memory_high max || status=1; fi
	cleanup_piped_processes
	if [[ -n ${memory_mount} ]]; then umount "${memory_mount}" || status=1; fi
	if [[ ${pod_created} == 1 ]]; then k8s_delete_pod "${BUSINESS_POD_NS}" "app=${pod_name}" || status=1; fi
	exit "${status}"
}
trap cleanup EXIT

container_metadata_ready() {
	curl -sf --connect-timeout 2 --max-time 3 -H 'Authorization: Bearer integration-node-token' \
		"${HUATUO_BAMAI_ADDR}/v1/containers/${container_id}" > "${HUATUO_BAMAI_TEST_TMPDIR}/container.json" \
		&& jq -e --arg id "${container_id}" '.data.id == $id' \
			"${HUATUO_BAMAI_TEST_TMPDIR}/container.json" > /dev/null
}

start_container_memory_agent() {
	vmstat_include=""
	vmstat_exclude=""
	events_include=""
	events_exclude=""
	case ${config_case} in
	include)
		vmstat_include='^(pgfault|pgmajfault)$'
		vmstat_exclude='^pgmajfault$'
		events_include='^(high|oom)$'
		events_exclude='^oom$'
		;;
	exclude)
		vmstat_exclude='^pgfault$'
		events_exclude='^high$'
		;;
	esac
	integration_huatuo_bamai_start write_config_container_memory_metrics --region dev --disable-storage --log-debug
	wait_until 20 0.1 container_metadata_ready || fatal "agent did not discover normal Pod ${pod_name} (${container_id})"
}

stop_container_memory_agent() {
	huatuo_bamai_stop
	assert_huatuo_bamai_log_clean
	cp "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${HUATUO_BAMAI_TEST_TMPDIR}/agent-${config_case}.log"
}

# Expected values are supplied by the scenario, never extracted from /metrics.
assert_container_memory_values() {
	local collector=$1 active=${2:-1} field value
	while read -r field value; do
		printf 'huatuo_bamai_%s_container_%s{%s} %s\n' "${collector}" "${field}" "${container_labels}" "${value}"
	done > "${expected_samples}"
	assert_memory_metric_samples "huatuo_bamai_${collector}_container_" "${expected_samples}" "${pod_name}" "${active}"
}

set_container_memory_high() {
	local value=$1 property=$1 unit=${container_cgroup##*/}
	if [[ ${unit} == *.scope ]]; then
		require_commands systemctl
		[[ ${value} != max ]] || property=infinity
		timeout --kill-after=1 10 systemctl set-property --runtime "${unit}" "MemoryHigh=${property}"
	else
		echo "${value}" > "${container_cgroup}/memory.high"
	fi
	[[ $(< "${container_cgroup}/memory.high") == "${value}" ]] \
		|| fatal "expected memory.high=${value}; actual $(< "${container_cgroup}/memory.high")"
}

assert_real_memory_events() {
	local baseline
	if [[ ! -e ${container_cgroup}/memory.events ]]; then
		assert_container_memory_values memory_events < /dev/null
		log_info "standard v1: missing memory.events exports no samples"
		return
	fi
	if [[ -e ${container_cgroup}/memory.high ]]; then
		compile_user_fixture "${ROOT_DIR}/integration/testdata/test_memsnapshot.c" "${HUATUO_BAMAI_TEST_TMPDIR}/memory-workload"
		start_piped_process "${HUATUO_BAMAI_TEST_TMPDIR}/workload" timeout --kill-after=1 30 bash -c '
   echo "${BASHPID}" > "$1/cgroup.procs"
   exec "$2"
  ' _ "${container_cgroup}" "${HUATUO_BAMAI_TEST_TMPDIR}/memory-workload"
		local workload_pid=${PROCESS_PID} input_fd=${PROCESS_INPUT_FD} output_fd=${PROCESS_OUTPUT_FD}
		printf a >&"${input_fd}"
		assert_process_message "${output_fd}" "${HUATUO_BAMAI_TEST_TMPDIR}/workload.log" ready
		baseline=$(awk '$1 == "high" {print $2}' "${container_cgroup}/memory.events")
		set_container_memory_high $((16 * 1024 * 1024))
		memory_high_set=1
		printf p >&"${input_fd}"
		wait_until 15 0.05 awk -v baseline="${baseline}" '$1 == "high" {value=$2} END {exit !(value>baseline)}' \
			"${container_cgroup}/memory.events" || fatal "memory.high counter did not increase from ${baseline}"
		set_container_memory_high max
		memory_high_set=0
		assert_process_message "${output_fd}" "${HUATUO_BAMAI_TEST_TMPDIR}/workload.log" 'pressure ready'
		printf q >&"${input_fd}"
		exec {input_fd}>&-
		wait "${workload_pid}" || fatal "memory pressure workload did not exit cleanly"
		exec {output_fd}<&-
	fi
	assert_container_memory_values memory_events < "${container_cgroup}/memory.events"
	log_info "real memory.events values match the Pod metrics"
}

kubelet_port=${KUBELET_PODS_API##*:}
kubelet_port=${kubelet_port%%/*}
timeout --kill-after=2 10 kubectl --request-timeout=8s get nodes > /dev/null || skip "Kubernetes node is unavailable"
pod_created=1
kubectl run "${pod_name}" --namespace "${BUSINESS_POD_NS}" --image="${BUSINESS_POD_IMAGE}" \
	--image-pull-policy=Never --restart=Never -l "app=${pod_name}" --dry-run=client -o json -- sleep infinity \
	| jq '.spec.terminationGracePeriodSeconds=1 | .spec.containers[0].resources={requests:{memory:"64Mi"},limits:{memory:"128Mi"}}' \
	| kubectl --request-timeout=10s create -f -
assert_kubelet_pod_count "${BUSINESS_POD_NS}" "^${pod_name}$" 1
container_id=$(kubelet_container_ids "${BUSINESS_POD_NS}" "^${pod_name}$")
cgroup_root=$(cgroup_memory_root) || fatal "memory controller mount is unavailable"
container_cgroup=$(cgroup_find_container "${cgroup_root}" "${container_id}") || fatal "Pod memory cgroup not found"
cgroup_relative=${container_cgroup#"${cgroup_root}"}
container_labels="container_host=\"${pod_name}\",container_hostnamespace=\"${BUSINESS_POD_NS}\",container_level=\"burstable\",container_name=\"${pod_name}\",container_type=\"normal\",host=\"huatuo-dev\",region=\"dev\""
start_container_memory_agent
assert_real_memory_events
# Standard kernels must disable the custom-interface collector cleanly.
if [[ ! -e ${cgroup_root}/memory.directstall_stat && ! -e ${cgroup_root}/memory.asynreclaim_stat && ! -e ${cgroup_root}/memory.local_direct_reclaim_time ]]; then
	assert_container_memory_values memory_others 0 < /dev/null
	grep -qF 'memory_others: kernel does not expose Didi memcg interfaces' "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
		|| fatal "expected unsupported-kernel memory_others diagnostic"
fi
stop_container_memory_agent

# Preserve real kernel files through an alias before replacing only this namespace's view.
original_root="${HUATUO_BAMAI_TEST_TMPDIR}/original-memory"
fixture_root="${HUATUO_BAMAI_TEST_TMPDIR}/cgroup-fixtures"
mkdir -p "${original_root}" "${fixture_root}"
mount --bind "${cgroup_root}" "${original_root}"
if [[ -e ${cgroup_root}/cgroup.controllers ]]; then
	# Keep the cgroup2 root filesystem intact so mode detection still sees v2.
	cp -as "${original_root}${cgroup_relative}/." "${fixture_root}/"
	fixture_container=${fixture_root}
	memory_mount=${container_cgroup}
else
	cp -as "${original_root}/." "${fixture_root}/"
	fixture_container="${fixture_root}${cgroup_relative}"
	memory_mount=${cgroup_root}
	# The root interfaces enable the real memory_others collector on ordinary v1 CI.
	rm -f "${fixture_root}/memory.directstall_stat"
	printf 'directstall_time 0\n' > "${fixture_root}/memory.directstall_stat"
fi
for file in memory.stat memory.events memory.directstall_stat memory.asynreclaim_stat memory.local_direct_reclaim_time; do
	rm -f "${fixture_container}/${file}"
done
printf 'pgfault 31\npgmajfault 0\ninactive_file 37\n' > "${fixture_container}/memory.stat"
printf 'high 0\nmax 3\noom 0\noom_kill 0\n' > "${fixture_container}/memory.events"
printf 'directstall_time 0\n' > "${fixture_container}/memory.directstall_stat"
printf 'asyncreclaim_time 17\n' > "${fixture_container}/memory.asynreclaim_stat"
printf '19\n' > "${fixture_container}/memory.local_direct_reclaim_time"
mount --bind "${fixture_root}" "${memory_mount}"
config_case=fixture
start_container_memory_agent
assert_container_memory_values memory_vmstat < "${fixture_container}/memory.stat"
assert_container_memory_values memory_events < "${fixture_container}/memory.events"
if [[ ${memory_mount} == "${cgroup_root}" ]]; then
	assert_container_memory_values memory_others << 'EXPECTED'
directstall_time 0
asyncreclaim_time 17
local_direct_reclaim_time 19
EXPECTED
	printf 'directstall_time 23\n' > "${fixture_container}/memory.directstall_stat"
	rm "${fixture_container}/memory.asynreclaim_stat"
	assert_container_memory_values memory_others << 'EXPECTED'
directstall_time 23
local_direct_reclaim_time 19
EXPECTED
	rm "${fixture_container}/memory.events"
	assert_container_memory_values memory_events < /dev/null
fi
printf 'pgfault 41\npgmajfault 0\n' > "${fixture_container}/memory.stat"
printf 'high 7\noom 3\n' > "${fixture_container}/memory.events"
assert_container_memory_values memory_vmstat < "${fixture_container}/memory.stat"
assert_container_memory_values memory_events < "${fixture_container}/memory.events"
stop_container_memory_agent

for config_case in include exclude; do
	start_container_memory_agent
	if [[ ${config_case} == include ]]; then
		assert_container_memory_values memory_vmstat <<< 'pgfault 41'
		assert_container_memory_values memory_events <<< 'high 7'
	else
		assert_container_memory_values memory_vmstat <<< 'pgmajfault 0'
		assert_container_memory_values memory_events <<< 'oom 3'
	fi
	stop_container_memory_agent
done
log_info "container memory collectors, updates, filters and missing interfaces verified"
