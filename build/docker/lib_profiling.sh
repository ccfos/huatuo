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

# Common operations return data; callers own test orchestration and cleanup.
source "${ROOT_DIR}/build/lib_profiling_assert.sh"

require_commands() {
	local command
	for command in "$@"; do
		command -v "$command" > /dev/null || {
			printf 'Install %s before running profiling scripts.\n' "$command" >&2
			return 1
		}
	done
}

http_request() {
	local output_dir=$1 label=$2 url=$3 status curl_status=0
	shift 3
	# A transport failure may leave no response, or an earlier polling response.
	: > "${output_dir}/${label}.response.json"
	status=$(curl --noproxy 127.0.0.1,localhost -sS --connect-timeout 2 --max-time 60 \
		-o "${output_dir}/${label}.response.json" -w '%{http_code}' "$@" "$url" \
		2> "${output_dir}/${label}.log") || curl_status=$?
	if [[ $curl_status != 0 || ! $status =~ ^[123][0-9]{2}$ ]]; then
		printf '%s request to %s failed (curl=%s HTTP=%s); inspect %s/%s.log.\n' "$label" "$url" "$curl_status" "$status" "$output_dir" "$label" >&2
		cat "${output_dir}/${label}.log" >&2
		[[ ! -s "${output_dir}/${label}.response.json" ]] || cat "${output_dir}/${label}.response.json" >&2
		return 1
	fi
	cat "${output_dir}/${label}.response.json"
}

cri_images_ensure_present() {
	local output_dir=$1 endpoint=$2 image
	local cri=(crictl --runtime-endpoint "$endpoint" --image-endpoint "$endpoint" --timeout=600s)
	shift 2
	for image in "$@"; do
		if "${cri[@]}" inspecti "$image" >> "${output_dir}/image-pull.log" 2>&1; then
			continue
		fi
		printf 'Pulling %s with crictl...\n' "$image" >&2
		if ! "${cri[@]}" pull "$image" >> "${output_dir}/image-pull.log" 2>&1; then
			printf 'crictl could not pull %s using %s; check runtime access and registry connectivity in %s/image-pull.log.\n' \
				"$image" "$endpoint" "$output_dir" >&2
			return 1
		fi
	done
}

require_local_kubernetes_node() {
	local output_dir=$1 node
	local KUBECTL=(kubectl --request-timeout=10s)
	require_commands kubectl ip || return 1
	"${KUBECTL[@]}" get nodes -o json > "${output_dir}/nodes.json" 2> "${output_dir}/kubernetes.log" || {
		cat "${output_dir}/kubernetes.log" >&2
		return 1
	}
	ip -j address > "${output_dir}/addresses.json" 2>> "${output_dir}/kubernetes.log" || {
		cat "${output_dir}/kubernetes.log" >&2
		return 1
	}
	node=$(jq -er --slurpfile addresses "${output_dir}/addresses.json" '
  [$addresses[0][].addr_info[].local] as $local
  | [.items[] | select(any(.status.addresses[]; .type == "InternalIP" and (.address as $ip | $local | index($ip) != null)))
     | .metadata.name]
  | if length == 1 then .[0] else error("Expected one local Kubernetes node") end
 ' "${output_dir}/nodes.json" 2>> "${output_dir}/kubernetes.log") || {
		printf 'No unique Kubernetes node matches this host.\n' >&2
		return 1
	}
	if ! kubectl --request-timeout=15s wait --for=condition=Ready "node/$node" --timeout=10s \
		>> "${output_dir}/kubernetes.log" 2>&1; then
		printf 'Kubernetes node %s did not become ready.\n' "$node" >&2
		return 1
	fi
	if jq -e --arg node "$node" '.items[] | select(.metadata.name == $node)
 | any(.status.conditions[]; .type == "DiskPressure" and .status == "True")' "${output_dir}/nodes.json" > /dev/null; then
		printf 'Node %s has DiskPressure; free disk space and wait for recovery before profiling.\n' "$node" >&2
		return 1
	fi
	printf '%s\n' "$node"
}

kubernetes_pod_create() {
	local output_dir=$1 namespace=$2 manifest=$3
	kubectl --request-timeout=10s -n "$namespace" create -f "$manifest" >> "${output_dir}/kubernetes.log"
}

