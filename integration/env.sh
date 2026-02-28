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

# prevent multiple loading
if [[ -n "${__HUATUO_ENV_SH_LOADED:-}" ]]; then
	return 0
fi
export __HUATUO_ENV_SH_LOADED=1

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/.."

HUATUO_BAMAI_BIN="${ROOT_DIR}/_output/bin/huatuo-bamai"
HUATUO_BAMAI_TEST_TMPDIR=$(mktemp -d /tmp/huatuo-test.XXXXXX)
HUATUO_BAMAI_MATCH_KEYWORDS="\"error\"|panic"
HUATUO_BAMAI_TEST_FIXTURES="${ROOT_DIR}/integration/fixtures"
HUATUO_BAMAI_TEST_EXPECTED="${ROOT_DIR}/integration/fixtures/expected_metrics"
HUATUO_BAMAI_ARGS_INTEGRATION=(
	"--config-dir" "${HUATUO_BAMAI_TEST_TMPDIR}"
	"--config" "bamai.conf"
	"--region" "dev"
	"--procfs-prefix" "${HUATUO_BAMAI_TEST_FIXTURES}"
	"--disable-storage"
	"--disable-kubelet"
)
HUATUO_BAMAI_ARGS_E2E=(
	"--config-dir" "${ROOT_DIR}/_output/conf/"
	"--config" "huatuo-bamai.conf"
	"--region" "e2e"
	"--disable-storage"
)
HUATUO_BAMAI_ADDR="http://127.0.0.1:19704"
HUATUO_BAMAI_METRICS_API="${HUATUO_BAMAI_ADDR}/metrics"
HUATUO_BAMAI_PODS_API="${HUATUO_BAMAI_ADDR}/containers/json"
WAIT_HUATUO_BAMAI_TIMEOUT=120 # second
WAIT_HUATUO_BAMAI_INTERVAL=2  # second

# k8s: metadata.name == pod-name, ct-hostname
# huatuo-bamai: name == ct-hostname, hostname == pod-name == metadata.name
BUSINESS_POD_NS="default"
BUSINESS_POD_IMAGE="busybox:1.36.1"
BUSINESS_DEFAULT_POD_NAME="busybox-business"
BUSINESS_DEFAULT_POD_NAME_REGEX="^${BUSINESS_DEFAULT_POD_NAME}$"
BUSINESS_DEFAULT_POD_COUNT=1 # default only one pod
BUSINESS_E2E_TEST_POD_NAME="business-e2e-test"
BUSINESS_E2E_TEST_POD_NAME_REGEX="^${BUSINESS_E2E_TEST_POD_NAME}-([0-9]+)?$"
BUSINESS_E2E_TEST_POD_LABEL="app=${BUSINESS_E2E_TEST_POD_NAME}"
BUSINESS_E2E_TEST_POD_COUNT=2

KUBELET_PODS_API="https://127.0.0.1:10250/pods"
KUBELET_CERT="/var/lib/kubelet/pki/kubelet-client-current.pem"
KUBELET_KEY="/var/lib/kubelet/pki/kubelet-client-current.pem"

CURL_TIMEOUT=(
	"--connect-timeout" "2"
	"--max-time" "3"
)
