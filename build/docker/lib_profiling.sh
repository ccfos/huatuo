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
	local output_dir=$1 api_base=$2 token=$3 job_id=$4
	http_request "$output_dir" raw "${api_base}/v1/profiling/${job_id}/raw?limit=1" -H "Authorization: Bearer ${token}" > /dev/null || return 1
	jq -e '.data | select(.items | type == "array")' "${output_dir}/raw.response.json"
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
	jq -er --arg grafana "$grafana" --arg region "$region" '
 $grafana+"/d/continuous-profiling?var-region="+($region|@uri)
 +"&var-hostname="+(.hostname|@uri)+"&var-task_id="+(.request_id|@uri)
 +"&var-scope="+((if .scope=="host" then "NOT _exists_:container_id" else "_exists_:container_id" end)|@uri)
 +(if .scope=="container" then "&var-container_id="+(.container_id|@uri) else "" end)
 +"&var-type="+(.profile_type|@uri)+"&from="+(.time_range.from_ms|tostring)
 +"&to="+(.time_range.to_ms|tostring)' "$profile"
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
