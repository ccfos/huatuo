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

scope=${1:-all}
if (($# > 1)) || [[ "$scope" != all && "$scope" != host && "$scope" != container ]]; then
	echo "Usage: run_test_profiling.sh [all|host|container] after make compose-dev-up." >&2
	exit 2
fi
for dependency in curl jq; do
	command -v "$dependency" > /dev/null || {
		printf 'Install %s before running run_test_profiling.sh.\n' "$dependency" >&2
		exit 1
	}
done

readonly API_BASE='http://127.0.0.1:12740'
readonly GRAFANA_BASE='http://127.0.0.1:3000'
readonly TARGET_HOST='huatuo-dev-node'
readonly REGION='example'
readonly NODE_BASE='http://127.0.0.1:19704'
readonly NODE_TOKEN='huatuo-dev-node-token'
readonly WORKLOAD_IMAGE='gcc:14'
readonly KUBECTL=(kubectl --request-timeout=10s)
readonly API_TOKEN='huatuo-dev-api-token'
readonly GRAFANA_AUTH='admin:admin'
readonly DURATION_SECONDS=60
ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
readonly ROOT_DIR

request() {
	local label=$1 url=$2
	local auth=(-H "Authorization: Bearer ${API_TOKEN}")
	[[ "$label" != flamegraph ]] || auth=(-u "$GRAFANA_AUTH")
	shift 2
	curl --fail-with-body -sS --connect-timeout 2 --max-time 60 "${auth[@]}" \
		-o "${output_dir}/${label}.response.json" "$@" "$url" 2> "${output_dir}/${label}.log" || {
		cat "${output_dir}/${label}.log" "${output_dir}/${label}.response.json" >&2
		return 1
	}
}

run_scope() {
	local scope=$1 output_dir=$2
	local container_id namespace job_id deadline status raw_count
	local start_ms end_ms node_count
	container_id=''
	if [[ "$scope" == container ]]; then
		namespace="huatuo-profiling-${run_dir##*.}"
		namespace=${namespace,,}
		"${KUBECTL[@]}" create namespace "$namespace" > "${output_dir}/kubernetes.log" || return 1
		workload_namespace=$namespace
		"${KUBECTL[@]}" -n "$namespace" create configmap workload \
			--from-file="workload.c=${ROOT_DIR}/integration/testdata/test_apiserver_profiling_workload.c" \
			>> "${output_dir}/kubernetes.log" || return 1
		jq -n --arg node "$node" --arg image "$WORKLOAD_IMAGE" '{
   apiVersion:"v1",kind:"Pod",metadata:{name:"workload"},
   spec:{nodeName:$node,restartPolicy:"Never",terminationGracePeriodSeconds:2,
    containers:[{name:"workload",image:$image,
     command:["sh","-c","gcc -O1 -g -fno-omit-frame-pointer /src/workload.c -o /tmp/workload && exec /tmp/workload"],
     resources:{requests:{cpu:"100m",memory:"32Mi"},limits:{cpu:"500m",memory:"256Mi"}},
     readinessProbe:{exec:{command:["test","-x","/tmp/workload"]},periodSeconds:1},
     volumeMounts:[{name:"source",mountPath:"/src",readOnly:true}]}],
    volumes:[{name:"source",configMap:{name:"workload"}}]}
  }' > "${output_dir}/workload.request.json" || return 1
		"${KUBECTL[@]}" -n "$namespace" create -f "${output_dir}/workload.request.json" \
			>> "${output_dir}/kubernetes.log" || return 1
		kubectl --request-timeout=310s -n "$namespace" wait pod/workload --for=condition=Ready --timeout=300s \
			>> "${output_dir}/kubernetes.log" || return 1
		"${KUBECTL[@]}" -n "$namespace" get pod workload -o json > "${output_dir}/workload.response.json" || return 1
		container_id=$(jq -er '.status.containerStatuses[] | select(.name == "workload")
   | .containerID | sub("^[^:]+://"; "") | select(test("^[0-9a-fA-F]{12,64}$"))' \
			"${output_dir}/workload.response.json") || return 1
		# Pod readiness precedes the Agent kubelet cache refresh.
		deadline=$((SECONDS + 90))
		until curl --fail-with-body -sS --connect-timeout 2 --max-time 5 -H "Authorization: Bearer ${NODE_TOKEN}" \
			"${NODE_BASE}/v1/containers/${container_id}" -o "${output_dir}/container.response.json" 2> "${output_dir}/container.log"; do
			if ((SECONDS >= deadline)); then
				printf 'Agent did not discover container %s; check kubelet synchronization and Agent logs.\n' "$container_id" >&2
				return 1
			fi
			sleep 2 || return 1
		done
	fi

	jq -n --arg host "$TARGET_HOST" --arg scope "$scope" --arg container "$container_id" --argjson duration "$DURATION_SECONDS" '{
		type:"cpu", language:"c", mode:"oncpu", scope:$scope,
		duration_seconds:$duration, hostname:$host
	} + (if $scope == "container" then {container_id:$container} else {} end)' > "${output_dir}/create.request.json" || return 1
	request create "${API_BASE}/v1/profiling" -X POST \
		-H 'Content-Type: application/json' \
		--data-binary "@${output_dir}/create.request.json" || return 1
	job_id=$(jq -er '.data.request_id | select(type == "string" and test("^[A-Za-z0-9_-]+$"))' \
		"${output_dir}/create.response.json") || return 1

	deadline=$((SECONDS + DURATION_SECONDS + 120))

	while true; do
		request status "${API_BASE}/v1/profiling/${job_id}" || return 1
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

	request raw "${API_BASE}/v1/profiling/${job_id}/raw?limit=1" || return 1
	raw_count=$(jq -er '.data.items | arrays | length' "${output_dir}/raw.response.json") || return 1
	if [[ "$scope" == container ]]; then
		jq -e --arg container "$container_id" '.data.items
   | (length > 0 and all(.[]; .container_id == $container)) or error("Missing windows or incorrect container ID")' \
			"${output_dir}/raw.response.json" > /dev/null || return 1
	fi

	# The client may have a different clock from the development node.
	start_ms=$(jq -er '.data.created_at | sub("\\.[0-9]+Z$"; "Z") | fromdateiso8601 * 1000' "${output_dir}/status.response.json") || return 1
	# Round up the server's end time to include subsecond publication timestamps.
	end_ms=$(jq -er '.data.ended_at | sub("\\.[0-9]+Z$"; "Z") | (fromdateiso8601 + 1) * 1000' "${output_dir}/status.response.json") || return 1
	jq -n --arg id "$job_id" --arg host "$TARGET_HOST" --arg region "$REGION" \
		--arg scope "$scope" --arg container "$container_id" --arg from "$start_ms" --arg to "$end_ms" '{
		from:$from, to:$to, queries:[{
			refId:"A", queryType:"profile",
			datasource:{type:"grafana-pyroscope-datasource",uid:"huatuo-apiserver-pyroscope"},
			profileTypeId:"process_cpu:cpu:nanoseconds:cpu:nanoseconds",
			labelSelector:("{scope=" + ($scope | tojson) + (if $scope == "container" then ",container_id=" + ($container | tojson) else "" end) + ",region=" + ($region | tojson) + ",hostname=" + ($host | tojson) + ",id=" + ($id | tojson) + "}")
		}]
	}' > "${output_dir}/flamegraph.request.json" || return 1
	request flamegraph "${GRAFANA_BASE}/api/ds/query" \
		-H 'Content-Type: application/json' --data-binary "@${output_dir}/flamegraph.request.json" || return 1
	node_count=$(jq -er '.results.A
  | if .status == 200 and (.error | not) then [.frames[]?.data.values[0] | length] | add // 0
    else error("Grafana query failed: " + tostring) end' "${output_dir}/flamegraph.response.json") || return 1
	if ((node_count == 0)); then
		printf 'No flame graph nodes; inspect %s/raw.response.json and the target workload.\n' "$output_dir" >&2
		[[ "$scope" == host ]] || return 1
	fi

	jq -n --arg scope "$scope" --arg id "$job_id" --arg container "$container_id" \
		--arg output "$output_dir" --argjson windows "$raw_count" --argjson nodes "$node_count" \
		--argjson from "$start_ms" --argjson to "$end_ms" \
		'{scope:$scope,status:"passed",request_id:$id,container_id:(if $container == "" then null else $container end),raw_windows:$windows,flamegraph_nodes:$nodes,output_dir:$output,time_range:{from_ms:$from,to_ms:$to}}'
}

printf 'Starting CPU profiling (60s per scope)...\n' >&2
started=$SECONDS
mkdir -p -- "${ROOT_DIR}/_output/profiling"
run_dir=$(mktemp -d "${ROOT_DIR}/_output/profiling/run.XXXXXX")
workload_namespace=''
cleanup() {
	if [[ -n "$workload_namespace" ]]; then
		"${KUBECTL[@]}" -n "$workload_namespace" get pod workload -o json > "${run_dir}/container/pod-at-exit.json" 2> /dev/null || true
		"${KUBECTL[@]}" -n "$workload_namespace" logs workload > "${run_dir}/container/workload.log" 2>&1 || true
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

scopes=(host)
if [[ "$scope" != host ]] && command -v kubectl > /dev/null && command -v ip > /dev/null \
	&& "${KUBECTL[@]}" get nodes -o json > "${run_dir}/nodes.json" 2> "${run_dir}/kubernetes.log" \
	&& ip -j address > "${run_dir}/addresses.json" 2>> "${run_dir}/kubernetes.log" \
	&& node=$(jq -er --slurpfile addresses "${run_dir}/addresses.json" '
  [$addresses[0][].addr_info[].local] as $local
  | [.items[] | select(.spec.unschedulable != true)
     | select(any(.status.conditions[]; .type == "Ready" and .status == "True"))
     | select(all(.status.conditions[]; if (.type | endswith("Pressure")) or .type == "NetworkUnavailable" then .status == "False" else true end))
     | select(any(.status.addresses[]; .type == "InternalIP" and (.address as $ip | $local | index($ip) != null)))
     | .metadata.name]
  | if length == 1 then .[0] else error("Expected one local healthy Kubernetes node") end
 ' "${run_dir}/nodes.json" 2>> "${run_dir}/kubernetes.log"); then
	scopes=(container)
	[[ "$scope" != all ]] || scopes=(host container)
fi
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
		cat "${directory}/error.log" >&2
	fi
done
if ! cleanup; then result=1; fi
# Use one server-time range so switching Scope keeps both tasks selectable.
jq -s --arg output "$run_dir" --argjson elapsed "$((SECONDS - started))" --argjson exit_status "$result" \
	--arg grafana "$GRAFANA_BASE" --arg host "$TARGET_HOST" --arg region "$REGION" '
 ([.[] | select(.status == "passed")]) as $passed
 | ($passed | map(.time_range.from_ms) | min) as $from
 | ($passed | map(.time_range.to_ms) | max) as $to
 | map(if .status == "passed" then
     .time_range = {from_ms:$from,to_ms:$to}
     | .grafana_url = ($grafana + "/d/continuous-profiling?var-region=" + ($region | @uri)
       + "&var-hostname=" + ($host | @uri) + "&var-task_id=" + .request_id
       + "&var-scope=" + ((if .scope == "host" then "NOT _exists_:container_id" else "_exists_:container_id" end) | @uri)
       + (if .scope == "container" then "&var-container_id=" + .container_id else "" end)
       + "&var-type=process_cpu%3Acpu%3Ananoseconds%3Acpu%3Ananoseconds&from=" + ($from | tostring) + "&to=" + ($to | tostring))
   else . end)
 | {status:(if $exit_status == 0 and all(.[]; .status == "passed") then "passed" else "failed" end),elapsed_seconds:$elapsed,output_dir:$output,results:.}
' "${files[@]}" > "${run_dir}/result.json"
jq . "${run_dir}/result.json"
exit "$result"
