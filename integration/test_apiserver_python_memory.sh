#!/usr/bin/env bash

# Copyright 2026 The HuaTuo Authors.
# SPDX-License-Identifier: Apache-2.0

# Container selector -> node operation -> Memray -> Toolstream -> ES -> raw API.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_storage.sh"
source "${ROOT_DIR}/integration/config.sh"
source "${ROOT_DIR}/integration/lib_continuous_profiling.sh"
source "${ROOT_DIR}/integration/lib_profiler_python_memory.sh"

readonly ES_PASSWORD="huatuo-integration"
readonly API_TOKEN="integration-admin"
readonly OTHER_API_TOKEN="integration-other"
readonly PROFILE_DURATION=6
readonly PROFILE_INTERVAL=1
readonly PYTHON_IMAGE="${PYTHON_MEMORY_CONTAINER_IMAGE:-python:3.12-slim}"
require_commands docker kubectl crictl gdb python3 jq ss curl ip
docker info > /dev/null 2>&1 || skip "docker daemon is unavailable"
kubectl --request-timeout=5s get nodes > /dev/null 2>&1 || skip "Kubernetes is unavailable"
crictl inspecti "${PYTHON_IMAGE}" > /dev/null 2>&1 \
	|| skip "preload ${PYTHON_IMAGE} in the Kubernetes container runtime"
require_readable "${KUBELET_CERT}" "${KUBELET_KEY}"
readonly MEMORY_POD="memray-$(basename "${HUATUO_BAMAI_TEST_TMPDIR}" | tr '[:upper:]' '[:lower:]' | tr '._' '-')"
readonly MEMORY_NAMESPACE=default
APISERVER_PORT=$(allocate_available_port) || fatal "cannot allocate apiserver port"
readonly APISERVER_PORT
readonly APISERVER_ADDR="http://127.0.0.1:${APISERVER_PORT}"
readonly CREATE_RESPONSE="${HUATUO_BAMAI_TEST_TMPDIR}/memory-create.json"
readonly STATUS_RESPONSE="${HUATUO_BAMAI_TEST_TMPDIR}/memory-status.json"
readonly RAW_RESPONSE="${HUATUO_BAMAI_TEST_TMPDIR}/memory-raw.json"

memory_container_id=""
MEMORY_TARGET_PID=""
memory_pod_created=0
cleanup() {
	local status=$?
	if [[ "${memory_pod_created}" == 1 ]]; then
		if [[ "${status}" != 0 ]]; then
			kubectl --request-timeout=5s -n "${MEMORY_NAMESPACE}" describe pod "${MEMORY_POD}" >&2 || true
		fi
		kubectl --request-timeout=5s -n "${MEMORY_NAMESPACE}" delete pod "${MEMORY_POD}" \
			--wait=false --grace-period=0 --force > /dev/null 2>&1 || true
	fi
	continuous_profiling_cleanup "${status}"
}
trap cleanup EXIT

memory_compile_fixture
cp "${MEMORY_FIXTURE}" "${HUATUO_BAMAI_TEST_TMPDIR}/fixture.py"
# hostPath and host PID signalling require the Pod to run on this test node.
kubectl --request-timeout=5s get nodes -o json > "${HUATUO_BAMAI_TEST_TMPDIR}/memory-nodes.json"
ip -j address > "${HUATUO_BAMAI_TEST_TMPDIR}/memory-addresses.json"
memory_node=$(memory_node_for_local_addresses \
	"${HUATUO_BAMAI_TEST_TMPDIR}/memory-nodes.json" \
	"${HUATUO_BAMAI_TEST_TMPDIR}/memory-addresses.json") \
	|| skip "a single unambiguous local Kubernetes node is required for hostPath and PID signalling"