kubernetes_pod_wait_ready() {
	local output_dir=$1 namespace=$2 pod=$3 timeout=${4:-300} workload_error
	local KUBECTL=(kubectl --request-timeout=10s)
	if kubectl --request-timeout="$((timeout + 10))s" -n "$namespace" wait --for=condition=Ready "pod/$pod" --timeout="${timeout}s" -o json \
		> "${output_dir}/${pod}.response.json" 2>> "${output_dir}/kubernetes.log"; then
		printf '%s\n' "${output_dir}/${pod}.response.json"
		return 0
	fi
	"${KUBECTL[@]}" -n "$namespace" get pod "$pod" -o json \
		> "${output_dir}/${pod}.response.json" 2>> "${output_dir}/kubernetes.log" || true
	workload_error=''
	if [[ -s ${output_dir}/${pod}.response.json ]]; then
		workload_error=$(jq -r '
  if .status.reason then .status.reason + ": " + (.status.message // "")
  else [((.status.initContainerStatuses // []) + (.status.containerStatuses // []))[]
    | .state | to_entries[]
    | select(.key == "waiting" or (.key == "terminated" and .value.exitCode != 0))
    | (.value.reason // "Container failed") + ": " + (.value.message // "")]
    | join("\n") end' "${output_dir}/${pod}.response.json") || workload_error='Invalid Pod response.'
	fi
	"${KUBECTL[@]}" -n "$namespace" describe pod "$pod" > "${output_dir}/pod-describe.log" 2>&1 || true
	printf 'Pod %s: %s Inspect %s/pod-describe.log.\n' "$pod" "${workload_error:-Workload did not become ready.}" "$output_dir" >&2
	return 1
}

assert_kubernetes_pod_running() {
	local namespace=$1 pod=$2 response=$3
	if ! kubectl --request-timeout=10s -n "$namespace" get pod "$pod" -o json > "$response" \
		|| ! jq -e '.status.phase=="Running" and (.status.containerStatuses | length==1 and all(.[]; .ready and .restartCount==0))' "$response" > /dev/null; then
		printf 'Expected running %s without restarts; inspect %s and Pod logs.\n' "$pod" "$response" >&2
		return 1
	fi
}

kubernetes_pod_container_get() {
	local pod_file=$1 container=$2
	jq -e --arg container "$container" '
 .status as $status | [$status.containerStatuses[]? | select(.name == $container)]
 | if length != 1 then error("Expected one container named " + $container) else .[0] end
 | {container_id:(.containerID | sub("^[^:]+://"; "")),pod_ip:$status.podIP}
 | if (.container_id | test("^[0-9a-fA-F]{12,64}$")) and (.pod_ip | length > 0) then .
   else error("Missing container ID or Pod IP for " + $container) end' "$pod_file"
}

kubernetes_pod_delete() {
	local output_dir=$1 namespace=$2 pod=$3
	kubectl --request-timeout=40s -n "$namespace" delete pod "$pod" --ignore-not-found --wait=true --timeout=30s \
		> "${output_dir}/${pod}-stop.log" 2>&1 || {
		printf 'Failed to delete pod %s; inspect %s/%s-stop.log.\n' "$pod" "$output_dir" "$pod" >&2
		return 1
	}
}

kubernetes_namespace_cleanup() {
	local output_dir=$1 namespace=$2 entry pod log_name
	shift 2
	[[ -n $namespace ]] || return 0
	for entry in "$@"; do
		pod=${entry%%:*}
		log_name=${entry#*:}
		kubectl --request-timeout=10s -n "$namespace" logs "$pod" > "${output_dir}/${log_name}.log" 2>&1 || true
		kubectl --request-timeout=10s -n "$namespace" get pod "$pod" -o json > "${output_dir}/${pod}-at-exit.json" 2> /dev/null || true
	done
	kubectl --request-timeout=10s delete namespace "$namespace" --ignore-not-found --wait=false >> "${output_dir}/cleanup.log" 2>&1 || {
		printf 'Failed to remove namespace %s; inspect %s/cleanup.log.\n' "$namespace" "$output_dir" >&2
		return 1
	}
}

node_container_wait_discovered() {
	local output_dir=$1 node_base=$2 token=$3 container_id=$4 timeout=${5:-90} deadline status
	# Pod readiness precedes the Agent kubelet cache refresh.
	deadline=$((SECONDS + timeout))
	while :; do
		if status=$(curl --noproxy 127.0.0.1,localhost -sS --connect-timeout 2 --max-time 5 \
			-H "Authorization: Bearer ${token}" "${node_base}/v1/containers/${container_id}" \
			-o "${output_dir}/container.response.json" -w '%{http_code}' 2> "${output_dir}/container.log") \
			&& [[ $status =~ ^[123][0-9]{2}$ ]]; then
			return 0
		fi
		if ((SECONDS >= deadline)); then
			printf 'Agent did not discover container %s.\n' "$container_id" >&2
			return 1
		fi
		sleep 2 || return 1
	done
}

profiling_task_create() {
	local output_dir=$1 api_base=$2 token=$3 request=$4
	http_request "$output_dir" create "${api_base}/v1/profiling" -X POST -H "Authorization: Bearer ${token}" \
		-H 'Content-Type: application/json' --data-binary "@$request" > /dev/null || return 1
	jq -e '.data | select(.request_id | type == "string" and test("^[A-Za-z0-9_-]+$"))' "${output_dir}/create.response.json"
}

profiling_task_wait_completed() {
	local output_dir=$1 api_base=$2 token=$3 job_id=$4 timeout=$5 deadline status
	deadline=$((SECONDS + timeout))

	while true; do
		http_request "$output_dir" status "${api_base}/v1/profiling/${job_id}" -H "Authorization: Bearer ${token}" > /dev/null || return 1
		status=$(jq -er .data.status "${output_dir}/status.response.json") || return 1
		[[ "$status" != terminal ]] || break
		if ((SECONDS >= deadline)); then
			printf 'ERROR: %s\n' "Task ${job_id} timed out; inspect ${output_dir}/status.response.json." >&2
			return 1
		fi
		sleep 5 || return 1
	done

	jq -e '.data.terminal | .outcome == "completed" or error(.message // .reason // .outcome)' \
		"${output_dir}/status.response.json" > /dev/null || return 1
	jq -e .data "${output_dir}/status.response.json"
}

profiling_task_stop() {
	local output_dir=$1 api_base=$2 token=$3 job_id=$4
	http_request "$output_dir" stop "${api_base}/v1/profiling/${job_id}/stop" -X POST -H "Authorization: Bearer ${token}"
}

profiling_raw_query() {
	local output_dir=$1 api_base=$2 token=$3 job_id=$4 offset=${5:-0}
	http_request "$output_dir" raw "${api_base}/v1/profiling/${job_id}/raw?limit=1&offset=${offset}" -H "Authorization: Bearer ${token}" > /dev/null || return 1
	jq -ce '.data | select(.items | type == "array")' "${output_dir}/raw.response.json"
}

grafana_flamegraph_query() {
	local output_dir=$1 grafana=$2 auth=$3 region=$4 profile=$5
	jq --arg region "$region" '
 {from:(.time_range.from_ms|tostring),to:(.time_range.to_ms|tostring),queries:[{
 refId:"A",queryType:"profile",datasource:{type:"grafana-pyroscope-datasource",uid:"huatuo-apiserver-pyroscope"},
 profileTypeId:.profile_type,
 labelSelector:(["scope="+(.scope|tojson),"region="+($region|tojson),"hostname="+(.hostname|tojson),"id="+(.request_id|tojson)]
 + (if .scope=="container" then ["container_id="+(.container_id|tojson)] else [] end) | "{"+join(",")+"}")
 }]}' "$profile" > "${output_dir}/flamegraph.request.json" || return 1
	http_request "$output_dir" flamegraph "${grafana}/api/ds/query" -u "$auth" -H 'Content-Type: application/json' \
		--data-binary "@${output_dir}/flamegraph.request.json" > /dev/null || return 1
	jq -e '.results.A | if .status == 200 and (.error|not) then . else error("Grafana query failed: "+tostring) end' \
		"${output_dir}/flamegraph.response.json"
}

grafana_profiling_url_build() {
	local grafana=$1 region=$2 profile=$3
	jq -er -L "${ROOT_DIR}/build" --arg grafana "$grafana" --arg region "$region" \
		'include "profiling"; grafana_profiling_url($grafana; $region)' "$profile"
}

require_endpoints_ready() {
	local output_dir=$1 url status index=0 prefix
	shift
	for url in "$@"; do
		index=$((index + 1))
		prefix="${output_dir}/endpoint-${index}-ready"
		status=$(curl -sS --noproxy 127.0.0.1,localhost --connect-timeout 2 --max-time 3 \
			-o "${prefix}.body" -w '%{http_code}' "$url" 2> "${prefix}.err") || {
			printf 'Readiness request to %s failed; run make compose-dev-up and inspect %s.err.\n' "$url" "$prefix" >&2
			return 1
		}
		if [[ ! $status =~ ^[123][0-9]{2}$ ]]; then
			printf 'Readiness endpoint %s returned HTTP %s; inspect %s.body.\n' "$url" "$status" "$prefix" >&2
			return 1
		fi
	done
}

profiling_mode_metadata() {
	local mode=$1 type profile_type scope=container
	case $mode in
	irq)
		type=irq
		mode=softirq
		scope=host
		profile_type='irqtracing:irq:count:irq:count'
		;;
	oncpu)
		type=cpu
		profile_type='process_cpu:cpu:nanoseconds:cpu:nanoseconds'
		;;
	offcpu)
		type=cpu
		profile_type='process_offcpu:offcpu:nanoseconds:offcpu:nanoseconds'
		;;
	virtual_alloc | physical_alloc | object_alloc | object_usage)
		type=memory
		profile_type="memory:${mode}:bytes:space:bytes"
		;;
	physical_usage)
		type=memory
		profile_type='memory:physical_usage:bytes:space:bytes'
		;;
	*)
		printf 'Unsupported profiling mode: %s.\n' "$mode" >&2
		return 1
		;;
	esac
	printf '{"type":"%s","profile_type":"%s","scope":"%s","mode":"%s"}\n' "$type" "$profile_type" "$scope" "$mode"
}

profiling_native_modes_parse() {
	profiling_modes_parse 'oncpu offcpu virtual_alloc physical_alloc physical_usage irq' oncpu "$@"
}

profiling_modes_parse() {
	local allowed_modes=$1 default_modes=$2 flag mode
	shift 2
	local modes=()
	read -r -a modes <<< "$allowed_modes"
	local -A allowed=() requested=()
	for mode in "${modes[@]}"; do
		allowed["--${mode//_/-}"]=$mode
	done
	for flag in "$@"; do
		if [[ -z $flag || -z ${allowed[$flag]:-} ]]; then
			printf 'Unknown argument: %s\n' "$flag" >&2
			return 2
		fi
		requested[${allowed[$flag]}]=true
	done
	if (($# == 0)); then
		read -r -a modes <<< "$default_modes"
		printf '%s\n' "${modes[@]}"
		return 0
	fi
	for mode in "${modes[@]}"; do
		if [[ ${requested[$mode]:-false} == true ]]; then printf '%s\n' "$mode"; fi
	done
}

write_config_profiling_request() {
	local hostname=$1 container_id=$2 duration=$3 mode=$4 cpu=${5:-} metadata
	metadata=$(profiling_mode_metadata "$mode") || return 1
	if [[ $mode == irq && ! $cpu =~ ^[0-9]+$ ]]; then
		printf 'IRQ profiling requires the CPU selected for its loopback workload.\n' >&2
		return 1
	fi
	jq -n --arg host "$hostname" --arg container "$container_id" --argjson duration "$duration" \
		--arg cpu "$cpu" --argjson metadata "$metadata" '
 {type:$metadata.type,mode:$metadata.mode,scope:$metadata.scope,hostname:$host,duration_seconds:$duration}
 + (if $metadata.scope=="host" then {host_cpu_list:$cpu} else {language:"c",container_id:$container} end)'
}

write_config_profile_query() {
	local task=$1 mode=$2 metadata
	metadata=$(profiling_mode_metadata "$mode") || return 1
	jq --argjson metadata "$metadata" '{request_id,hostname,scope,type,mode,profile_type:$metadata.profile_type,time_range:{
 from_ms:(.created_at|sub("\\.[0-9]+Z$";"Z")|fromdateiso8601*1000),
 to_ms:(.ended_at|sub("\\.[0-9]+Z$";"Z")|(fromdateiso8601+1)*1000)}}
 + (if .scope=="host" then {host_cpu_list} else {container_id} end)' "$task"
}

profiling_irq_load_start() {
	local output_dir=$1 duration=$2 cpu
	require_commands python3 taskset timeout || return 1
	cpu=$(python3 -c 'import os; print(min(os.sched_getaffinity(0)))') || return 1
	printf '%s\n' "$cpu" > "${output_dir}/irq-cpu.txt" || return 1
	# A single CPU bounds collection cost and keeps the load and IRQ selection aligned.
	timeout --kill-after=5 "$((duration + 150))" taskset -c "$cpu" python3 -u -c '
import socket
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
s.bind(("127.0.0.1", 0))
s.settimeout(2)
destination = s.getsockname()
print("ready", flush=True)
while True:
    s.sendto(b"irq", destination)
    s.recvfrom(16)
' > "${output_dir}/irq-load.log" 2>&1 &
	printf '%s\n' "$!" > "${output_dir}/irq-load.pid"
}

assert_irq_load_active() {
	local output_dir=$1 pid
	pid=$(cat "${output_dir}/irq-load.pid") || return 1
	if ! kill -0 "$pid" 2> /dev/null; then
		printf 'IRQ loopback workload exited; inspect %s/irq-load.log.\n' "$output_dir" >&2
		return 1
	fi
}

profiling_irq_load_stop() {
	local output_dir=$1 pid status=0
	[[ -s ${output_dir}/irq-load.pid ]] || return 0
	pid=$(cat "${output_dir}/irq-load.pid") || return 1
	kill -TERM "$pid" 2> /dev/null || true
	wait "$pid" || status=$?
	: > "${output_dir}/irq-load.pid" || return 1
	case $status in
	0 | 124 | 137 | 143) return 0 ;;
	*)
		printf 'IRQ workload failed (exit=%s); inspect %s/irq-load.log.\n' "$status" "$output_dir" >&2
		return 1
		;;
	esac
}

