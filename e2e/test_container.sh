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

set -euo pipefail

source ${ROOT_DIR}/integration/lib.sh
source ${ROOT_DIR}/e2e/lib.sh

test_huatuo_bamai_default_container_exists() {
	log_info "⬅️ test huatuo-bamai default container exists"

	assert_kubelet_pod_count \
		"${BUSINESS_POD_NS}" \
		"${BUSINESS_DEFAULT_POD_NAME_REGEX}" \
		"${BUSINESS_DEFAULT_POD_COUNT}" \
		"default pod exists in kubelet"

	assert_huatuo_bamai_pod_count \
		"${BUSINESS_DEFAULT_POD_NAME_REGEX}" \
		"${BUSINESS_DEFAULT_POD_COUNT}" \
		"default pod exists in huatuo-bamai"

	log_info "✅ test huatuo-bamai default container exists ok"
}

test_huatuo_bamai_e2e_container_create() {
	log_info "⬅️ creating e2e test pods"

	# ensure clean
	k8s_delete_pod "${BUSINESS_POD_NS}" "${BUSINESS_E2E_TEST_POD_LABEL}" || true

	assert_kubelet_pod_count \
		"${BUSINESS_POD_NS}" \
		"${BUSINESS_E2E_TEST_POD_NAME_REGEX}" \
		"0" \
		"kubelet e2e pods cleaned"

	assert_huatuo_bamai_pod_count \
		"${BUSINESS_E2E_TEST_POD_NAME_REGEX}" \
		"0" \
		"huatuo-bamai e2e pods cleaned"

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

	assert_huatuo_bamai_pod_count \
		"${BUSINESS_E2E_TEST_POD_NAME_REGEX}" \
		"${BUSINESS_E2E_TEST_POD_COUNT}" \
		"huatuo-bamai e2e pods created"

	log_info "✅ test huatuo-bamai e2e container create ok"
}

test_huatuo_bamai_e2e_container_delete() {
	log_info "⬅️ deleting e2e test pods"

	assert_kubelet_pod_count \
		"${BUSINESS_POD_NS}" \
		"${BUSINESS_E2E_TEST_POD_NAME_REGEX}" \
		"${BUSINESS_E2E_TEST_POD_COUNT}" \
		"kubelet e2e pods exist before delete"

	assert_huatuo_bamai_pod_count \
		"${BUSINESS_E2E_TEST_POD_NAME_REGEX}" \
		"${BUSINESS_E2E_TEST_POD_COUNT}" \
		"huatuo-bamai e2e pods exist before delete"

	k8s_delete_pod "${BUSINESS_POD_NS}" "${BUSINESS_E2E_TEST_POD_LABEL}"

	assert_kubelet_pod_count \
		"${BUSINESS_POD_NS}" \
		"${BUSINESS_E2E_TEST_POD_NAME_REGEX}" \
		"0" \
		"kubelet e2e pods deleted"

	assert_huatuo_bamai_pod_count \
		"${BUSINESS_E2E_TEST_POD_NAME_REGEX}" \
		"0" \
		"huatuo-bamai e2e pods deleted"

	log_info "✅ test huatuo-bamai e2e container delete ok"
}

test_huatuo_bamai_default_container_exists
test_huatuo_bamai_e2e_container_create
test_huatuo_bamai_e2e_container_delete
