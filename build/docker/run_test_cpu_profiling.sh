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
readonly CPU_PROFILER_WORKLOAD_IMAGE='gcc:14'
readonly CPU_PROFILER_DURATION_SECONDS=20
readonly CPU_PROFILER_SCOPE_HOST='host'
readonly CPU_PROFILER_SCOPE_CONTAINER='container'
readonly CPU_PROFILER_API_BASE='http://127.0.0.1:12740'
readonly CPU_PROFILER_API_TOKEN='huatuo-dev-api-token'
readonly CPU_PROFILER_NODE_BASE='http://127.0.0.1:19704'
readonly CPU_PROFILER_NODE_TOKEN='huatuo-dev-node-token'
readonly CPU_PROFILER_GRAFANA_BASE='http://127.0.0.1:3000'
readonly CPU_PROFILER_GRAFANA_AUTH='admin:admin'
readonly CPU_PROFILER_TARGET_HOST='huatuo-dev-node'
readonly CPU_PROFILER_REGION='pre-test'
readonly CPU_PROFILER_KUBECTL=(kubectl --request-timeout=10s)

usage() {
	printf 'Usage: run_test_cpu_profiling.sh [--with-host] [--with-container] [--oncpu] [--offcpu]\nRun make compose-dev-up first. Defaults: both scopes, oncpu, 20 seconds per scope and mode.\n'
}

cpu_profiler_with_host=false
cpu_profiler_with_container=false
cpu_profiler_oncpu=false
cpu_profiler_offcpu=false
while (($#)); do
	case $1 in
	--with-host) cpu_profiler_with_host=true ;;
	--with-container) cpu_profiler_with_container=true ;;
	--oncpu) cpu_profiler_oncpu=true ;;
	--offcpu) cpu_profiler_offcpu=true ;;
	--help | -h)
		usage
		exit 0
		;;
	*)
		printf 'Unknown argument: %s\n' "$1" >&2
		usage >&2
		exit 2
		;;
	esac
	shift
done
if [[ $cpu_profiler_with_host == false && $cpu_profiler_with_container == false ]]; then
	cpu_profiler_with_host=true
	cpu_profiler_with_container=true
fi
if [[ $cpu_profiler_oncpu == false && $cpu_profiler_offcpu == false ]]; then
	cpu_profiler_oncpu=true
fi
cpu_profiler_scopes=()
if [[ $cpu_profiler_with_host == true ]]; then cpu_profiler_scopes+=("$CPU_PROFILER_SCOPE_HOST"); fi
if [[ $cpu_profiler_with_container == true ]]; then cpu_profiler_scopes+=("$CPU_PROFILER_SCOPE_CONTAINER"); fi
cpu_profiler_modes=()
if [[ $cpu_profiler_oncpu == true ]]; then cpu_profiler_modes+=(oncpu); fi
if [[ $cpu_profiler_offcpu == true ]]; then cpu_profiler_modes+=(offcpu); fi
require_commands curl jq

assert_cpu_profile() {
	local output_dir=$1 scope=$2 container_id=$3 mode=$4 profile_type=$5
	jq -e --arg type "$profile_type" \
		'.items | length > 0 and all(.[]; .profile_type == $type)' "${output_dir}/raw.json" > /dev/null || {
		printf 'Expected %s profile windows; inspect %s/raw.json.\n' "$profile_type" "$output_dir" >&2
		return 1
	}
	if [[ $mode == offcpu ]]; then
		jq -e 'any(.items[]; .profile as $p | any($p.sample[]?.label[]?; $p.string_table[.key] == "offcpu_reason"))' \
			"${output_dir}/raw.json" > /dev/null || {
			printf 'Expected offcpu_reason sample labels; inspect %s/raw.json.\n' "$output_dir" >&2
			return 1
		}
	fi
	if [[ $scope == container ]]; then
		jq -e --arg container "$container_id" '.items | length > 0 and all(.[]; .container_id == $container)' "${output_dir}/raw.json" > /dev/null || return 1
	fi
}