assert_irq_profile() {
	local output_dir=$1 cpu
	cpu=$(cat "${output_dir}/irq-cpu.txt") || return 1
	assert_profile_windows "${output_dir}/raw.json" 1 'irqtracing:irq:count:irq:count' || return 1
	jq -e --arg cpu "$cpu" '
 [.items[].profile | . as $p | .sample[]? | {value:.value,labels:
   [.label[]? | {key:$p.string_table[.key],value:$p.string_table[.str]}] | from_entries}] as $samples
 |
 (.has_more|not) and (.items|length)==1 and all(.items[];
   (.container_id // "")=="" and .profile.duration_nanos>0 and
   ((.dropped_samples // {}) | type=="object" and all(.[]; type=="number" and .>=0 and floor==.))) and
 ($samples|length)>0 and all($samples[];
   (.value|length>0 and all(.[];.>0)) and .labels.cpu==$cpu and .labels.irq_type=="softirq" and
   (.labels.irq_context|IN("source","victim")) and
   (.labels.irq_vector|type=="string" and length>0) and
   (.labels.pid|type=="string" and test("^(0|[1-9][0-9]*)$")) and (.labels.comm|type=="string"))' \
		"${output_dir}/raw.json" > /dev/null || {
		printf 'Expected a host IRQ window with positive counts and IRQ labels for CPU %s; inspect %s/raw.json.\n' "$cpu" "$output_dir" >&2
		return 1
	}
}

profiling_task_stop_if_active() {
	local output_dir=$1 api_base=$2 token=$3 job_id=$4
	[[ -n $job_id ]] || return 0
	if jq -e '.data.status=="terminal"' "${output_dir}/status.response.json" > /dev/null 2>&1; then return 0; fi
	profiling_task_stop "$output_dir" "$api_base" "$token" "$job_id"
}

profiling_result_write() {
	local output_dir=$1 grafana=$2 region=$3 url windows nodes
	url=$(grafana_profiling_url_build "$grafana" "$region" "${output_dir}/profile.json") || return 1
	windows=$(jq -er '.items|length' "${output_dir}/raw.json") || return 1
	nodes=$(jq -er '[.frames[]?.data.values[0]|length]|add // 0' "${output_dir}/flamegraph.json") || return 1
	jq --arg output "$output_dir" --arg url "$url" --argjson windows "$windows" --argjson nodes "$nodes" '
 .+{status:"passed",output_dir:$output,grafana_url:$url,raw_windows:$windows,flamegraph_nodes:$nodes}' "${output_dir}/profile.json"
}

profiling_results_write() {
	local output_dir=$1 elapsed=$2 status=$3 grafana=$4 region=$5 align_ranges=$6 report results file result
	shift 6
	report=$(jq -s -L "${ROOT_DIR}/build" --arg output "$output_dir" --argjson elapsed "$elapsed" --argjson status "$status" \
		--arg grafana "$grafana" --arg region "$region" --argjson align "$align_ranges" '
 include "profiling";
 (if $align then
  ([.[]|select(.status=="passed")|.time_range] | {from_ms:(map(.from_ms)|min),to_ms:(map(.to_ms)|max)}) as $range
  | map(if .status=="passed" then .time_range=$range | .grafana_url=grafana_profiling_url($grafana;$region) else . end)
 else . end)
 | {status:(if $status==0 and length>0 and all(.[];.status=="passed") then "passed" else "failed" end),
 elapsed_seconds:$elapsed,output_dir:$output,results:.}' "$@") || return 1
	if [[ $align_ranges == true ]]; then
		results=$(jq -c '.results[]' <<< "$report") || return 1
		for file in "$@"; do
			IFS= read -r result || return 1
			printf '%s\n' "$result" > "$file" || return 1
		done <<< "$results"
	fi
	printf '%s\n' "$report"
}

profiling_raw_query_all() {
	local output_dir=$1 api_base=$2 token=$3 job_id=$4 offset=0 page_file count separator=''
	local pages=()
	# Physical snapshots can exceed the API response cap when fetched together.
	while :; do
		page_file="${output_dir}/raw-page-${offset}.json"
		profiling_raw_query "$output_dir" "$api_base" "$token" "$job_id" "$offset" > "$page_file" || return 1
		jq -e --argjson offset "$offset" '
 .offset==$offset and .limit==1 and (.has_more|type=="boolean") and (.items|length<=1)
 and (if .has_more then (.items|length)==1 else true end)' "$page_file" > /dev/null || {
			printf 'Invalid raw profile page at offset %s; inspect %s.\n' "$offset" "$page_file" >&2
			return 1
		}
		count=$(jq '.items|length' "$page_file") || return 1
		if ((count > 0)); then pages+=("$page_file"); fi
		offset=$((offset + count))
		if ! jq -e '.has_more' "$page_file" > /dev/null; then break; fi
		if ((offset >= 100)); then
			printf 'Profile exceeds the 100-window query budget; inspect %s.\n' "$page_file" >&2
			return 1
		fi
	done
	# Stream each page to keep merging independent of the full capture size.
	printf '{"has_more":false,"limit":%d,"offset":0,"items":[' "$offset"
	for page_file in "${pages[@]}"; do
		printf '%s' "$separator"
		jq -c '.items[0]' "$page_file" || return 1
		separator=,
	done
	printf ']}\n'
}

assert_native_profile() {
	local output_dir=$1 container_id=$2 mode=$3 minimum=$4 profile_type
	profile_type=$(profiling_mode_metadata "$mode" | jq -er .profile_type) || return 1
	assert_profile_windows "${output_dir}/raw.json" "$minimum" "$profile_type" "$container_id" || return 1
	jq -e '(.has_more | not) and any(.items[].profile.sample[]?.value[]?; .>0)' "${output_dir}/raw.json" > /dev/null || {
		printf 'Expected complete profiling windows with positive samples; inspect %s/raw.json.\n' "$output_dir" >&2
		return 1
	}
	case $mode in
	virtual_alloc | physical_alloc | physical_usage)
		jq -e 'all(.items[].profile; . as $p | any(.sample_type[]?; $p.string_table[.unit]=="bytes"))' \
			"${output_dir}/raw.json" > /dev/null || {
			printf 'Expected memory samples measured in bytes; inspect %s/raw.json.\n' "$output_dir" >&2
			return 1
		}
		;;
	esac
	if [[ $mode == offcpu ]]; then
		jq -e '
 [.items[].profile | . as $p | .sample[]? | {value:.value,labels:
   [.label[]? | {key:$p.string_table[.key],value:$p.string_table[.str]}] | from_entries}] as $samples
 | ($samples | length > 0) and all($samples[];
   (.value | length > 0 and all(.[]; . > 0)) and
   (.labels.offcpu_reason | IN("blocked","runqueue","runqueue_preempted","runqueue_yielded","runqueue_missed_wakeup","unknown")) and
   (.labels.cpu | type == "string" and test("^(0|[1-9][0-9]*)$")) and
   (.labels.pid | type == "string" and test("^[1-9][0-9]*$")) and
   (.labels.comm | type == "string"))' "${output_dir}/raw.json" > /dev/null || {
			printf 'Expected positive off-CPU samples with offcpu_reason/cpu/pid/comm string labels; inspect %s/raw.json.\n' "$output_dir" >&2
			return 1
		}
	fi
	return 0
}