jq -n --arg name "${MEMORY_POD}" --arg namespace "${MEMORY_NAMESPACE}" \
	--arg node "${memory_node}" --arg image "${PYTHON_IMAGE}" \
	--arg work "${HUATUO_BAMAI_TEST_TMPDIR}" '{
	apiVersion: "v1", kind: "Pod", metadata: {name: $name, namespace: $namespace},
	spec: {nodeName: $node, restartPolicy: "Never",
		tolerations: [{operator: "Exists"}],
		containers: [{name: "python", image: $image, imagePullPolicy: "Never",
			command: ["sh", "-c", "exec python3 -u /work/fixture.py /work/memory-fixture.so > /work/memory-fixture.log 2> /work/memory-fixture.err"],
			volumeMounts: [{name: "work", mountPath: "/work"}]}],
		volumes: [{name: "work", hostPath: {path: $work, type: "Directory"}}]}}
' | kubectl --request-timeout=10s create -f -
memory_pod_created=1
memory_pod_running() {
	memory_container_id=$(kubectl --request-timeout=5s -n "${MEMORY_NAMESPACE}" \
		get pod "${MEMORY_POD}" -o json | jq -er \
		'.status.containerStatuses[]? | select(.name == "python" and .state.running != null) | .containerID | sub("^[^:]+://"; "")')
}
wait_until 20 0.2 memory_pod_running || fatal "Python Pod did not start"
MEMORY_TARGET_PID=$(crictl inspect "${memory_container_id}" | jq -er '.info.pid | select(. > 0)')
memory_wait_for_ack ready
python_version=$(kubectl --request-timeout=5s -n "${MEMORY_NAMESPACE}" exec "${MEMORY_POD}" -- python3 -c \
	'import sys; print("%d.%d" % sys.version_info[:2])')
memory_require_runtime "${python_version}" "${ROOT_DIR}/_output/tools/memray"

storage_start
integration_huatuo_bamai_start write_config_bamai_python_memory --region integration --log-debug
integration_huatuo_apiserver_start write_config_apiserver_python_memory
memory_container_discovered() {
	curl -sf "${CURL_TIMEOUT[@]}" -H 'Authorization: Bearer integration-node-token' \
		"${HUATUO_BAMAI_ADDR}/v1/containers/${memory_container_id}" \
		> "${HUATUO_BAMAI_TEST_TMPDIR}/memory-container.json" \
		&& jq -e --arg id "${memory_container_id}" '.data.id == $id' \
			"${HUATUO_BAMAI_TEST_TMPDIR}/memory-container.json" > /dev/null
}
wait_until 20 0.2 memory_container_discovered || fatal "agent did not discover Python Pod through kubelet"

create_memory_job() {
	local response=$1 status
	status=$(curl -sS "${CURL_TIMEOUT[@]}" -o "${response}" -w '%{http_code}' \
		-X POST -H "Authorization: Bearer ${API_TOKEN}" -H 'Content-Type: application/json' \
		"${APISERVER_ADDR}/v1/profiling" \
		-d "{\"hostname\":\"127.0.0.1\",\"duration_seconds\":${PROFILE_DURATION},\"scope\":\"container\",\"container_id\":\"${memory_container_id}\",\"type\":\"memory\",\"language\":\"python\",\"mode\":\"object_usage\"}")
	assert_eq "${status}" 201 "create Python memory job" \
		|| fatal "API rejected Python memory job: $(cat "${response}")"
}
create_memory_job "${CREATE_RESPONSE}"
PROFILE_ID=$(jq -er '.data.request_id' "${CREATE_RESPONSE}")
memory_api_tracking() {
	curl -sf "${CURL_TIMEOUT[@]}" -H "Authorization: Bearer ${API_TOKEN}" \
		"${APISERVER_ADDR}/v1/profiling/${PROFILE_ID}" > "${STATUS_RESPONSE}" || return 1
	if jq -e '.data.status == "terminal"' "${STATUS_RESPONSE}" > /dev/null; then
		fatal "memory job ended before tracking: $(cat "${STATUS_RESPONSE}")"
	fi
	memory_fixture_has_ack tracking
}
wait_until 10 0.1 memory_api_tracking || fatal "API job did not activate Memray: $(cat "${STATUS_RESPONSE}")"
duplicate_response="${HUATUO_BAMAI_TEST_TMPDIR}/memory-duplicate.json"
duplicate_status="${HUATUO_BAMAI_TEST_TMPDIR}/memory-duplicate-status.json"
create_memory_job "${duplicate_response}"
duplicate_id=$(jq -er '.data.request_id' "${duplicate_response}")
[[ "${duplicate_id}" != "${PROFILE_ID}" ]] || fatal "duplicate request reused the first job ID"
# Do not spend the first capture's duration waiting for rejection status.
memory_request_phase allocated
memory_request_phase released
wait_until 5 0.1 continuous_profile_status_is "${duplicate_id}" failed "${duplicate_status}" \
	|| fatal "concurrent memory job was not rejected: $(cat "${duplicate_status}")"
