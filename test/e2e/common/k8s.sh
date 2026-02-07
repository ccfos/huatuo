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
