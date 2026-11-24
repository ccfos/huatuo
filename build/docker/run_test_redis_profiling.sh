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

# Mixed SET/GET traffic exercises command processing without persistence.
set -euo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
readonly ROOT_DIR
source "${ROOT_DIR}/build/docker/lib_profiling.sh"
readonly REDIS_IMAGE='redis:8.12.0-trixie'
readonly REDIS_MEMTIER_IMAGE='redislabs/memtier_benchmark:2.6.1'
readonly REDIS_DURATION=30
readonly REDIS_API_BASE='http://127.0.0.1:12740'
readonly REDIS_API_TOKEN='huatuo-dev-api-token'
readonly REDIS_NODE_BASE='http://127.0.0.1:19704'
readonly REDIS_NODE_TOKEN='huatuo-dev-node-token'
readonly REDIS_GRAFANA_BASE='http://127.0.0.1:3000'
readonly REDIS_GRAFANA_AUTH='admin:admin'
readonly REDIS_TARGET_HOST='huatuo-dev-node'
readonly REDIS_REGION='example'
readonly REDIS_CRI_ENDPOINT=${CONTAINER_RUNTIME_ENDPOINT:-unix:///run/containerd/containerd.sock}

write_config_redis_pod() {
	jq -n --arg node "$1" --arg image "$REDIS_IMAGE" '{
 apiVersion:"v1",kind:"Pod",metadata:{name:"redis"},
 spec:{nodeName:$node,restartPolicy:"Never",terminationGracePeriodSeconds:5,
 containers:[{name:"redis",image:$image,imagePullPolicy:"Never",
 args:["redis-server","--save","","--appendonly","no"],
 resources:{requests:{cpu:"100m",memory:"64Mi"},limits:{cpu:"2",memory:"512Mi"}},
 readinessProbe:{exec:{command:["redis-cli","ping"]},periodSeconds:2}}]}}
 '
}

write_config_memtier_pod() {
	jq -n --arg node "$1" --arg address "$2" --arg image "$REDIS_MEMTIER_IMAGE" '{
 apiVersion:"v1",kind:"Pod",metadata:{name:"memtier"},
 spec:{nodeName:$node,restartPolicy:"Never",terminationGracePeriodSeconds:2,
 containers:[{name:"memtier",image:$image,imagePullPolicy:"Never",
 env:[{name:"REDIS_HOST",value:$address}],command:["sh","-ec"],args:[
 "while :; do
   memtier_benchmark --server=\"$REDIS_HOST\" --port=6379 --protocol=redis --threads=2 --clients=4 --ratio=1:4 --data-size=256 --key-minimum=1 --key-maximum=10000 --pipeline=8 --test-time=10 > /tmp/batch.log 2>&1 || { cat /tmp/batch.log; exit 1; }
   cat /tmp/batch.log
   grep -Eq \"^Totals[[:space:]]+[1-9][0-9]*\" /tmp/batch.log
   echo completed >> /tmp/completed
 done"],
 resources:{requests:{cpu:"100m",memory:"64Mi"},limits:{cpu:"2",memory:"512Mi"}},
 readinessProbe:{exec:{command:["test","-s","/tmp/completed"]},periodSeconds:2}}]}}
 '
}

redis_start() {
	local output_dir=$1 namespace=$2 node=$3 pod_file
	write_config_redis_pod "$node" > "${output_dir}/service.request.json"
	kubernetes_pod_create "$output_dir" "$namespace" "${output_dir}/service.request.json"
	pod_file=$(kubernetes_pod_wait_ready "$output_dir" "$namespace" redis)
	kubernetes_pod_container_get "$pod_file" redis > "${output_dir}/target.json"
}

memtier_start() {
	local output_dir=$1 namespace=$2 node=$3 address=$4
	write_config_memtier_pod "$node" "$address" > "${output_dir}/benchmark.request.json"
	kubernetes_pod_create "$output_dir" "$namespace" "${output_dir}/benchmark.request.json"
	kubernetes_pod_wait_ready "$output_dir" "$namespace" memtier > /dev/null
}

assert_memtier_progress() {
	local output_dir=$1 namespace=$2 previous=${3:-0} batches
	kubectl --request-timeout=10s -n "$namespace" get pod memtier -o json > "${output_dir}/benchmark.response.json" || return 1
	kubectl --request-timeout=10s -n "$namespace" logs memtier > "${output_dir}/benchmark.log" || return 1
	jq -e '.status.phase == "Running" and all(.status.containerStatuses[]; .ready and .restartCount == 0)' \
		"${output_dir}/benchmark.response.json" > /dev/null || {
		printf 'Expected running memtier without restarts; inspect %s/benchmark.response.json and benchmark.log.\n' "$output_dir" >&2
		return 1
	}
	batches=$(kubectl --request-timeout=10s -n "$namespace" exec memtier -- sh -c 'wc -l < /tmp/completed') || return 1
	if [[ ! $batches =~ ^[0-9]+$ ]] || ((batches <= previous)); then
		printf 'Expected memtier batches above %s, got %s; inspect %s/benchmark.log.\n' "$previous" "$batches" "$output_dir" >&2
		return 1
	fi
	printf '%s\n' "$batches"
}

