#!/usr/bin/env bash
# Copyright 2026 The HuaTuo Authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Exercise an existing development stack; Kubernetes owns the temporary workload.
set -euo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
readonly ROOT_DIR
source "${ROOT_DIR}/build/docker/lib_profiling.sh"
readonly WORKLOAD_IMAGE='gcc:14'
readonly DURATION_SECONDS=20
readonly PROFILING_SCOPE_HOST='host'
readonly PROFILING_SCOPE_CONTAINER='container'
readonly API_BASE='http://127.0.0.1:12740'
readonly API_TOKEN='huatuo-dev-api-token'
readonly NODE_BASE='http://127.0.0.1:19704'
readonly NODE_TOKEN='huatuo-dev-node-token'
readonly GRAFANA_BASE='http://127.0.0.1:3000'
readonly GRAFANA_AUTH='admin:admin'
readonly TARGET_HOST='huatuo-dev-node'
readonly REGION='example'
readonly KUBECTL=(kubectl --request-timeout=10s)

scope=${1:-all}
if (($# > 1)) || [[ "$scope" != all && "$scope" != "$PROFILING_SCOPE_HOST" && "$scope" != "$PROFILING_SCOPE_CONTAINER" ]]; then
	echo "Usage: run_test_profiling.sh [all|host|container] after make compose-dev-up." >&2
	exit 2
fi
require_commands curl jq

run_scope() {
	local scope=$1 output_dir=$2
	local container_id namespace job_id node pod_file
	container_id=''
	if [[ "$scope" == "$PROFILING_SCOPE_CONTAINER" ]]; then
		node=$(require_local_kubernetes_node "$output_dir") || return 1
		namespace="huatuo-profiling-${run_dir##*.}"
		namespace=${namespace,,}
		"${KUBECTL[@]}" create namespace "$namespace" >> "${output_dir}/kubernetes.log" || return 1
		workload_namespace=$namespace
		"${KUBECTL[@]}" -n "$namespace" create configmap workload \
			--from-file="workload.c=${ROOT_DIR}/integration/testdata/test_apiserver_profiling_workload.c" \
			>> "${output_dir}/kubernetes.log" || return 1
		jq -n --arg node "$node" --arg image "$WORKLOAD_IMAGE" '{
   apiVersion:"v1",kind:"Pod",metadata:{name:"workload"},
   spec:{nodeName:$node,restartPolicy:"Never",terminationGracePeriodSeconds:2,
    containers:[{name:"workload",image:$image,imagePullPolicy:"IfNotPresent",
     command:["sh","-c","gcc -O1 -g -pthread -fno-omit-frame-pointer -fno-optimize-sibling-calls /src/workload.c -o /tmp/workload && exec /tmp/workload --label-workers /tmp/workload.ready"],
     resources:{requests:{cpu:"100m",memory:"32Mi"},limits:{cpu:"2",memory:"256Mi"}},
     readinessProbe:{exec:{command:["test","-s","/tmp/workload.ready"]},periodSeconds:1},
     volumeMounts:[{name:"source",mountPath:"/src",readOnly:true}]}],
    volumes:[{name:"source",configMap:{name:"workload"}}]}
  }' > "${output_dir}/workload.request.json" || return 1
		kubernetes_pod_create "$output_dir" "$namespace" "${output_dir}/workload.request.json" || return 1
		pod_file=$(kubernetes_pod_wait_ready "$output_dir" "$namespace" workload) || return 1
		kubernetes_pod_container_get "$pod_file" workload > "${output_dir}/target.json" || return 1
		container_id=$(jq -er .container_id "${output_dir}/target.json") || return 1
		node_container_wait_discovered "$output_dir" "$NODE_BASE" "$NODE_TOKEN" "$container_id" || return 1
	fi

	jq -n --arg scope "$scope" --arg container "$container_id" --arg host "$TARGET_HOST" --argjson duration "$DURATION_SECONDS" '
 {type:"cpu",language:"c",mode:"oncpu",scope:$scope,hostname:$host,duration_seconds:$duration}
 + (if $scope=="container" then {container_id:$container} else {} end)' > "${output_dir}/create.request.json" || return 1
	job_id=$(profiling_task_create "$output_dir" "$API_BASE" "$API_TOKEN" "${output_dir}/create.request.json" | jq -er .request_id) || return 1
	profiling_task_wait_completed "$output_dir" "$API_BASE" "$API_TOKEN" "$job_id" "$((DURATION_SECONDS + 120))" > "${output_dir}/task.json" || return 1
	profiling_raw_query "$output_dir" "$API_BASE" "$API_TOKEN" "$job_id" > "${output_dir}/raw.json" || return 1
	if [[ $scope == container ]]; then
		jq -e --arg container "$container_id" '.items | length > 0 and all(.[]; .container_id == $container)' "${output_dir}/raw.json" > /dev/null || return 1
	fi
	jq '{request_id,hostname,scope,container_id,time_range:{
 from_ms:(.created_at|sub("\\.[0-9]+Z$";"Z")|fromdateiso8601*1000),
 to_ms:(.ended_at|sub("\\.[0-9]+Z$";"Z")|(fromdateiso8601+1)*1000)}}' "${output_dir}/task.json" > "${output_dir}/profile.json" || return 1
	grafana_flamegraph_query "$output_dir" "$GRAFANA_BASE" "$GRAFANA_AUTH" "$REGION" "${output_dir}/profile.json" > "${output_dir}/flamegraph.json" || return 1
	local nodes
	nodes=$(jq -er '[.frames[]?.data.values[0]|length]|add // 0' "${output_dir}/flamegraph.json") || return 1
	if ((nodes == 0)); then
		printf 'No flame graph nodes; inspect %s/raw.response.json and the target workload.\n' "$output_dir" >&2
		[[ $scope == host ]] || return 1
	fi
	jq --arg output "$output_dir" --slurpfile raw "${output_dir}/raw.json" --argjson nodes "$nodes" '
 . + {status:"passed",output_dir:$output,raw_windows:($raw[0].items|length),flamegraph_nodes:$nodes}' "${output_dir}/profile.json"

}

