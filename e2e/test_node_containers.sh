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

# Verify bamai container discovery/deletion and host-only sockstat memory
# through kubelet, the container API, and /metrics using temporary test Pods.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/e2e/lib.sh"

require_commands kubectl jq curl

container_ids=()

test_huatuo_bamai_existing_container_exists() {
	local existing namespace pod_name pod_name_regex
	log_info "⬅️ test huatuo-bamai discovers an existing Kubernetes container"

	existing=$(kubectl get pods -n kube-system \
		--field-selector=status.phase=Running -o json | jq -er '
			first(.items[]
				| select(.spec.hostNetwork != true)
				| select(.status.containerStatuses != null)
				| select(all(.status.containerStatuses[]; .ready == true))
				| select(.spec.containers | length == 1)
				| [.metadata.namespace, .metadata.name]
				| @tsv)') || fatal "no running single-container Kubernetes component pod is available"
	IFS=$'\t' read -r namespace pod_name <<< "$existing"
	pod_name_regex="^${pod_name}$"

	assert_kubelet_pod_count \
		"$namespace" \
		"$pod_name_regex" \
		"1" \
		"existing Kubernetes component exists in kubelet"

	assert_huatuo_bamai_containers_present \
		"$namespace" \
		"$pod_name_regex" \
		"1" \
		"existing Kubernetes component exists in huatuo-bamai"

	log_info "✅ existing Kubernetes container discovered: ${namespace}/${pod_name}"
}

test_huatuo_bamai_e2e_container_create() {
	log_info "⬅️ creating e2e test pods"
	local ids
	local -a stale_container_ids=()

	# ensure clean
	ids=$(kubelet_container_ids \
		"${BUSINESS_POD_NS}" \
		"${BUSINESS_E2E_TEST_POD_NAME_REGEX}") || fatal "cannot read stale container IDs"
	if [[ -n ${ids} ]]; then mapfile -t stale_container_ids <<< "${ids}"; fi
	k8s_delete_pod "${BUSINESS_POD_NS}" "${BUSINESS_E2E_TEST_POD_LABEL}"

	assert_kubelet_pod_count \
		"${BUSINESS_POD_NS}" \
		"${BUSINESS_E2E_TEST_POD_NAME_REGEX}" \
		"0" \
		"kubelet e2e pods cleaned"

	if [[ ${#stale_container_ids[@]} -gt 0 ]]; then
		assert_huatuo_bamai_containers_absent \
			"huatuo-bamai e2e containers cleaned" \
			"${stale_container_ids[@]}"
	fi

	# create
	k8s_create_pod \
		"${BUSINESS_POD_NS}" \
		"${BUSINESS_E2E_TEST_POD_NAME}" \
		"${BUSINESS_POD_IMAGE}" \
		"${BUSINESS_E2E_TEST_POD_LABEL}" \
		"${BUSINESS_E2E_TEST_POD_COUNT}"

	assert_kubelet_pod_count \
		"${BUSINESS_POD_NS}" \
		"${BUSINESS_E2E_TEST_POD_NAME_REGEX}" \
		"${BUSINESS_E2E_TEST_POD_COUNT}" \
		"kubelet e2e pods created"

	assert_huatuo_bamai_containers_present \
		"${BUSINESS_POD_NS}" \
		"${BUSINESS_E2E_TEST_POD_NAME_REGEX}" \
		"${BUSINESS_E2E_TEST_POD_COUNT}" \
		"huatuo-bamai e2e pods created"
	ids=$(kubelet_container_ids \
		"${BUSINESS_POD_NS}" \
		"${BUSINESS_E2E_TEST_POD_NAME_REGEX}") || fatal "cannot read created container IDs"
	container_ids=()
	if [[ -n ${ids} ]]; then mapfile -t container_ids <<< "${ids}"; fi
	assert_eq "${#container_ids[@]}" "${BUSINESS_E2E_TEST_POD_COUNT}" "created container ID count" \
		|| fatal "created container IDs do not match test Pod count"

	log_info "✅ test huatuo-bamai e2e container create ok"
}

assert_sockstat_container_filter() {
	huatuo_bamai_collect_metrics
	# Require samples from each created pod before checking absent fields.
	# Otherwise a failed container discovery would also satisfy the filter.
	local index
	for ((index = 1; index <= BUSINESS_E2E_TEST_POD_COUNT; index++)); do
		assert_metrics "sockstat container ${index}" \
			"sockstat_container_TCP_inuse\\{.*container_host=\"${BUSINESS_E2E_TEST_POD_NAME}-${index}\""
	done
	assert_metrics "sockstat memory is host-only" \
		'sockstat_TCP_mem\{' 'sockstat_TCP_mem_bytes\{' \
		-- 'sockstat_container_.*_mem(_bytes)?\{'
}

test_huatuo_bamai_e2e_container_delete() {
	log_info "⬅️ deleting e2e test pods"

	assert_kubelet_pod_count \
		"${BUSINESS_POD_NS}" \
		"${BUSINESS_E2E_TEST_POD_NAME_REGEX}" \
		"${BUSINESS_E2E_TEST_POD_COUNT}" \
		"kubelet e2e pods exist before delete"

	assert_huatuo_bamai_containers_present \
		"${BUSINESS_POD_NS}" \
		"${BUSINESS_E2E_TEST_POD_NAME_REGEX}" \
		"${BUSINESS_E2E_TEST_POD_COUNT}" \
		"huatuo-bamai e2e pods exist before delete"

	k8s_delete_pod "${BUSINESS_POD_NS}" "${BUSINESS_E2E_TEST_POD_LABEL}"

	assert_kubelet_pod_count \
		"${BUSINESS_POD_NS}" \
		"${BUSINESS_E2E_TEST_POD_NAME_REGEX}" \
		"0" \
		"kubelet e2e pods deleted"

	assert_huatuo_bamai_containers_absent \
		"huatuo-bamai e2e containers deleted" \
		"${container_ids[@]}"

	log_info "✅ test huatuo-bamai e2e container delete ok"
}

cleanup_business_pods() {
	local status=$?
	trap - EXIT
	if ! k8s_delete_pod "${BUSINESS_POD_NS}" "${BUSINESS_E2E_TEST_POD_LABEL}"; then
		log_error "failed to clean up test Pods in ${BUSINESS_POD_NS} with label ${BUSINESS_E2E_TEST_POD_LABEL}"
		if [[ ${status} -eq 0 ]]; then status=1; fi
	fi
	exit "${status}"
}

trap cleanup_business_pods EXIT
test_huatuo_bamai_existing_container_exists
test_huatuo_bamai_e2e_container_create
assert_sockstat_container_filter
test_huatuo_bamai_e2e_container_delete
