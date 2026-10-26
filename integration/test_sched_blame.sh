#!/usr/bin/env bash

# Copyright 2026 The HuaTuo Authors.
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

# Exercise the built command with real scheduler hooks, perf transport, and
# kubelet discovery. Exact attribution and anomaly decisions have unit coverage.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

kernel_version_le 6 5 || skip "SchedBlame requires Linux 4.18 through 6.5"
kernel_version_le 4 17 && skip "SchedBlame requires Linux 4.18 through 6.5"
# Check concrete prerequisites, not container identity or root filesystem type.
require_commands kubectl crictl jq python3 gcc ip curl
require_readable /sys/kernel/btf/vmlinux "${KUBELET_CERT}" "${KUBELET_KEY}"
for event in sched_switch sched_wakeup sched_wakeup_new sched_migrate_task; do
	tracepoint_available sched "${event}" || skip "sched/${event} is unavailable"
done
for event in cgroup_mkdir cgroup_rmdir; do
	tracepoint_available cgroup "${event}" || skip "cgroup/${event} is unavailable"
done
kprobe_available unthrottle_cfs_rq || skip "unthrottle_cfs_rq is unavailable"

readonly SCHED_BLAME_BIN="${ROOT_DIR}/_output/bin/sched-blame"
readonly BPF_DIR="${ROOT_DIR}/_output/bpf"
readonly CHECKER="${ROOT_DIR}/integration/testdata/test_sched_blame_csv.py"
readonly TRACER_LOG="${HUATUO_BAMAI_TEST_TMPDIR}/sched-blame.log"
readonly RATIO_FILE="${HUATUO_BAMAI_TEST_TMPDIR}/ratios.csv"
readonly PHASE_FILE="${HUATUO_BAMAI_TEST_TMPDIR}/phases.tsv"
readonly WORK_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/workload"
readonly CASE_ID=$(tr -d '-' < /proc/sys/kernel/random/uuid | cut -c1-12)
readonly TEST_NAMESPACE="huatuo-sched-blame-${CASE_ID}"
readonly TARGET_POD="sb-target-${CASE_ID}"
readonly COMPETITOR_POD="sb-competitor-${CASE_ID}"
tracer_pid=""
namespace_created=false

