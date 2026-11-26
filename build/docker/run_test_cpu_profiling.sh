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

# Exercise an existing development stack with container workloads or host IRQ load.
set -euo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
readonly ROOT_DIR
source "${ROOT_DIR}/build/docker/lib_profiling.sh"
readonly CPU_PROFILER_WORKLOAD_IMAGE='gcc:14'
readonly CPU_PROFILER_DURATION_SECONDS=20
readonly CPU_PROFILER_API_BASE='http://127.0.0.1:12740'
readonly CPU_PROFILER_API_TOKEN='huatuo-dev-api-token'
readonly CPU_PROFILER_NODE_BASE='http://127.0.0.1:19704'
readonly CPU_PROFILER_NODE_TOKEN='huatuo-dev-node-token'
readonly CPU_PROFILER_GRAFANA_BASE='http://127.0.0.1:3000'
readonly CPU_PROFILER_GRAFANA_AUTH='admin:admin'
readonly CPU_PROFILER_TARGET_HOST='huatuo-dev-node'
readonly CPU_PROFILER_REGION='pre-test'

readonly CPU_PROFILER_CRI_ENDPOINT=${CONTAINER_RUNTIME_ENDPOINT:-unix:///run/containerd/containerd.sock}

usage() {
	printf '%s\n' \
		'Usage: run_test_cpu_profiling.sh [MODE...]' \
		'Modes: --oncpu --offcpu --virtual-alloc --physical-alloc --physical-usage --irq' \
		'Default: --oncpu, 20s per mode. Modes can be combined.' \
		'Scope: --irq = host (one CPU); others = container.' \
		'Setup: make compose-dev-up'
}

write_config_cpu_profiler_workload_pod() {
	local node=$1 mode=$2 command readiness
	case $mode in
	oncpu | offcpu)
		command='exec /tmp/workload --label-workers /tmp/workload.ready'
		readiness='test -s /tmp/workload.ready'
		;;
	*)
		# Label workers only exercise CPU; this existing variant also faults and retains pages.
		command='/tmp/workload --cpu-worker 2>&1 | tee /tmp/workload.log'
		readiness='grep -q "^ready$" /tmp/workload.log'
		;;
	esac
	jq -n --arg node "$node" --arg image "$CPU_PROFILER_WORKLOAD_IMAGE" --arg command "$command" --arg readiness "$readiness" '{
 apiVersion:"v1",kind:"Pod",metadata:{name:"workload"},
 spec:{nodeName:$node,restartPolicy:"Never",terminationGracePeriodSeconds:2,
 containers:[{name:"workload",image:$image,imagePullPolicy:"Never",command:["bash","-euo","pipefail","-c"],
 args:["gcc -O1 -g -pthread -fno-omit-frame-pointer -fno-optimize-sibling-calls /src/workload.c -o /tmp/workload\n"+$command],
 resources:{requests:{cpu:"100m",memory:"32Mi"},limits:{cpu:"2",memory:"256Mi"}},
 readinessProbe:{exec:{command:["bash","-ec",$readiness]},periodSeconds:1},
 volumeMounts:[{name:"source",mountPath:"/src",readOnly:true}]}],
 volumes:[{name:"source",configMap:{name:"workload"}}]}}
 '
}

cpu_profiler_result_write() {
	local output_dir=$1 status=$2 elapsed=$3 result='{}' metadata
	metadata=$(profiling_mode_metadata "$cpu_profiler_mode") || return 1
	if ((status == 0)); then result=$(profiling_result_write "$output_dir" "$CPU_PROFILER_GRAFANA_BASE" "$CPU_PROFILER_REGION") || return 1; fi
	jq -n --argjson status "$status" --argjson result "$result" --argjson metadata "$metadata" \
		--arg output "$output_dir" --arg id "$cpu_profiler_job_id" --argjson duration "$CPU_PROFILER_DURATION_SECONDS" \
		--argjson elapsed "$elapsed" --rawfile error "${output_dir}/error.log" '
 {scope:$metadata.scope,type:$metadata.type,mode:$metadata.mode,output_dir:$output,request_id:$id,duration_seconds:$duration,elapsed_seconds:$elapsed,grafana_url:null}
 + (if $status==0 then $result else {status:"failed",error:("Profiling failed; inspect "+$output+"/*.log. "+($error|rtrimstr("\n")))} end)'
}

