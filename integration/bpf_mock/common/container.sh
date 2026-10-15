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
    nodeName: $node, restartPolicy: "Never",
    containers: [{
     name: "trigger", image: $image, imagePullPolicy: "Never",
     command: ["sleep", "infinity"],
     securityContext: {privileged: true, runAsUser: 0},
     resources: {requests: {memory: "16Mi", cpu: "10m"}, limits: {memory: "64Mi", cpu: "1"}},
     volumeMounts: [
      {name: "trigger", mountPath: "/mock", readOnly: true},
      {name: "device", mountPath: $device, readOnly: true}
     ]
    }],
    volumes: [
     {name: "trigger", hostPath: {path: $trigger_dir, type: "Directory"}},
     {name: "device", hostPath: {path: $device, type: "CharDevice"}}
    ]
   }
  }'
}
