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

# Controlled events exercise the daemon; they do not create real blocked tasks.

# Controlled syscalls carry real task CSS through the standard daemon and Pod index.
# This verifies attribution, not a real blocked task or kernel reclaim trigger.
set -euo pipefail
source "${ROOT_DIR}/integration/lib_observability.sh"
source "${ROOT_DIR}/e2e/lib.sh"
require_commands kubectl gcc jq curl systemd-detect-virt
systemd-detect-virt --vm --quiet || skip "hungtask stack collection requires a disposable VM"
require_readable "${KUBELET_CERT}" "${KUBELET_KEY}"
kubelet_pods_json > "${HUATUO_BAMAI_TEST_TMPDIR}/pods.json"
node=$(jq -er '[.items[].spec.nodeName | select(. != null and . != "")][0]' "${HUATUO_BAMAI_TEST_TMPDIR}/pods.json")
namespace="huatuo-observe-$(cut -c1-8 /proc/sys/kernel/random/uuid)"
created=false
cleanup_container_events() {
	local status=$?
	huatuo_bamai_stop || status=1
	if [[ ${created} == true ]]; then
		kubectl --request-timeout=40s delete namespace "${namespace}" --timeout=30s || status=1
	fi
	return "${status}"
}
trap cleanup_container_events EXIT
work="${HUATUO_BAMAI_TEST_TMPDIR}/container-fixture"
mkdir -p "${work}/bpf"
compile_user_fixture "${ROOT_DIR}/integration/testdata/test_observability_emit.user.c" "${work}/emit" -static
compile_bpf_fixture "${ROOT_DIR}/integration/testdata/test_hungtask_events.bpf.c" "${work}/bpf/system_hungtask.o" -DCONTAINER_FIXTURE
compile_bpf_fixture "${ROOT_DIR}/integration/testdata/test_memory_reclaim_events.bpf.c" "${work}/bpf/memory_reclaim_events.o"
kubectl --request-timeout=20s create namespace "${namespace}"
created=true
kubectl --request-timeout=20s -n "${namespace}" create serviceaccount workload
create_workload() {
	jq -n --arg node "${node}" --arg image "${BUSINESS_POD_IMAGE}" --arg work "${work}" '
 {apiVersion:"v1",kind:"Pod",metadata:{name:"workload"},spec:{
 nodeName:$node,serviceAccountName:"workload",automountServiceAccountToken:false,
 restartPolicy:"Never",terminationGracePeriodSeconds:1,
 containers:[{name:"workload",image:$image,imagePullPolicy:"Never",
 command:["sleep","infinity"],volumeMounts:[{name:"work",mountPath:"/work",readOnly:true}]}],
 volumes:[{name:"work",hostPath:{path:$work,type:"Directory"}}]}}' \
		| kubectl --request-timeout=20s -n "${namespace}" create -f -
	kubectl --request-timeout=70s -n "${namespace}" wait pod/workload --for=condition=Ready --timeout=60s
	runtime_id=$(kubectl --request-timeout=10s -n "${namespace}" get pod workload -o jsonpath='{.status.containerStatuses[0].containerID}')
	container_id=${runtime_id#*://}
	[[ ${container_id} =~ ^[0-9a-f]{64}$ ]] || fatal "invalid container ID"
}
write_container_events_config() {
	observability_component=hungtask write_config_bamai_observability \
		| sed 's/"memory_reclaim_events",//'
	cat << EOF
[Pod]
 KubeletReadOnlyPort = 0
 KubeletAuthorizedPort = 10250
 KubeletClientCertPath = "${KUBELET_CERT},${KUBELET_KEY}"
EOF
}
create_workload
integration_huatuo_bamai_start write_container_events_config --region integration --log-debug --enable-cgroup --bpf-dir "${work}/bpf"
assert_event_pipes() {
	local log="${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log"
	grep -F 'map_name="hungtask_perf_events"' "${log}" | grep -Fq 'msg="attached BPF and created event pipe"' \
		&& grep -F 'map_name="reclaim_perf_events"' "${log}" | grep -Fq 'msg="attached BPF and created event pipe"'
}
wait_until 15 .1 assert_event_pipes || fatal "container event probes did not attach"
assert_huatuo_bamai_containers_present "${namespace}" '^workload$' 1
emit_container() {
	kubectl --request-timeout=15s -n "${namespace}" exec workload -- /work/emit "$1" "$2"
}
assert_container_events() {
	local id=$1
	jq -e -s --arg id "${id}" '
 [.[] | select(.container_id==$id)] | length==4 and
 all(.[]; .tracer_data.comm=="huatuo-reclaim2" and .tracer_data.reclaim_duration_ns==1000000001)
 ' "${HUATUO_BAMAI_TEST_TMPDIR}/events/memory_reclaim" > /dev/null
}
assert_hungtask_container_count() {
	huatuo_bamai_collect_metrics
	awk '
 /^huatuo_bamai_hungtask_total[{]/ {host=$NF}
 /^huatuo_bamai_hungtask_container_total[{]/ && /container_name="workload"/ {n++; count+=$NF}
 END {exit n!=1 || count!=4 || host<count}
 ' "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt"
}
# Unknown CSS first occupies the host cooldown; resolved events must all survive.
"${work}/emit" huatuo-reclaim1 1
emit_container huatuo-reclaim2 4
wait_until 15 .1 assert_container_events "${container_id}" || fatal "resolved reclaim events were lost or misattributed"
emit_container huatuo-hung 4
wait_until 15 .1 assert_hungtask_container_count || fatal "container hungtask count missing"
wait_until 15 .1 observability_event_ready hungtask || fatal "hungtask event missing"
jq -e -s --arg id "${container_id}" 'any(.[]; .container_id==$id and .tracer_data.comm=="huatuo-hung")' "${HUATUO_BAMAI_TEST_TMPDIR}/events/hungtask" > /dev/null
old_id=${container_id}
kubectl --request-timeout=40s -n "${namespace}" delete pod workload --timeout=30s
assert_huatuo_bamai_containers_absent "${old_id}"
assert_deleted_count() {
	huatuo_bamai_collect_metrics
	! grep -E '^huatuo_bamai_hungtask_container_total[{].*container_name="workload"' "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt"
}
wait_until 15 .1 assert_deleted_count || fatal "deleted container count retained"
# A new binding must be discovered without attributing to the previous instance.
create_workload
[[ ${container_id} != "${old_id}" ]] || fatal "container was not recreated"
assert_huatuo_bamai_containers_present "${namespace}" '^workload$' 1
emit_container huatuo-reclaim2 4
wait_until 15 .1 assert_container_events "${container_id}" || fatal "recreated container attribution did not refresh"
assert_observability_stop
