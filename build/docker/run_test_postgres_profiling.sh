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

# Read-only transactions emphasize CPU execution rather than WAL waits.
set -euo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
readonly ROOT_DIR
source "${ROOT_DIR}/build/docker/lib_profiling.sh"
readonly POSTGRES_IMAGE='m.daocloud.io/docker.io/library/postgres:18.7-bookworm'
readonly POSTGRES_PGBENCH_IMAGE="$POSTGRES_IMAGE"
readonly POSTGRES_DURATION=60
readonly POSTGRES_API_BASE='http://127.0.0.1:12740'
readonly POSTGRES_API_TOKEN='huatuo-dev-api-token'
readonly POSTGRES_NODE_BASE='http://127.0.0.1:19704'
readonly POSTGRES_NODE_TOKEN='huatuo-dev-node-token'
readonly POSTGRES_GRAFANA_BASE='http://127.0.0.1:3000'
readonly POSTGRES_GRAFANA_AUTH='admin:admin'
readonly POSTGRES_TARGET_HOST='huatuo-dev-node'
readonly POSTGRES_REGION='pre-test'
readonly POSTGRES_CRI_ENDPOINT=${CONTAINER_RUNTIME_ENDPOINT:-unix:///run/containerd/containerd.sock}

write_config_postgres_pod() {
	jq -n --arg node "$1" --arg image "$POSTGRES_IMAGE" '{
 apiVersion:"v1",kind:"Pod",metadata:{name:"postgres"},
 spec:{nodeName:$node,restartPolicy:"Never",terminationGracePeriodSeconds:5,
 containers:[{name:"postgres",image:$image,imagePullPolicy:"Never",
 env:[{name:"POSTGRES_PASSWORD",value:"huatuo-benchmark"},{name:"POSTGRES_DB",value:"benchmark"}],
 resources:{requests:{cpu:"100m",memory:"256Mi"},limits:{cpu:"2",memory:"1Gi"}},
 startupProbe:{exec:{command:["pg_isready","-h","127.0.0.1","-U","postgres","-d","benchmark"]},periodSeconds:2,failureThreshold:150},
 volumeMounts:[{name:"data",mountPath:"/var/lib/postgresql"}]}],volumes:[{name:"data",emptyDir:{sizeLimit:"2Gi"}}]}}
 '
}

write_config_pgbench_pod() {
	# Cover Pod readiness and API deadlines; the caller stops the load after sampling.
	jq -n --arg node "$1" --arg address "$2" --arg image "$POSTGRES_PGBENCH_IMAGE" --argjson duration "$((POSTGRES_DURATION + 600))" '{
 apiVersion:"v1",kind:"Pod",metadata:{name:"pgbench"},
 spec:{nodeName:$node,restartPolicy:"Never",terminationGracePeriodSeconds:2,
 containers:[{name:"pgbench",image:$image,imagePullPolicy:"Never",
 env:[{name:"PGHOST",value:$address},{name:"PGUSER",value:"postgres"},{name:"PGDATABASE",value:"benchmark"},
 {name:"PGPASSWORD",value:"huatuo-benchmark"},{name:"PGCONNECT_TIMEOUT",value:"5"},{name:"LC_ALL",value:"C"}],
 command:["bash","-euo","pipefail","-c"],args:[
 "pgbench -i -s 10
 pgbench -n -S -M prepared -c 8 -j 2 --exit-on-abort -P 1 --progress-timestamp -T \($duration) 2>&1 | tee /tmp/pgbench.log"],
 resources:{requests:{cpu:"100m",memory:"64Mi"},limits:{cpu:"2",memory:"512Mi"}},
 startupProbe:{exec:{command:["grep","-q","^progress:","/tmp/pgbench.log"]},periodSeconds:2,failureThreshold:150}}]}}
 '
}

postgres_start() {
	local output_dir=$1 namespace=$2 node=$3 pod_file
	write_config_postgres_pod "$node" > "${output_dir}/service.request.json"
	kubernetes_pod_create "$output_dir" "$namespace" "${output_dir}/service.request.json"
	pod_file=$(kubernetes_pod_wait_ready "$output_dir" "$namespace" postgres)
	kubernetes_pod_container_get "$pod_file" postgres > "${output_dir}/target.json"
}

