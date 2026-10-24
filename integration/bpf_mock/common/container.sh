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

# Keep pod creation declarative so fixture inputs can be inspected before apply.
bpf_mock_pod_manifest() {
	local namespace=$1 pod=$2 node=$3 image=$4 trigger_dir=$5 device=$6
	jq -n --arg namespace "$namespace" --arg pod "$pod" --arg node "$node" \
		--arg image "$image" --arg trigger_dir "$trigger_dir" --arg device "$device" '
  {
   apiVersion: "v1", kind: "Pod",
   metadata: {name: $pod, namespace: $namespace},
   spec: {
    nodeName: $node, restartPolicy: "Never", terminationGracePeriodSeconds: 1,
    containers: [{
     name: "trigger", image: $image, imagePullPolicy: "Never",
     command: ["sleep", "infinity"],
     securityContext: {privileged: true, runAsUser: 0},
     resources: {requests: {memory: "16Mi", cpu: "10m"}, limits: {memory: "64Mi", cpu: "1"}},
     volumeMounts: [
      {name: "trigger", mountPath: "/mock", readOnly: true},
      {name: "device", mountPath: $device, readOnly: true},
      {name: "host-proc", mountPath: "/host-proc", readOnly: true}
     ]
    }],
    volumes: [
     {name: "trigger", hostPath: {path: $trigger_dir, type: "Directory"}},
     {name: "device", hostPath: {path: $device, type: "CharDevice"}},
     {name: "host-proc", hostPath: {path: "/proc", type: "Directory"}}
    ]
   }
  }'
}

# The caller records namespace ownership before any pod operation can fail.
create_bpf_mock_pod() {
	local namespace=$1 pod=$2 trigger_dir=$3 device=$4
	local node=${BPF_MOCK_NODE:-}
	if [[ -z ${node} ]]; then
		node=$(curl -fsk "${CURL_TIMEOUT[@]}" --cert "${KUBELET_CERT}" --key "${KUBELET_KEY}" \
			"${KUBELET_PODS_API}" | jq -er 'first(.items[].spec.nodeName | select(. != null and . != ""))') \
			|| fatal "cannot determine the local node from kubelet; set BPF_MOCK_NODE"
	fi
	kubectl --request-timeout=10s label namespace "${namespace}" pod-security.kubernetes.io/enforce=privileged || return
	bpf_mock_pod_manifest "${namespace}" "${pod}" "${node}" "${BUSINESS_POD_IMAGE}" \
		"${trigger_dir}" "${device}" > "${HUATUO_BAMAI_TEST_TMPDIR}/pod.json" || return
	kubectl --request-timeout=10s apply -f "${HUATUO_BAMAI_TEST_TMPDIR}/pod.json" || return
	kubectl --request-timeout=130s wait -n "${namespace}" --for=condition=Ready "pod/${pod}" --timeout=120s \
		|| fatal "mock trigger pod ${namespace}/${pod} did not become ready"
}

# One discovery attempt; callers choose their wait policy and failure diagnostics.
read_bpf_mock_container() {
	local namespace=$1 pod=$2 controller=$3 output=$4 id
	id=$(kubectl --request-timeout=5s get pod -n "${namespace}" "${pod}" -o json \
		| jq -er '.status.containerStatuses[] | select(.name == "trigger") | .containerID | sub("^[^:]+://"; "")') || return 1
	[[ ${id} =~ ^[0-9a-f]{64}$ ]] || return 1
	curl -fsS "${CURL_TIMEOUT[@]}" "${HUATUO_BAMAI_ADDR}/v1/containers/${id}" > "${output}" || return 1
	jq -e --arg id "${id}" --arg controller "${controller}" \
		'.data.id == $id and (.data.cgroup_css[$controller] | test("^0x[0-9a-f]{16}$"))
   and .data.cgroup_css[$controller] != "0x0000000000000000"' "${output}" > /dev/null 2>&1
}

verify_bpf_mock_container_alive() {
	local namespace=$1 pod=$2 expected_id=$3
	kubectl --request-timeout=10s get pod -n "${namespace}" "${pod}" -o json \
		| jq -e --arg id "${expected_id}" '
   .status.phase == "Running" and
   (.status.containerStatuses[] | select(.name == "trigger") |
    .ready and .restartCount == 0 and (.containerID | sub("^[^:]+://"; "")) == $id)
  ' > /dev/null
}

describe_bpf_mock_pod() {
	kubectl --request-timeout=10s describe pod -n "$1" "$2" \
		> "${HUATUO_BAMAI_TEST_TMPDIR}/pod-failure.log" 2>&1 || true
}

delete_bpf_mock_namespace() {
	kubectl --request-timeout=10s delete namespace "$1" --wait=true --timeout=60s
}

write_bpf_mock_container_config() {
	local port=$1 kubelet_port=${KUBELET_PODS_API##*:}
	kubelet_port=${kubelet_port%%/*}
	cat >> "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" << EOF

[HTTPServer]
    ListenAddress = "127.0.0.1:${port}"
[Pod]
    KubeletReadOnlyPort = 0
    KubeletAuthorizedPort = ${kubelet_port}
    KubeletClientCertPath = "${KUBELET_CERT},${KUBELET_KEY}"
EOF
}
