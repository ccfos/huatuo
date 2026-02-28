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

export TEST_LOG_TAG="E2E TEST"

source ${ROOT_DIR}/integration/lib.sh

# k8s
k8s_create_pod() {
	local ns=$1
	local name=$2
	local image=$3
	local label=$4
	local num=$5

	for i in $(seq 1 ${num}); do
		kubectl run "${name}-${i}" \
			-n ${ns} \
			--image=${image} \
			--restart=Never \
			-l ${label} \
			-- sleep infinity
	done
}

k8s_delete_pod() {
	local ns=$1
	local label=$2
	kubectl delete pod --namespace "$ns" -l "$label"
}

# kubelet
kubelet_pods_json() {
	curl -sk "${CURL_TIMEOUT[@]}" \
		--cert ${KUBELET_CERT} \
		--key ${KUBELET_KEY} \
		--header "Content-Type: application/json" \
		${KUBELET_PODS_API}
}

kubelet_pod_count() {
	local ns=$1
	local regex=$2
	kubelet_pods_json |
		jq --arg ns "$ns" --arg re "$regex" '
        [ .items[]
          | select(.metadata.namespace == $ns)
          | select(.metadata.name | test($re))
          | select(.status.phase == "Running")
        ] | length
        ' 2>/dev/null || echo 0
}

assert_kubelet_pod_count() {
	local ns=$1 regex=$2 expect=$3 desc=${4:-"kubelet pod count"}

	_assert() {
		local actual
		actual="$(kubelet_pod_count "$ns" "$regex")"
		assert_eq "$actual" "$expect" "$desc"
	}

	wait_until \
		"$((WAIT_HUATUO_BAMAI_TIMEOUT / 2))" \
		"${WAIT_HUATUO_BAMAI_INTERVAL}" \
		"$desc" \
		_assert
}

assert_huatuo_bamai_pod_count() {
	local regex=$1 expect=$2 desc=${3:-"huatuo-bamai pod count"}
	_assert() {
		local actual
		actual="$(huatuo_bamai_pod_count "$regex")"
		assert_eq "$actual" "$expect" "$desc"
	}

	wait_until \
		"$((WAIT_HUATUO_BAMAI_TIMEOUT / 2))" \
		"${WAIT_HUATUO_BAMAI_INTERVAL}" \
		"$desc" \
		_assert
}

e2e_test_teardown() {
	local code=$1

	huatuo_bamai_stop || true
	huatuo_bamai_log_check || true

	if [[ $code -ne 0 ]]; then
		fatal "❌ e2e test failed with exit code: $code"
	fi
}