cleanup() {
	local status=$?
	trap - EXIT INT TERM
	set +e
	if ! profiling_task_stop_if_active "$cpu_profiler_directory" "$CPU_PROFILER_API_BASE" "$CPU_PROFILER_API_TOKEN" "$cpu_profiler_job_id" >> "${cpu_profiler_directory}/cleanup.log" 2>&1; then status=1; fi
	if ! profiling_irq_load_stop "$cpu_profiler_directory" >> "${cpu_profiler_directory}/cleanup.log" 2>&1; then status=1; fi
	if ! kubernetes_namespace_cleanup "$cpu_profiler_directory" "$cpu_profiler_workload_namespace" workload; then status=1; fi
	if ! cpu_profiler_result_write "$cpu_profiler_directory" "$status" "$((SECONDS - cpu_profiler_started))" > "${cpu_profiler_directory}/result.json"; then
		status=1
		printf 'Failed to write result to %s/result.json.\n' "$cpu_profiler_directory" >&4
	fi
	((status == 0)) || tail -n 20 "${cpu_profiler_directory}/error.log" >&4
	exit "$status"
}

main() {
	local output_dir=$1 node namespace pod_file container_id='' cpu=''
	require_commands curl
	require_endpoints_ready "$output_dir" "${CPU_PROFILER_NODE_BASE}/readyz" "${CPU_PROFILER_API_BASE}/readyz"
	if [[ $cpu_profiler_mode == irq ]]; then
		profiling_irq_load_start "$output_dir" "$CPU_PROFILER_DURATION_SECONDS"
		cpu=$(cat "${output_dir}/irq-cpu.txt")
		assert_irq_load_active "$output_dir"
	else
		require_commands kubectl ip crictl
		node=$(require_local_kubernetes_node "$output_dir")
		cri_images_ensure_present "$output_dir" "$CPU_PROFILER_CRI_ENDPOINT" "$CPU_PROFILER_WORKLOAD_IMAGE"
		namespace="huatuo-profiling-${cpu_profiler_directory##*/}-${cpu_profiler_started}-$$"
		namespace=${namespace//_/-}
		namespace=${namespace,,}
		kubectl --request-timeout=10s create namespace "$namespace" > "${output_dir}/namespace.log"
		cpu_profiler_workload_namespace=$namespace
		kubectl --request-timeout=10s -n "$namespace" create configmap workload \
			--from-file="workload.c=${ROOT_DIR}/integration/testdata/test_apiserver_profiling_workload.c" >> "${output_dir}/kubernetes.log"
		write_config_cpu_profiler_workload_pod "$node" "$cpu_profiler_mode" > "${output_dir}/workload.request.json"
		kubernetes_pod_create "$output_dir" "$namespace" "${output_dir}/workload.request.json"
		pod_file=$(kubernetes_pod_wait_ready "$output_dir" "$namespace" workload)
		kubernetes_pod_container_get "$pod_file" workload > "${output_dir}/target.json"
		container_id=$(jq -er .container_id "${output_dir}/target.json")
		node_container_wait_discovered "$output_dir" "$CPU_PROFILER_NODE_BASE" "$CPU_PROFILER_NODE_TOKEN" "$container_id"
		assert_kubernetes_pod_running "$namespace" workload "${output_dir}/workload.response.json"
	fi
	write_config_profiling_request "$CPU_PROFILER_TARGET_HOST" "$container_id" "$CPU_PROFILER_DURATION_SECONDS" "$cpu_profiler_mode" "$cpu" > "${output_dir}/create.request.json"
	cpu_profiler_job_id=$(profiling_task_create "$output_dir" "$CPU_PROFILER_API_BASE" "$CPU_PROFILER_API_TOKEN" "${output_dir}/create.request.json" | jq -er .request_id)
	profiling_task_wait_completed "$output_dir" "$CPU_PROFILER_API_BASE" "$CPU_PROFILER_API_TOKEN" "$cpu_profiler_job_id" "$((CPU_PROFILER_DURATION_SECONDS + 120))" > "${output_dir}/task.json"
	if [[ $cpu_profiler_mode == irq ]]; then
		assert_irq_load_active "$output_dir"
	else
		assert_kubernetes_pod_running "$namespace" workload "${output_dir}/workload.response.json"
	fi
	profiling_raw_query_all "$output_dir" "$CPU_PROFILER_API_BASE" "$CPU_PROFILER_API_TOKEN" "$cpu_profiler_job_id" > "${output_dir}/raw.json"
	if [[ $cpu_profiler_mode == irq ]]; then
		assert_irq_profile "$output_dir"
	else
		assert_native_profile "$output_dir" "$container_id" "$cpu_profiler_mode" 2
	fi
	write_config_profile_query "${output_dir}/task.json" "$cpu_profiler_mode" > "${output_dir}/profile.json"
	grafana_flamegraph_query "$output_dir" "$CPU_PROFILER_GRAFANA_BASE" "$CPU_PROFILER_GRAFANA_AUTH" "$CPU_PROFILER_REGION" "${output_dir}/profile.json" > "${output_dir}/flamegraph.json"
	assert_grafana_flamegraph "${output_dir}/flamegraph.json"
}

if [[ ${BASH_SOURCE[0]} == "$0" ]]; then
	for cpu_profiler_argument in "$@"; do
		case $cpu_profiler_argument in --help | -h)
			usage
			exit 0
			;;
		esac
	done
	if ! cpu_profiler_selection=$(profiling_native_modes_parse "$@"); then
		usage >&2
		exit 2
	fi
	mapfile -t cpu_profiler_modes <<< "$cpu_profiler_selection"
	require_commands jq
	if [[ -z ${CPU_PROFILER_TRIAL_DIR:-} ]]; then
		cpu_profiler_started=$SECONDS
		mkdir -p "${ROOT_DIR}/_output/profiling"
		cpu_profiler_run_dir=$(mktemp -d "${ROOT_DIR}/_output/profiling/run.XXXXXX")
		cpu_profiler_result=0
		trap 'cpu_profiler_result=130' INT
		trap 'cpu_profiler_result=143' TERM
		cpu_profiler_files=()
		for cpu_profiler_mode in "${cpu_profiler_modes[@]}"; do
			cpu_profiler_scope=$(profiling_mode_metadata "$cpu_profiler_mode" | jq -er .scope)
			cpu_profiler_directory="${cpu_profiler_run_dir}/${cpu_profiler_scope}/${cpu_profiler_mode}"
			mkdir -p "$cpu_profiler_directory"
			cpu_profiler_files+=("${cpu_profiler_directory}/result.json")
			# Each child owns its API task, namespace and cancellation cleanup.
			if ! CPU_PROFILER_TRIAL_DIR="$cpu_profiler_directory" bash "${BASH_SOURCE[0]}" "--${cpu_profiler_mode//_/-}" > /dev/null; then
				if ((cpu_profiler_result == 0)); then cpu_profiler_result=1; fi
			fi
			if [[ ! -s ${cpu_profiler_directory}/result.json ]]; then
				if ((cpu_profiler_result == 0)); then cpu_profiler_result=1; fi
				jq -n --arg mode "$cpu_profiler_mode" --arg scope "$cpu_profiler_scope" --arg output "$cpu_profiler_directory" \
					'{scope:$scope,mode:$mode,status:"failed",output_dir:$output,error:"Profiling child returned no result; inspect the trial logs."}' > "${cpu_profiler_directory}/result.json"
			fi
			((cpu_profiler_result < 128)) || break
		done
		profiling_results_write "$cpu_profiler_run_dir" "$((SECONDS - cpu_profiler_started))" "$cpu_profiler_result" "$CPU_PROFILER_GRAFANA_BASE" "$CPU_PROFILER_REGION" true "${cpu_profiler_files[@]}" > "${cpu_profiler_run_dir}/result.json"
		cat "${cpu_profiler_run_dir}/result.json"
		exit "$cpu_profiler_result"
	fi
	if ((${#cpu_profiler_modes[@]} != 1)); then
		printf 'A profiling child must select exactly one mode.\n' >&2
		exit 2
	fi
	cpu_profiler_directory=$CPU_PROFILER_TRIAL_DIR
	cpu_profiler_mode=${cpu_profiler_modes[0]}
	cpu_profiler_started=$SECONDS cpu_profiler_job_id='' cpu_profiler_workload_namespace=''
	: > "${cpu_profiler_directory}/error.log"
	exec 4>&2
	trap cleanup EXIT
	trap 'exit 130' INT
	trap 'exit 143' TERM
	printf 'Profiling %s (%s, %ss); logs: %s\n' "$(profiling_mode_metadata "$cpu_profiler_mode" | jq -er .scope)" "$cpu_profiler_mode" "$CPU_PROFILER_DURATION_SECONDS" "$cpu_profiler_directory" >&2
	main "$cpu_profiler_directory" > "${cpu_profiler_directory}/execution.log" 2> "${cpu_profiler_directory}/error.log"
fi