memtier_stop() {
	local output_dir=$1 namespace=$2
	kubectl --request-timeout=10s -n "$namespace" get pod memtier -o json > "${output_dir}/benchmark-at-exit.json"
	redis_benchmark_saved=true
	kubernetes_pod_delete "$output_dir" "$namespace" memtier
}

assert_redis_profile() {
	local output_dir=$1 container_id=$2
	jq -e --arg container "$container_id" '.items | length > 0 and all(.[]; .container_id == $container)' \
		"${output_dir}/raw.json" > /dev/null || {
		printf 'Expected windows from container %s; inspect %s/raw.json.\n' "$container_id" "$output_dir" >&2
		return 1
	}
	jq -e '([.frames[]?.data.values[0] | length] | add // 0) > 0' "${output_dir}/flamegraph.json" > /dev/null || {
		printf 'Expected a nonempty redis flame graph; inspect %s/flamegraph.response.json.\n' "$output_dir" >&2
		return 1
	}
}

redis_result_write() {
	local output_dir=$1 status=$2 job_id=$3 elapsed=$4 url='' profile=/dev/null raw=/dev/null graph=/dev/null stage=$5
	if ((status == 0)); then
		profile="${output_dir}/profile.json"
		raw="${output_dir}/raw.json"
		graph="${output_dir}/flamegraph.json"
		url=$(grafana_profiling_url_build "$REDIS_GRAFANA_BASE" "$REDIS_REGION" "$profile") || return 1
	fi
	jq -n --argjson status "$status" --arg url "$url" --arg id "$job_id" --arg stage "$stage" \
		--arg image "$REDIS_IMAGE" --arg tool "$REDIS_MEMTIER_IMAGE" --argjson duration "$REDIS_DURATION" \
		--argjson elapsed "$elapsed" --arg output "$output_dir" \
		--rawfile error "${output_dir}/stderr.log" --slurpfile profile "$profile" --slurpfile raw "$raw" --slurpfile graph "$graph" '
 {scope:"container",service:"redis",service_image:$image,benchmark_image:$tool,duration_seconds:$duration,
 elapsed_seconds:$elapsed,output_dir:$output,benchmark_log:($output+"/benchmark.log"),request_id:$id,grafana_url:null}
 + (if $status==0 then $profile[0] + {status:"passed",grafana_url:$url,raw_windows:($raw[0].items|length),
     flamegraph_nodes:([$graph[0].frames[]?.data.values[0]|length]|add // 0)}
    else {status:"failed",error:($stage+" failed; inspect "+$output+"/*.log. "+($error|rtrimstr("\n")))} end)'
}

cleanup() {
	local status=$? pod log_name
	trap - EXIT INT TERM
	set +e
	if [[ -n $redis_job_id && $status != 0 ]] && ! jq -e '.data.status == "terminal"' \
		"${redis_run_dir}/status.response.json" > /dev/null 2>&1; then
		profiling_task_stop "$redis_run_dir" "$REDIS_API_BASE" "$REDIS_API_TOKEN" "$redis_job_id" >> "${redis_run_dir}/cleanup.log" 2>&1
	fi
	if [[ -n $redis_namespace ]]; then
		for pod in redis memtier; do
			[[ $pod != memtier || $redis_benchmark_saved != true ]] || continue
			log_name=$pod
			[[ $pod != memtier ]] || log_name=benchmark
			kubectl --request-timeout=10s -n "$redis_namespace" logs "$pod" > "${redis_run_dir}/${log_name}.log" 2>&1
			kubectl --request-timeout=10s -n "$redis_namespace" get pod "$pod" -o json > "${redis_run_dir}/${pod}-at-exit.json" 2> /dev/null
		done
		if ! kubectl --request-timeout=10s delete namespace "$redis_namespace" --wait=false >> "${redis_run_dir}/cleanup.log" 2>&1; then
			status=1
			printf 'Failed to remove namespace %s; inspect cleanup.log.\n' "$redis_namespace" >> "${redis_run_dir}/stderr.log"
		fi
	fi
	if ! redis_result_write "$redis_run_dir" "$status" "$redis_job_id" "$((SECONDS - redis_started))" "$redis_stage" > "${redis_run_dir}/result.json"; then
		status=1
		printf 'Failed to write result to %s/result.json.\n' "$redis_run_dir" >&2
	elif ! cat "${redis_run_dir}/result.json"; then
		status=1
		printf 'Failed to print result from %s/result.json.\n' "$redis_run_dir" >&2
	fi
	((status == 0)) || tail -n 20 "${redis_run_dir}/stderr.log" >&2
	exit "$status"
}

main() {
	local output_dir=$1 node address batches namespace container_id
	require_commands kubectl ip curl crictl
	redis_stage=development-stack
	require_development_stack "$output_dir" "$REDIS_NODE_BASE" "$REDIS_API_BASE"
	node=$(require_local_kubernetes_node "$output_dir")
	redis_stage=image-pull
	cri_images_ensure_present "$output_dir" "$REDIS_CRI_ENDPOINT" "$REDIS_IMAGE" "$REDIS_MEMTIER_IMAGE"
	redis_stage=service-start
	namespace="huatuo-redis-${output_dir##*.}"
	namespace=${namespace,,}
	kubectl --request-timeout=10s create namespace "$namespace" > "${output_dir}/namespace.log"
	# Register ownership only after creation succeeds so EXIT never removes an unrelated namespace.
	redis_namespace=$namespace
	redis_start "$output_dir" "$namespace" "$node"
	container_id=$(jq -er .container_id "${output_dir}/target.json")
	address=$(jq -er .pod_ip "${output_dir}/target.json")
	node_container_wait_discovered "$output_dir" "$REDIS_NODE_BASE" "$REDIS_NODE_TOKEN" "$container_id"
	redis_stage=benchmark
	memtier_start "$output_dir" "$namespace" "$node" "$address"
	batches=$(assert_memtier_progress "$output_dir" "$namespace")
	redis_stage=collection
	jq -n --arg host "$REDIS_TARGET_HOST" --arg container "$container_id" --argjson duration "$REDIS_DURATION" \
		'{type:"cpu",language:"c",mode:"oncpu",scope:"container",hostname:$host,container_id:$container,duration_seconds:$duration}' \
		> "${output_dir}/create.request.json"
	redis_job_id=$(profiling_task_create "$output_dir" "$REDIS_API_BASE" "$REDIS_API_TOKEN" "${output_dir}/create.request.json" | jq -er .request_id)
	profiling_task_wait_completed "$output_dir" "$REDIS_API_BASE" "$REDIS_API_TOKEN" "$redis_job_id" "$((REDIS_DURATION + 120))" > "${output_dir}/task.json"
	assert_memtier_progress "$output_dir" "$namespace" "$batches"
	redis_stage=benchmark-stop
	memtier_stop "$output_dir" "$namespace"
	redis_stage=flamegraph
	profiling_raw_query "$output_dir" "$REDIS_API_BASE" "$REDIS_API_TOKEN" "$redis_job_id" > "${output_dir}/raw.json"
	jq '{request_id,hostname,scope,container_id,time_range:{
   from_ms:(.created_at|sub("\\.[0-9]+Z$";"Z")|fromdateiso8601*1000),
   to_ms:(.ended_at|sub("\\.[0-9]+Z$";"Z")|(fromdateiso8601+1)*1000)}}' \
		"${output_dir}/task.json" > "${output_dir}/profile.json"
	grafana_flamegraph_query "$output_dir" "$REDIS_GRAFANA_BASE" "$REDIS_GRAFANA_AUTH" "$REDIS_REGION" "${output_dir}/profile.json" > "${output_dir}/flamegraph.json"
	assert_redis_profile "$output_dir" "$container_id"
}