assert_eq "$(jq -r '.data.terminal.reason' "${duplicate_status}")" execution_failed \
	"duplicate job execution must fail"
grep -q 'Memray session already active' "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
	|| fatal "duplicate job failed for an unrelated reason: $(cat "${duplicate_status}")"
wait_until 20 0.2 continuous_profile_status_is \
	"${PROFILE_ID}" completed "${STATUS_RESPONSE}" \
	|| fatal "memory job did not complete: $(cat "${STATUS_RESPONSE}")"
memory_wait_for_ack deactivated

memory_profile_stored() {
	curl -sf "${CURL_TIMEOUT[@]}" -H "Authorization: Bearer ${API_TOKEN}" \
		"${APISERVER_ADDR}/v1/profiling/${PROFILE_ID}/raw" > "${RAW_RESPONSE}" \
		&& jq -e '(.data.items | length) > 0' "${RAW_RESPONSE}" > /dev/null
}
wait_until 15 0.2 memory_profile_stored || fatal "API returned no stored memory profile"
# API coverage stops at lifecycle, storage, metadata, and labels. Stack order,
# symbolization, and retained byte accounting belong to the CLI test.
assert_eq "$(jq '.data.has_more' "${RAW_RESPONSE}")" false "single retained-memory result page"
assert_eq "$(jq '.data.items | length' "${RAW_RESPONSE}")" 1 "one stored retained-memory profile"
assert_eq "$(jq -r '.data.items[0].container_id' "${RAW_RESPONSE}")" "${memory_container_id}" "stored container selector"
assert_eq "$(jq -r '.data.items[0].profile_type' "${RAW_RESPONSE}")" \
	memory:alloc_space:bytes:space:bytes "stored memory profile type"
assert_eq "$(jq '.data.items[0].profile as $p |
	$p.string_table[($p.sample_type[0].unit | tonumber)] == "bytes"' "${RAW_RESPONSE}")" true "stored byte unit"
assert_eq "$(jq --arg pid "${MEMORY_TARGET_PID}" '
	.data.items[0].profile as $p |
	($p.sample | length) > 0 and
	all($p.sample[]; (.value[0] | tonumber) >= 0 and
		any(.label[]? | select($p.string_table[(.key | tonumber)] == "pid");
			$p.string_table[((.str // 0) | tonumber)] == $pid)) and
	any($p.sample[]; (.value[0] | tonumber) > 0)' "${RAW_RESPONSE}")" \
	true "stored samples must have nonnegative bytes and the target host PID"
# The duplicate operation is expected to log a failure; reject other failures.
grep -v 'Memray session already active' "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
	> "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-unexpected.log" || true
assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-unexpected.log" huatuo-bamai
assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.log" huatuo-apiserver
! grep -q 'Traceback\|Failed to write output' "${MEMORY_FIXTURE_ERR}" \
	|| fatal "target tracking failed: $(cat "${MEMORY_FIXTURE_ERR}")"
