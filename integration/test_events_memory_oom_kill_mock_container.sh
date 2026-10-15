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

# Exercise OOM attribution and enrichment with a real container and a mock hook.
set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"
source "${ROOT_DIR}/integration/lib_bpf_mock.sh"
source "${ROOT_DIR}/integration/bpf_mock/common/elasticsearch.sh"
source "${ROOT_DIR}/integration/bpf_mock/common/container.sh"

skip_known_mock_kprobe_kernel

require_commands make gcc kubectl jq ss insmod rmmod
require_readable "${KUBELET_CERT}" "${KUBELET_KEY}"
if kernel_version_le 4 17; then
	skip "OOM fixture requires Linux 4.18 or newer"
fi
case $(uname -m) in
x86_64 | aarch64) ;;
*) skip "OOM fixture requires x86_64 or aarch64" ;;
esac

mkdir -p "${HUATUO_BAMAI_TEST_TMPDIR}/trigger"
readonly oom_namespace="bpf-oom-${BASHPID}-${RANDOM}"
readonly oom_pod="oom-trigger"
readonly oom_device=/dev/huatuo_oom_mock
readonly oom_trigger_name="oom-mock-$(head -c 6 /proc/sys/kernel/random/uuid)"
readonly oom_events="${HUATUO_BAMAI_TEST_TMPDIR}/es-oom-events.json"
readonly -a oom_scenarios=(same-task other-task memcg other-task-memcg null-victim)
readonly oom_metadata="${HUATUO_BAMAI_TEST_TMPDIR}/container.json"
oom_kubelet_port=${KUBELET_PODS_API##*:}
oom_kubelet_port=${oom_kubelet_port%%/*}
readonly oom_kubelet_port

kernel_build_dir=${KERNEL_BUILD_DIR:-/lib/modules/$(uname -r)/build}
[[ -d "${kernel_build_dir}" ]] || skip "kernel build directory is missing: ${kernel_build_dir}"
[[ -r "${ROOT_DIR}/_output/bpf/memory_oom_kill.o" ]] || fatal "memory_oom_kill.o is missing; run make build"
fixture_dir="${HUATUO_BAMAI_TEST_TMPDIR}/module"
mkdir -p "${fixture_dir}"
cp "${ROOT_DIR}/integration/bpf_mock/oom/"{Makefile,oom_mock_kmod.c,oom_mock.h} "${fixture_dir}/"
make -C "${fixture_dir}" KERNEL_BUILD_DIR="${kernel_build_dir}" \
	> "${HUATUO_BAMAI_TEST_TMPDIR}/module-build.log" 2>&1 \
	|| fatal "OOM module build failed; see module-build.log"
# Busybox does not provide the host libc. A static trigger has no image dependencies.
compile_user_fixture "${ROOT_DIR}/integration/bpf_mock/oom/trigger.c" \
	"${HUATUO_BAMAI_TEST_TMPDIR}/trigger/${oom_trigger_name}" -static

module_loaded=0
namespace_created=0
cleanup() {
	local status=$?
	if [[ ${status} -ne 0 && ${namespace_created} == 1 ]]; then
		kubectl --request-timeout=10s describe pod -n "${oom_namespace}" "${oom_pod}" \
			> "${HUATUO_BAMAI_TEST_TMPDIR}/pod-failure.log" 2>&1 || true
	fi
	huatuo_bamai_stop || status=1
	if [[ ${status} -ne 0 && ${status} -ne 77 ]]; then
		storage_dump_logs || true
	fi
	stop_bpf_mock_es || status=1
	if [[ ${namespace_created} == 1 ]]; then
		kubectl --request-timeout=10s delete namespace "${oom_namespace}" --wait=true --timeout=60s || status=1
	fi
	if [[ ${module_loaded} == 1 ]]; then
		rmmod huatuo_oom_mock || status=1
	fi
	return "${status}"
}
trap 'cleanup || exit $?' EXIT
start_bpf_mock_es
oom_port=$(allocate_available_port) || fatal "cannot allocate a test daemon port"
readonly oom_port
HUATUO_BAMAI_ADDR="http://127.0.0.1:${oom_port}"
HUATUO_BAMAI_METRICS_API="${HUATUO_BAMAI_ADDR}/metrics"
insmod "${fixture_dir}/huatuo_oom_mock.ko"
module_loaded=1
wait_until 5 0.1 test -e "${oom_device}" || fatal "OOM mock device did not appear"

oom_node=${BPF_MOCK_NODE:-}
if [[ -z ${oom_node} ]]; then
	# Kubelet returns local pods, whose nodeName identifies this VM.
	oom_node=$(curl -fsk "${CURL_TIMEOUT[@]}" --cert "${KUBELET_CERT}" --key "${KUBELET_KEY}" \
		"${KUBELET_PODS_API}" | jq -er 'first(.items[].spec.nodeName | select(. != null and . != ""))') \
		|| fatal "cannot determine the local node from kubelet; set BPF_MOCK_NODE to this machine's Kubernetes node name"
fi
readonly oom_node
kubectl --request-timeout=10s create namespace "${oom_namespace}"
namespace_created=1
kubectl --request-timeout=10s label namespace "${oom_namespace}" pod-security.kubernetes.io/enforce=privileged
bpf_mock_pod_manifest "${oom_namespace}" "${oom_pod}" "${oom_node}" "${BUSINESS_POD_IMAGE}" \
	"${HUATUO_BAMAI_TEST_TMPDIR}/trigger" "${oom_device}" > "${HUATUO_BAMAI_TEST_TMPDIR}/pod.json"
kubectl --request-timeout=10s apply -f "${HUATUO_BAMAI_TEST_TMPDIR}/pod.json"
kubectl --request-timeout=130s wait -n "${oom_namespace}" --for=condition=Ready "pod/${oom_pod}" --timeout=120s \
	|| fatal "mock trigger pod did not become ready"

oom_trigger() {
	kubectl --request-timeout=8s exec -n "${oom_namespace}" "${oom_pod}" -c trigger -- \
		"/mock/${oom_trigger_name}" "$@"
}

write_oom_container_config() {
	write_bpf_mock_es_config memory_oom_kill
	cat >> "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" << EOF

[HTTPServer]
    ListenAddress = "127.0.0.1:${oom_port}"
[Pod]
    KubeletReadOnlyPort = 0
    KubeletAuthorizedPort = ${oom_kubelet_port}
    KubeletClientCertPath = "${KUBELET_CERT},${KUBELET_KEY}"
EOF
}
integration_huatuo_bamai_start write_oom_container_config --region dev --log-debug \
	--bpf-mock --bpf-mock-object memory_oom_kill.o
wait_until 15 0.1 grep -q 'attached BPF program.*symbol="oom_kill_process_mock"' \
	"${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" || fatal "OOM did not attach to the mock hook"
wait_until 15 0.1 grep -q 'attached BPF and created event pipe.*map_name="oom_perf_events"' \
	"${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" || fatal "OOM event pipe did not start"

container_is_discovered() {
	local id
	id=$(kubectl --request-timeout=5s get pod -n "${oom_namespace}" "${oom_pod}" -o json \
		| jq -er '.status.containerStatuses[] | select(.name == "trigger") | .containerID | sub("^[^:]+://"; "")') || return 1
	[[ ${id} =~ ^[0-9a-f]{64}$ ]] || return 1
	curl -fsS "${CURL_TIMEOUT[@]}" "${HUATUO_BAMAI_ADDR}/v1/containers/${id}" > "${oom_metadata}" || return 1
	jq -e --arg id "$id" '.data.id == $id and (.data.cgroup_css.memory | test("^0x[0-9a-f]{16}$"))' \
		"${oom_metadata}" > /dev/null 2>&1
}
wait_until 60 0.5 container_is_discovered || {
	# The runner prints only the first 160 lines; discovery logs can follow them.
	log_error "----- Huatuo container discovery log -----"
	cat "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" >&2 || true
	fatal "Huatuo did not discover the pod memory CSS"
}
oom_container_id=$(jq -er '.data.id' "${oom_metadata}")
readonly oom_container_id
readonly oom_container_expectation="${HUATUO_BAMAI_TEST_TMPDIR}/expected-container.json"
jq --arg pod "${oom_pod}" '.data.hostname = $pod' "${oom_metadata}" > "${oom_container_expectation}"

host_counter() {
	huatuo_bamai_collect_metrics || return 1
	awk '/^huatuo_bamai_memory_oom_kill_host_total\{/ { print $2; found = 1 } END { exit !found }' \
		"${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt"
}
oom_host_before=$(host_counter) || fatal "OOM host counter baseline is missing"
readonly oom_host_before

for scenario in "${oom_scenarios[@]}"; do
	oom_trigger "$scenario" 0x1ace 321 > "${HUATUO_BAMAI_TEST_TMPDIR}/${scenario}.json"
done
oom_trigger null-control 0x1ace 321 > "${HUATUO_BAMAI_TEST_TMPDIR}/null-control.json"

container_counter_is_four() {
	huatuo_bamai_collect_metrics || return 1
	awk -v pod="${oom_pod}" '
  /^huatuo_bamai_memory_oom_kill_container_total\{/ && index($0, "container_host=\"" pod "\"") && index($0, "container_name=\"trigger\"") {
   found++; value = $2
  }
  END { exit !(found == 1 && value == 4) }
 ' "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt"
}
wait_until 20 0.2 container_counter_is_four || fatal "container OOM counter did not increase to four"
# Metrics follow the victim: the missing-victim event increments host_total.
host_counter_is_expected() {
	local after
	after=$(host_counter) || return 1
	awk -v before="${oom_host_before}" -v after="$after" 'BEGIN { exit !(after == before + 1) }'
}
wait_until 20 0.2 host_counter_is_expected \
	|| fatal "missing-victim OOM did not increment the host counter by one"
stop_bpf_mock_daemon
es_events_are_complete() {
	read_bpf_mock_es_events "${oom_events}" || return 1
	jq -se 'length == 5' "${oom_events}" > /dev/null
}
wait_until 30 0.5 es_events_are_complete || fatal "expected exactly five ES OOM documents"

run_bpf_mock_checker oom -container "${oom_container_expectation}" "${oom_events}" "${HUATUO_BAMAI_TEST_TMPDIR}" "${oom_scenarios[@]}" null-control

kubectl --request-timeout=10s get pod -n "${oom_namespace}" "${oom_pod}" -o json \
	| jq -e --arg id "${oom_container_id}" '
  .status.phase == "Running" and .status.containerStatuses[0].ready
  and .status.containerStatuses[0].restartCount == 0
  and (.status.containerStatuses[0].containerID | sub("^[^:]+://"; "")) == $id
 ' > /dev/null || fatal "mock events killed or restarted the test container"
log_info "container mock OOM passed: ES documents, discovery, actor attribution, cgroup snapshots, container_total=4, host_total +1 for null victim"