if [[ ${BASH_SOURCE[0]} == "$0" ]]; then
	if [[ ${1:-} == --help && $# == 1 ]]; then
		printf 'Usage: run_test_redis_profiling.sh\nRun make compose-dev-up first. Preload images with crictl, benchmark redis, profile its container for 30 seconds, and print JSON with grafana_url.\n'
		exit 0
	fi
	if (($# != 0)); then
		printf 'Usage: run_test_redis_profiling.sh [--help]\n' >&2
		exit 2
	fi
	if ! command -v jq > /dev/null; then
		printf '{"status":"failed","grafana_url":null,"error":"Install jq before running this script."}\n'
		exit 1
	fi
	mkdir -p "${ROOT_DIR}/_output/profiling"
	redis_run_dir=$(mktemp -d "${ROOT_DIR}/_output/profiling/redis.XXXXXX")
	redis_started=$SECONDS redis_stage=dependencies redis_namespace='' redis_job_id='' redis_benchmark_saved=false
	: > "${redis_run_dir}/stderr.log"
	trap cleanup EXIT
	trap 'exit 130' INT
	trap 'exit 143' TERM
	printf 'Profiling redis; logs: %s\n' "$redis_run_dir" >&2
	main "$redis_run_dir" > "${redis_run_dir}/execution.log" 2> "${redis_run_dir}/stderr.log"
fi