pgbench_start() {
	local output_dir=$1 namespace=$2 node=$3 address=$4
	write_config_pgbench_pod "$node" "$address" > "${output_dir}/benchmark.request.json"
	kubernetes_pod_create "$output_dir" "$namespace" "${output_dir}/benchmark.request.json"
	kubernetes_pod_wait_ready "$output_dir" "$namespace" pgbench > /dev/null
}

assert_pgbench_progress() {
	local output_dir=$1 namespace=$2
	kubectl --request-timeout=10s -n "$namespace" get pod pgbench -o json > "${output_dir}/benchmark.response.json" || {
		printf 'Cannot read pgbench progress; inspect %s/benchmark.response.json and benchmark.log.\n' "$output_dir" >&2
		return 1
	}
	kubectl --request-timeout=10s -n "$namespace" logs pgbench > "${output_dir}/benchmark.log" || {
		printf 'Cannot read pgbench progress; inspect %s/benchmark.response.json and benchmark.log.\n' "$output_dir" >&2
		return 1
	}
	jq -e '.status.phase == "Running" and all(.status.containerStatuses[]; .ready and .restartCount == 0)' \
		"${output_dir}/benchmark.response.json" > /dev/null || {
		printf 'Expected running pgbench without restarts; inspect %s/benchmark.response.json and benchmark.log.\n' "$output_dir" >&2
		return 1
	}
	awk -v since="$(($(date +%s) - 5))" '
 /^progress:/ { latest=$2; if ($4+0 <= 0 || $0 !~ /, 0 failed$/) failed=1 }
 END { exit (failed || latest+0 < since) }
 ' "${output_dir}/benchmark.log" || {
		printf 'Expected recent pgbench progress with positive TPS and no failed transactions; inspect %s/benchmark.log.\n' "$output_dir" >&2
		return 1
	}
}

pgbench_stop() {
	local output_dir=$1 namespace=$2
	kubectl --request-timeout=10s -n "$namespace" get pod pgbench -o json > "${output_dir}/benchmark-at-exit.json"
	postgres_benchmark_saved=true
	kubernetes_pod_delete "$output_dir" "$namespace" pgbench
}

assert_postgres_profile() {
	local output_dir=$1 container_id=$2 mode=$3 profile_type=$4
	assert_profile_windows "${output_dir}/raw.json" 1 "$profile_type" "$container_id" || return 1
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
	assert_grafana_flamegraph "${output_dir}/flamegraph.json" || return 1
}