run_scope() {
	local scope=$1 mode=$2 output_dir=$3
	local container_id namespace job_id node pod_file profile_type
	case $mode in
	oncpu) profile_type='process_cpu:cpu:nanoseconds:cpu:nanoseconds' ;;
	offcpu) profile_type='process_offcpu:offcpu:nanoseconds:offcpu:nanoseconds' ;;
	esac
	require_endpoints_ready "$output_dir" "$CPU_PROFILER_NODE_BASE" "$CPU_PROFILER_API_BASE" || return 1
	container_id=''
	if [[ "$scope" == "$CPU_PROFILER_SCOPE_CONTAINER" ]]; then
		node=$(require_local_kubernetes_node "$output_dir") || return 1
		namespace="huatuo-profiling-${cpu_profiler_run_dir##*.}-${mode}"
		namespace=${namespace,,}
		"${CPU_PROFILER_KUBECTL[@]}" create namespace "$namespace" >> "${output_dir}/kubernetes.log" || return 1
		cpu_profiler_workload_namespace=$namespace
		"${CPU_PROFILER_KUBECTL[@]}" -n "$namespace" create configmap workload \
			--from-file="workload.c=${ROOT_DIR}/integration/testdata/test_apiserver_profiling_workload.c" \
			>> "${output_dir}/kubernetes.log" || return 1
		jq -n --arg node "$node" --arg image "$CPU_PROFILER_WORKLOAD_IMAGE" '{
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
		node_container_wait_discovered "$output_dir" "$CPU_PROFILER_NODE_BASE" "$CPU_PROFILER_NODE_TOKEN" "$container_id" || return 1
	fi

	jq -n --arg scope "$scope" --arg container "$container_id" --arg host "$CPU_PROFILER_TARGET_HOST" --argjson duration "$CPU_PROFILER_DURATION_SECONDS" --arg mode "$mode" '
 {type:"cpu",language:"c",mode:$mode,scope:$scope,hostname:$host,duration_seconds:$duration}
 + (if $scope=="container" then {container_id:$container} else {} end)' > "${output_dir}/create.request.json" || return 1
	job_id=$(profiling_task_create "$output_dir" "$CPU_PROFILER_API_BASE" "$CPU_PROFILER_API_TOKEN" "${output_dir}/create.request.json" | jq -er .request_id) || return 1
	profiling_task_wait_completed "$output_dir" "$CPU_PROFILER_API_BASE" "$CPU_PROFILER_API_TOKEN" "$job_id" "$((CPU_PROFILER_DURATION_SECONDS + 120))" > "${output_dir}/task.json" || return 1
	profiling_raw_query "$output_dir" "$CPU_PROFILER_API_BASE" "$CPU_PROFILER_API_TOKEN" "$job_id" > "${output_dir}/raw.json" || return 1
	assert_cpu_profile "$output_dir" "$scope" "$container_id" "$mode" "$profile_type" || return 1

	jq --arg type "$profile_type" '{request_id,hostname,scope,container_id,mode,profile_type:$type,time_range:{
 from_ms:(.created_at|sub("\\.[0-9]+Z$";"Z")|fromdateiso8601*1000),
 to_ms:(.ended_at|sub("\\.[0-9]+Z$";"Z")|(fromdateiso8601+1)*1000)}}' "${output_dir}/task.json" > "${output_dir}/profile.json" || return 1
	grafana_flamegraph_query "$output_dir" "$CPU_PROFILER_GRAFANA_BASE" "$CPU_PROFILER_GRAFANA_AUTH" "$CPU_PROFILER_REGION" "${output_dir}/profile.json" > "${output_dir}/flamegraph.json" || return 1
	local nodes
	nodes=$(jq -er '[.frames[]?.data.values[0]|length]|add // 0' "${output_dir}/flamegraph.json") || return 1
	if ((nodes == 0)); then
		printf 'No flame graph nodes; inspect %s/raw.response.json and the target workload.\n' "$output_dir" >&2
		[[ $scope == host && $mode == oncpu ]] || return 1
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
			url=$(grafana_profiling_url_build "$CPU_PROFILER_GRAFANA_BASE" "$CPU_PROFILER_REGION" "${file}.tmp") || return 1
			jq --arg url "$url" '.grafana_url=$url' "${file}.tmp" > "$file" || return 1
		fi
	done
	jq -s --arg output "$output" --argjson elapsed "$elapsed" --argjson status "$status" '
 {status:(if $status==0 and all(.[];.status=="passed") then "passed" else "failed" end),
 elapsed_seconds:$elapsed,output_dir:$output,results:.}' "$@"
}

printf 'Starting CPU profiling (%ss per scope and mode)...\n' "$CPU_PROFILER_DURATION_SECONDS" >&2
cpu_profiler_started=$SECONDS
mkdir -p -- "${ROOT_DIR}/_output/profiling"
cpu_profiler_run_dir=$(mktemp -d "${ROOT_DIR}/_output/profiling/run.XXXXXX")
cpu_profiler_workload_namespace=''
cleanup() {
	if [[ -n "$cpu_profiler_workload_namespace" ]]; then
		"${CPU_PROFILER_KUBECTL[@]}" -n "$cpu_profiler_workload_namespace" get pod workload -o json > "${cpu_profiler_directory}/pod-at-exit.json" 2> /dev/null || true
		"${CPU_PROFILER_KUBECTL[@]}" -n "$cpu_profiler_workload_namespace" logs workload > "${cpu_profiler_directory}/workload.log" 2>&1 || true
		"${CPU_PROFILER_KUBECTL[@]}" delete namespace "$cpu_profiler_workload_namespace" --wait=false > "${cpu_profiler_directory}/cleanup.log" 2>&1 || {
			cat "${cpu_profiler_directory}/cleanup.log" >&2
			return 1
		}
		cpu_profiler_workload_namespace=''
	fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

cpu_profiler_result=0
cpu_profiler_files=()
for cpu_profiler_target in "${cpu_profiler_scopes[@]}"; do
	for cpu_profiler_mode in "${cpu_profiler_modes[@]}"; do
		cpu_profiler_directory="${cpu_profiler_run_dir}/${cpu_profiler_target}/${cpu_profiler_mode}"
		mkdir -p -- "$cpu_profiler_directory"
		cpu_profiler_files+=("${cpu_profiler_directory}/result.json")
		if ! run_scope "$cpu_profiler_target" "$cpu_profiler_mode" "$cpu_profiler_directory" > "${cpu_profiler_directory}/result.json" 2> "${cpu_profiler_directory}/error.log"; then
			cpu_profiler_result=1
			jq -n --arg scope "$cpu_profiler_target" --arg mode "$cpu_profiler_mode" --arg output "$cpu_profiler_directory" --rawfile error "${cpu_profiler_directory}/error.log" \
				'{scope:$scope,mode:$mode,status:"failed",output_dir:$output,error:($error | rtrimstr("\n"))}' > "${cpu_profiler_directory}/result.json"
		fi
		if ! cleanup; then
			cpu_profiler_result=1
			break 2
		fi
	done
done
# Use one server-time range so switching Scope or Profile type keeps tasks selectable.
scope_results_write "$cpu_profiler_run_dir" "$((SECONDS - cpu_profiler_started))" "$cpu_profiler_result" "${cpu_profiler_files[@]}" > "${cpu_profiler_run_dir}/result.json"
jq . "${cpu_profiler_run_dir}/result.json"
exit "$cpu_profiler_result"