cleanup() {
	local status=$?
	trap - EXIT
	# The runner allows five seconds for signal cleanup. Request Kubernetes
	# deletion first, without waiting for finalizers, before stopping the tracer.
	if [[ ${namespace_created} == true && (${status} == 130 || ${status} == 143) ]]; then
		kubectl --request-timeout=2s delete namespace "${TEST_NAMESPACE}" --wait=false || true
		namespace_created=false
	fi
	if [[ -n ${tracer_pid} ]]; then
		stop_by_pid "${tracer_pid}" 2
		wait "${tracer_pid}" 2> /dev/null || true
	fi
	if [[ ${namespace_created} == true ]]; then
		if [[ ${status} -ne 0 ]]; then
			kubectl --request-timeout=10s -n "${TEST_NAMESPACE}" get pods -o yaml \
				> "${HUATUO_BAMAI_TEST_TMPDIR}/pods-at-exit.yaml" 2>&1 || true
			kubectl --request-timeout=10s -n "${TEST_NAMESPACE}" logs "${TARGET_POD}" \
				> "${HUATUO_BAMAI_TEST_TMPDIR}/target-at-exit.log" 2>&1 || true
			kubectl --request-timeout=10s -n "${TEST_NAMESPACE}" logs "${COMPETITOR_POD}" \
				> "${HUATUO_BAMAI_TEST_TMPDIR}/competitor-at-exit.log" 2>&1 || true
		fi
		kubectl --request-timeout=40s delete namespace "${TEST_NAMESPACE}" \
			--wait=true --timeout=30s || status=1
	fi
	exit "${status}"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

[[ -x ${SCHED_BLAME_BIN} && -r ${BPF_DIR}/sched_blame.o ]] \
	|| fatal "SchedBlame build output is missing; run make build before this test"
kubectl --request-timeout=10s get nodes -o json \
	> "${HUATUO_BAMAI_TEST_TMPDIR}/nodes.json" || skip "Kubernetes API is unavailable"
ip -j address > "${HUATUO_BAMAI_TEST_TMPDIR}/addresses.json"
TEST_NODE=$(
	python3 - "${HUATUO_BAMAI_TEST_TMPDIR}" << 'PYTHON'
import json
from pathlib import Path
import sys
root = Path(sys.argv[1])
addresses = {a['local'] for i in json.loads((root / 'addresses.json').read_text()) for a in i['addr_info']}
nodes = [n['metadata']['name'] for n in json.loads((root / 'nodes.json').read_text())['items']
         if any(a['type'] == 'InternalIP' and a['address'] in addresses for a in n['status']['addresses'])]
if len(nodes) != 1:
    sys.exit('expected exactly one local Kubernetes node')
print(nodes[0])
PYTHON
) || skip "a local Kubernetes node is required"
readonly TEST_NODE
SELECTED_CPU=$(python3 -c 'import os; print(max(cpu for cpu in os.sched_getaffinity(0) if cpu < 512))') \
	|| skip "no allowed CPU fits the SchedBlame CPU limit"
readonly SELECTED_CPU
crictl inspecti "${BUSINESS_POD_IMAGE}" > /dev/null 2>&1 \
	|| skip "test image is not available through CRI: ${BUSINESS_POD_IMAGE}"
curl -fsSk "${CURL_TIMEOUT[@]}" --cert "${KUBELET_CERT}" --key "${KUBELET_KEY}" \
	"${KUBELET_PODS_API}" -o /dev/null \
	|| skip "authenticated local kubelet discovery is unavailable"

mkdir -p "${WORK_DIR}"
compile_user_fixture "${ROOT_DIR}/integration/testdata/test_sched_blame.user.c" \
	"${WORK_DIR}/load" -static
kubectl --request-timeout=30s create namespace "${TEST_NAMESPACE}"
namespace_created=true
# Do not race asynchronous creation of the namespace's default service account.
kubectl --request-timeout=30s -n "${TEST_NAMESPACE}" create serviceaccount workload

start_workload() {
	local pod=$1 mode=$2 runtime_id
	jq -n --arg pod "${pod}" --arg node "${TEST_NODE}" \
		--arg image "${BUSINESS_POD_IMAGE}" --arg fixtures "${WORK_DIR}" \
		--arg cpu "${SELECTED_CPU}" --arg mode "${mode}" '
		{apiVersion:"v1",kind:"Pod",metadata:{name:$pod},spec:{
		  nodeName:$node,restartPolicy:"Never",terminationGracePeriodSeconds:1,
		  serviceAccountName:"workload",automountServiceAccountToken:false,
		  containers:[{name:"workload",image:$image,imagePullPolicy:"Never",
		    command:["/work/load",$cpu,$mode,"/work/compete"],
		    volumeMounts:[{name:"work",mountPath:"/work",readOnly:true}]}],
		  volumes:[{name:"work",hostPath:{path:$fixtures,type:"Directory"}}]}}' \
		> "${HUATUO_BAMAI_TEST_TMPDIR}/${pod}.json"
	kubectl --request-timeout=30s -n "${TEST_NAMESPACE}" create \
		-f "${HUATUO_BAMAI_TEST_TMPDIR}/${pod}.json"
	kubectl --request-timeout=70s -n "${TEST_NAMESPACE}" wait "pod/${pod}" \
		--for=condition=Ready --timeout=60s
	runtime_id=$(kubectl --request-timeout=10s -n "${TEST_NAMESPACE}" get pod "${pod}" \
		-o jsonpath='{.status.containerStatuses[0].containerID}')
	workload_container_id=${runtime_id#*://}
	[[ ${workload_container_id} =~ ^[0-9a-f]{64}$ ]] \
		|| fatal "invalid runtime container ID for ${pod}: ${runtime_id}"
}

start_workload "${TARGET_POD}" target
target_container_id=${workload_container_id}
start_workload "${COMPETITOR_POD}" competitor

cat > "${HUATUO_BAMAI_TEST_TMPDIR}/sched-blame.conf" << EOF
[Log]
    Level = "debug"
[Pod]
    KubeletReadOnlyPort = 0
    KubeletAuthorizedPort = 10250
    KubeletClientCertPath = "${KUBELET_CERT},${KUBELET_KEY}"
[AutoTracing.SchedBlame]
    TargetContainerScope = "normal"
    SliceDropPercent = 0
    ExternalAnomalyK = 1.0
    SliceBatchSize = 128
    PerfEventPerCPUBufferBytes = 524288
    PollIntervalMs = 100
    PerfEventWatermarkBytes = 8192
    PerfEventQueueRecords = 8192
EOF
"${SCHED_BLAME_BIN}" --config "${HUATUO_BAMAI_TEST_TMPDIR}/sched-blame.conf" \
	--bpf-dir "${BPF_DIR}" --output-file "${HUATUO_BAMAI_TEST_TMPDIR}/events.txt" \
	--highlight-container "${TARGET_POD}" --ratio-debug-file "${RATIO_FILE}" --log-debug \
	> "${TRACER_LOG}" 2>&1 &
tracer_pid=$!

tracer_started() {
	kill -0 "${tracer_pid}" 2> /dev/null || fatal "sched-blame exited during startup"
	grep -Fq 'transport=perf_event_array' "${TRACER_LOG}"
}
wait_until 60 1 tracer_started || fatal "sched-blame did not attach and start collection"

printf 'phase\tcontainer_id\tstart\tend\n' > "${PHASE_FILE}"
capture_phase() {
	local phase=$1 container_id=$2 start end
	start=$(date +%s.%N)
	log_info "sched-blame phase=${phase} cpu=${SELECTED_CPU} target=${container_id}"
	wait_until 30 1 python3 "${CHECKER}" ready "${RATIO_FILE}" "${container_id}" "${start}" \
		|| fatal "sched-blame did not collect valid runtime samples during ${phase}"
	kill -0 "${tracer_pid}" 2> /dev/null || fatal "sched-blame exited during ${phase}"
	end=$(date +%s.%N)
	printf '%s\t%s\t%s\t%s\n' "${phase}" "${container_id}" "${start}" "${end}" >> "${PHASE_FILE}"
}

capture_phase baseline "${target_container_id}"
touch "${WORK_DIR}/compete"
capture_phase competing "${target_container_id}"
kubectl --request-timeout=40s -n "${TEST_NAMESPACE}" delete pod "${COMPETITOR_POD}" \
	--wait=true --timeout=30s
capture_phase recovered "${target_container_id}"

kubectl --request-timeout=40s -n "${TEST_NAMESPACE}" delete pod "${TARGET_POD}" \
	--wait=true --timeout=30s
wait_until 70 1 grep -Fq \
	"removed target dense_id=0 container=${TARGET_POD} id=${target_container_id:0:13}" "${TRACER_LOG}" \
	|| fatal "sched-blame did not remove the deleted highlighted target"
# Immediate slot reuse accepts late records. Check identity and history reset,
# not exact charges in the first replacement interval.
start_workload "${TARGET_POD}" target
replacement_container_id=${workload_container_id}
[[ ${replacement_container_id} != "${target_container_id}" ]] \
	|| fatal "recreated pod retained the old container ID"
capture_phase replacement "${replacement_container_id}"

stop_gracefully_by_pid "${tracer_pid}" 15 || fatal "sched-blame did not shut down cleanly"
tracer_pid=""
assert_log_has_no_failure "${TRACER_LOG}" sched-blame
[[ -f ${HUATUO_BAMAI_TEST_TMPDIR}/events.txt ]] || fatal "command did not create its output file"
python3 "${CHECKER}" verify "${RATIO_FILE}" "${PHASE_FILE}" "${TRACER_LOG}" "${TARGET_POD}"
log_info "sched-blame live collection verified"