postgres_result_write() {
	local output_dir=$1 status=$2 job_id=$3 elapsed=$4 url='' profile=/dev/null raw=/dev/null graph=/dev/null
	if ((status == 0)); then
		profile="${output_dir}/profile.json"
		raw="${output_dir}/raw.json"
		graph="${output_dir}/flamegraph.json"
		url=$(grafana_profiling_url_build "$POSTGRES_GRAFANA_BASE" "$POSTGRES_REGION" "$profile") || return 1
	fi
	jq -n --argjson status "$status" --arg url "$url" --arg id "$job_id" --arg mode "$postgres_mode" \
		--arg image "$POSTGRES_IMAGE" --arg tool "$POSTGRES_PGBENCH_IMAGE" --argjson duration "$POSTGRES_DURATION" \
		--argjson elapsed "$elapsed" --arg output "$output_dir" \
		--rawfile error "${output_dir}/stderr.log" --slurpfile profile "$profile" --slurpfile raw "$raw" --slurpfile graph "$graph" '
 {scope:"container",service:"postgres",mode:$mode,service_image:$image,benchmark_image:$tool,duration_seconds:$duration,
 elapsed_seconds:$elapsed,output_dir:$output,benchmark_log:($output+"/benchmark.log"),request_id:$id,grafana_url:null}
 + (if $status==0 then $profile[0] + {status:"passed",grafana_url:$url,raw_windows:($raw[0].items|length),
     flamegraph_nodes:([$graph[0].frames[]?.data.values[0]|length]|add // 0)}
    else {status:"failed",error:("Profiling failed; inspect "+$output+"/*.log. "+($error|rtrimstr("\n")))} end)'
}

cleanup() {
	local status=$? pod log_name
	trap - EXIT INT TERM
	set +e
	if [[ -n $postgres_job_id && $status != 0 ]] && ! jq -e '.data.status == "terminal"' \
		"${postgres_run_dir}/status.response.json" > /dev/null 2>&1; then
		profiling_task_stop "$postgres_run_dir" "$POSTGRES_API_BASE" "$POSTGRES_API_TOKEN" "$postgres_job_id" >> "${postgres_run_dir}/cleanup.log" 2>&1
	fi
	if [[ -n $postgres_namespace ]]; then
		for pod in postgres pgbench; do
			[[ $pod != pgbench || $postgres_benchmark_saved != true ]] || continue
			log_name=$pod
			[[ $pod != pgbench ]] || log_name=benchmark
			kubectl --request-timeout=10s -n "$postgres_namespace" logs "$pod" > "${postgres_run_dir}/${log_name}.log" 2>&1
			kubectl --request-timeout=10s -n "$postgres_namespace" get pod "$pod" -o json > "${postgres_run_dir}/${pod}-at-exit.json" 2> /dev/null
		done
		if ! kubectl --request-timeout=10s delete namespace "$postgres_namespace" --ignore-not-found --wait=false >> "${postgres_run_dir}/cleanup.log" 2>&1; then
			status=1
			printf 'Failed to remove namespace %s; inspect cleanup.log.\n' "$postgres_namespace" >> "${postgres_run_dir}/stderr.log"
		fi
	fi
	if ! postgres_result_write "$postgres_run_dir" "$status" "$postgres_job_id" "$((SECONDS - postgres_started))" > "${postgres_run_dir}/result.json"; then
		status=1
		printf 'Failed to write result to %s/result.json.\n' "$postgres_run_dir" >&2
	elif ! cat "${postgres_run_dir}/result.json"; then
		status=1
		printf 'Failed to print result from %s/result.json.\n' "$postgres_run_dir" >&2
	fi
	((status == 0)) || tail -n 20 "${postgres_run_dir}/stderr.log" >&2
	exit "$status"
}

main() {
	local output_dir=$1 node address namespace container_id profile_type
	case $postgres_mode in
	oncpu) profile_type='process_cpu:cpu:nanoseconds:cpu:nanoseconds' ;;
	offcpu) profile_type='process_offcpu:offcpu:nanoseconds:offcpu:nanoseconds' ;;
	esac
	require_commands kubectl ip curl crictl
	require_endpoints_ready "$output_dir" "${POSTGRES_NODE_BASE}/readyz" "${POSTGRES_API_BASE}/readyz"
	node=$(require_local_kubernetes_node "$output_dir")
	cri_images_ensure_present "$output_dir" "$POSTGRES_CRI_ENDPOINT" "$POSTGRES_IMAGE"
	namespace="huatuo-postgres-${output_dir##*.}"
	namespace=${namespace,,}
	kubectl --request-timeout=10s create namespace "$namespace" > "${output_dir}/namespace.log"
	# Register ownership only after creation succeeds so EXIT never removes an unrelated namespace.
	postgres_namespace=$namespace
	postgres_start "$output_dir" "$namespace" "$node"
	container_id=$(jq -er .container_id "${output_dir}/target.json")
	address=$(jq -er .pod_ip "${output_dir}/target.json")
	node_container_wait_discovered "$output_dir" "$POSTGRES_NODE_BASE" "$POSTGRES_NODE_TOKEN" "$container_id"
	pgbench_start "$output_dir" "$namespace" "$node" "$address"
	assert_pgbench_progress "$output_dir" "$namespace" || return 1
	jq -n --arg host "$POSTGRES_TARGET_HOST" --arg container "$container_id" --argjson duration "$POSTGRES_DURATION" --arg mode "$postgres_mode" \
		'{type:"cpu",language:"c",mode:$mode,scope:"container",hostname:$host,container_id:$container,duration_seconds:$duration}' \
		> "${output_dir}/create.request.json"
	postgres_job_id=$(profiling_task_create "$output_dir" "$POSTGRES_API_BASE" "$POSTGRES_API_TOKEN" "${output_dir}/create.request.json" | jq -er .request_id)
	profiling_task_wait_completed "$output_dir" "$POSTGRES_API_BASE" "$POSTGRES_API_TOKEN" "$postgres_job_id" "$((POSTGRES_DURATION + 120))" > "${output_dir}/task.json"
	assert_pgbench_progress "$output_dir" "$namespace" || return 1
	pgbench_stop "$output_dir" "$namespace"
	profiling_raw_query "$output_dir" "$POSTGRES_API_BASE" "$POSTGRES_API_TOKEN" "$postgres_job_id" > "${output_dir}/raw.json"
	jq --arg type "$profile_type" --arg mode "$postgres_mode" '{request_id,hostname,scope,container_id,mode,profile_type:$type,time_range:{
   from_ms:(.created_at|sub("\\.[0-9]+Z$";"Z")|fromdateiso8601*1000),
   to_ms:(.ended_at|sub("\\.[0-9]+Z$";"Z")|(fromdateiso8601+1)*1000)}}' \
		"${output_dir}/task.json" > "${output_dir}/profile.json"
	grafana_flamegraph_query "$output_dir" "$POSTGRES_GRAFANA_BASE" "$POSTGRES_GRAFANA_AUTH" "$POSTGRES_REGION" "${output_dir}/profile.json" > "${output_dir}/flamegraph.json"
	assert_postgres_profile "$output_dir" "$container_id" "$postgres_mode" "$profile_type" || {
		printf 'Invalid PostgreSQL %s profiling results; inspect %s/raw.json and flamegraph.json.\n' "$postgres_mode" "$output_dir" >&2
		return 1
	}
}

