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
	kubelet_pods_json \
		| jq --arg ns "$ns" --arg re "$regex" '
        [ .items[]
          | select(.metadata.namespace == $ns)
          | select(.metadata.name | test($re))
          | select(.status.phase == "Running")
        ] | length
        ' 2> /dev/null || echo 0
}

kubelet_container_ids() {
	local ns=$1
	local regex=$2
	kubelet_pods_json \
		| jq -r --arg ns "$ns" --arg re "$regex" '
        .items[]
        | select(.metadata.namespace == $ns)
        | select(.metadata.name | test($re))
        | select(.status.phase == "Running")
        | .status.containerStatuses[]?.containerID // empty
        | sub("^[^:]+://"; "")
      ' 2> /dev/null
}

assert_kubelet_pod_count() {
	local ns=$1 regex=$2 expect=$3 desc=${4:-"kubelet pod count"}

	_assert() {
		local actual
		actual="$(kubelet_pod_count "$ns" "$regex")"
		assert_eq "$actual" "$expect" "$desc"
	}

	if ! wait_until \
		"$((WAIT_HUATUO_BAMAI_TIMEOUT / 2))" \
		"${WAIT_HUATUO_BAMAI_INTERVAL}" \
		_assert; then
		# wait timeout, dump pods from kubelet
		kubelet_pods_json

		fatal "❌ wait timeout, kubelet pod count not expected"
	fi
}

huatuo_bamai_containers_present() {
	local ns=$1 regex=$2 expect=$3
	local -a container_ids=()
	local container_id

	mapfile -t container_ids < <(kubelet_container_ids "$ns" "$regex")
	[[ ${#container_ids[@]} -eq ${expect} ]] || return 1
	for container_id in "${container_ids[@]}"; do
		curl -sf "${CURL_TIMEOUT[@]}" \
			"${HUATUO_BAMAI_ADDR}/v1/containers/${container_id}" \
			| jq -e --arg id "${container_id}" '.data.id == $id' > /dev/null \
			|| return 1
	done
}

assert_huatuo_bamai_containers_present() {
	local ns=$1 regex=$2 expect=$3 desc=${4:-"huatuo-bamai containers present"}

	wait_until \
		"$((WAIT_HUATUO_BAMAI_TIMEOUT / 2))" \
		"${WAIT_HUATUO_BAMAI_INTERVAL}" \
		huatuo_bamai_containers_present "$ns" "$regex" "$expect" \
		|| fatal "${desc}: matching container metadata did not become available"
}

huatuo_bamai_containers_absent() {
	local container_id status
	for container_id in "$@"; do
		status=$(curl -sS "${CURL_TIMEOUT[@]}" \
			-o /dev/null -w '%{http_code}' \
			"${HUATUO_BAMAI_ADDR}/v1/containers/${container_id}") \
			|| return 1
		[[ ${status} == "404" ]] || return 1
	done
}

assert_huatuo_bamai_containers_absent() {
	local desc=$1
	shift

	wait_until \
		"$((WAIT_HUATUO_BAMAI_TIMEOUT / 2))" \
		"${WAIT_HUATUO_BAMAI_INTERVAL}" \
		huatuo_bamai_containers_absent "$@" \
		|| fatal "${desc}: stale container metadata remained available"
}

e2e_test_teardown() {
	local code=$1

	huatuo_bamai_stop "${code}" || true
	if ! huatuo_bamai_log_check; then
		log_error "❌ huatuo-bamai log check failed"
		code=1
	fi

	if [ $code -ne 0 ]; then
		fatal "❌ e2e test failed with exit code: $code"
	fi
}