scope_results_write() {
	local output=$1 elapsed=$2 status=$3 range file url
	shift 3
	range=$(jq -s '[.[]|select(.status=="passed")|.time_range] | {from_ms:(map(.from_ms)|min),to_ms:(map(.to_ms)|max)}' "$@") || return 1
	for file in "$@"; do
		if jq -e '.status=="passed"' "$file" > /dev/null; then
			jq --argjson range "$range" '.time_range=$range' "$file" > "${file}.tmp" || return 1
			url=$(grafana_profiling_url_build "$GRAFANA_BASE" "$REGION" "${file}.tmp") || return 1
			jq --arg url "$url" '.grafana_url=$url' "${file}.tmp" > "$file" || return 1
		fi
	done
	jq -s --arg output "$output" --argjson elapsed "$elapsed" --argjson status "$status" '
 {status:(if $status==0 and all(.[];.status=="passed") then "passed" else "failed" end),
 elapsed_seconds:$elapsed,output_dir:$output,results:.}' "$@"
}

printf 'Starting CPU profiling (%ss per scope)...\n' "$DURATION_SECONDS" >&2
started=$SECONDS
mkdir -p -- "${ROOT_DIR}/_output/profiling"
run_dir=$(mktemp -d "${ROOT_DIR}/_output/profiling/run.XXXXXX")
workload_namespace=''
cleanup() {
	if [[ -n "$workload_namespace" ]]; then
		"${KUBECTL[@]}" -n "$workload_namespace" get pod workload -o json > "${run_dir}/${PROFILING_SCOPE_CONTAINER}/pod-at-exit.json" 2> /dev/null || true
		"${KUBECTL[@]}" -n "$workload_namespace" logs workload > "${run_dir}/${PROFILING_SCOPE_CONTAINER}/workload.log" 2>&1 || true
		"${KUBECTL[@]}" delete namespace "$workload_namespace" --wait=false > "${run_dir}/cleanup.log" 2>&1 || {
			cat "${run_dir}/cleanup.log" >&2
			return 1
		}
		workload_namespace=''
	fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

case $scope in
all) scopes=("$PROFILING_SCOPE_HOST" "$PROFILING_SCOPE_CONTAINER") ;;
host) scopes=("$PROFILING_SCOPE_HOST") ;;
container) scopes=("$PROFILING_SCOPE_CONTAINER") ;;
esac
result=0
files=()
for target in "${scopes[@]}"; do
	directory="${run_dir}/${target}"
	mkdir -p -- "$directory"
	files+=("${directory}/result.json")
	if ! run_scope "$target" "$directory" > "${directory}/result.json" 2> "${directory}/error.log"; then
		result=1
		jq -n --arg scope "$target" --arg output "$directory" --rawfile error "${directory}/error.log" \
			'{scope:$scope,status:"failed",output_dir:$output,error:($error | rtrimstr("\n"))}' > "${directory}/result.json"
	fi
done
if ! cleanup; then result=1; fi
# Use one server-time range so switching Scope keeps both tasks selectable.
scope_results_write "$run_dir" "$((SECONDS - started))" "$result" "${files[@]}" > "${run_dir}/result.json"
jq . "${run_dir}/result.json"
exit "$result"