usage() {
	printf 'Usage: run_test_postgres_profiling.sh [--oncpu] [--offcpu]\nRun make compose-dev-up first. Defaults: container scope, oncpu, 60 seconds per mode. Both flags run separate trials sequentially and print a results array; each trial has its own output_dir and grafana_url.\n'
}

if [[ ${BASH_SOURCE[0]} == "$0" ]]; then
	postgres_oncpu=false postgres_offcpu=false
	while (($#)); do
		case $1 in
		--oncpu) postgres_oncpu=true ;;
		--offcpu) postgres_offcpu=true ;;
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
	if ! command -v jq > /dev/null; then
		printf '{"status":"failed","grafana_url":null,"error":"Install jq before running this script."}\n'
		exit 1
	fi
	mkdir -p "${ROOT_DIR}/_output/profiling"
	if [[ $postgres_oncpu == true && $postgres_offcpu == true ]]; then
		postgres_output_dir=$(mktemp -d "${ROOT_DIR}/_output/profiling/postgres.XXXXXX")
		postgres_status=0
		# Separate processes retain errexit and the existing per-trial cleanup on failure.
		for postgres_mode in oncpu offcpu; do
			if bash "${BASH_SOURCE[0]}" "--${postgres_mode}" > "${postgres_output_dir}/${postgres_mode}.json"; then
				continue
			else
				postgres_status=1
			fi
		done
		jq -s --argjson status "$postgres_status" --arg output "$postgres_output_dir" \
			'{status:(if $status==0 then "passed" else "failed" end),output_dir:$output,results:.}' \
			"${postgres_output_dir}/oncpu.json" "${postgres_output_dir}/offcpu.json" > "${postgres_output_dir}/result.json"
		cat "${postgres_output_dir}/result.json"
		exit "$postgres_status"
	fi
	postgres_mode=oncpu
	if [[ $postgres_offcpu == true ]]; then postgres_mode=offcpu; fi
	postgres_run_dir=$(mktemp -d "${ROOT_DIR}/_output/profiling/postgres.XXXXXX")
	postgres_started=$SECONDS postgres_namespace='' postgres_job_id='' postgres_benchmark_saved=false
	: > "${postgres_run_dir}/stderr.log"
	trap cleanup EXIT
	trap 'exit 130' INT
	trap 'exit 143' TERM
	printf 'Profiling postgres (%s); logs: %s\n' "$postgres_mode" "$postgres_run_dir" >&2
	main "$postgres_run_dir" > "${postgres_run_dir}/execution.log" 2> "${postgres_run_dir}/stderr.log"
fi
