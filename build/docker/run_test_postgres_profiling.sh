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

# CPU trials use read-only transactions; memory trials allocate across fresh backends.
set -euo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
readonly ROOT_DIR
source "${ROOT_DIR}/build/docker/lib_profiling.sh"
readonly POSTGRES_IMAGE='docker.io/ubuntu/postgres:18-26.04_edge'
readonly POSTGRES_PGBENCH_IMAGE='m.daocloud.io/docker.io/library/postgres:18.7-bookworm'
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
 spec:{nodeName:$node,restartPolicy:"Never",terminationGracePeriodSeconds:5,securityContext:{runAsUser:584792,runAsGroup:584792,fsGroup:584792},
 containers:[{name:"postgres",image:$image,imagePullPolicy:"Never",
 env:[{name:"PGDATA",value:"/pgdata/db"},{name:"POSTGRES_PASSWORD",value:"huatuo-benchmark"}],
 command:["sh","-ec"],args:["
 umask 077
 printf \"%s\\n\" \"$POSTGRES_PASSWORD\" > /tmp/pg-password
 initdb -A scram-sha-256 --pwfile=/tmp/pg-password -U postgres
 printf \"%s\\n\" \"host all all all scram-sha-256\" >> \"$PGDATA/pg_hba.conf\"
 exec postgres -c listen_addresses=\"*\" -c unix_socket_directories=/tmp"],
 resources:{requests:{cpu:"100m",memory:"256Mi"},limits:{cpu:"2",memory:"1Gi"}},
 startupProbe:{exec:{command:["pg_isready","-h","127.0.0.1","-U","postgres","-d","postgres"]},periodSeconds:2,failureThreshold:150},
 volumeMounts:[{name:"data",mountPath:"/pgdata"}]}],volumes:[{name:"data",emptyDir:{sizeLimit:"2Gi"}}]}}
 '
}

write_config_pgbench_pod() {
	local mode=$3 setup options clients=8 hold=0.25 progress=1
	if [[ $mode == physical_usage ]]; then
		# Staggered holds bound backend history without synchronizing all frees.
		hold='2 + :client_id * 0.1'
		progress=3
	fi
	case $mode in
	oncpu | offcpu | irq)
		setup='pgbench -i -s 10'
		options='-S -M prepared'
		;;
	virtual_alloc | physical_alloc | physical_usage)
		# Reconnecting avoids allocator reuse; materialization holds touched pages for snapshots.
		setup="cat > /tmp/memory.sql <<'SQL'
BEGIN;
SET LOCAL work_mem = '16MB';
WITH allocation AS MATERIALIZED (
 SELECT repeat(md5(i::text), 8192) AS payload FROM generate_series(1, 4) AS i
)
SELECT sum(octet_length(payload)), pg_sleep(${hold}) FROM allocation;
COMMIT;
SQL"
		options='-C -f /tmp/memory.sql'
		clients=4
		;;
	esac
	# Cover Pod readiness and API deadlines; the caller stops the load after sampling.
	jq -n --arg node "$1" --arg address "$2" --arg image "$POSTGRES_PGBENCH_IMAGE" --argjson duration "$((POSTGRES_DURATION + 600))" \
		--arg setup "$setup" --arg options "$options" --argjson clients "$clients" --argjson progress "$progress" '{
 apiVersion:"v1",kind:"Pod",metadata:{name:"pgbench"},
 spec:{nodeName:$node,restartPolicy:"Never",terminationGracePeriodSeconds:2,
 containers:[{name:"pgbench",image:$image,imagePullPolicy:"Never",
 env:[{name:"PGHOST",value:$address},{name:"PGUSER",value:"postgres"},{name:"PGDATABASE",value:"postgres"},
 {name:"PGPASSWORD",value:"huatuo-benchmark"},{name:"PGCONNECT_TIMEOUT",value:"5"},{name:"LC_ALL",value:"C"}],
 command:["bash","-euo","pipefail","-c"],args:[
 "\($setup)
 pgbench -n \($options) -c \($clients) -j 2 --exit-on-abort -P \($progress) --progress-timestamp -T \($duration) 2>&1 | tee /tmp/pgbench.log"],
 resources:{requests:{cpu:"100m",memory:"64Mi"},limits:{cpu:"2",memory:"512Mi"}},
 startupProbe:{exec:{command:["grep","-q","^progress:","/tmp/pgbench.log"]},periodSeconds:2,failureThreshold:150}}]}}
 '
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

postgres_result_write() {
	local output_dir=$1 status=$2 job_id=$3 elapsed=$4 result='{}' metadata
	metadata=$(profiling_mode_metadata "$postgres_mode") || return 1
	if ((status == 0)); then
		result=$(profiling_result_write "$output_dir" "$POSTGRES_GRAFANA_BASE" "$POSTGRES_REGION") || return 1
	fi
	jq -n --argjson status "$status" --argjson result "$result" --arg id "$job_id" --argjson metadata "$metadata" \
		--arg image "$POSTGRES_IMAGE" --arg tool "$POSTGRES_PGBENCH_IMAGE" --argjson duration "$POSTGRES_DURATION" \
		--argjson elapsed "$elapsed" --arg output "$output_dir" --rawfile error "${output_dir}/stderr.log" '
 {scope:$metadata.scope,service:"postgres",type:$metadata.type,mode:$metadata.mode,service_image:$image,benchmark_image:$tool,duration_seconds:$duration,
 elapsed_seconds:$elapsed,output_dir:$output,benchmark_log:($output+"/benchmark.log"),request_id:$id,grafana_url:null}
 + (if $status==0 then $result
 else {status:"failed",error:("Profiling failed; inspect "+$output+"/*.log. "+($error|rtrimstr("\n")))} end)'
}

cleanup() {
	local status=$?
	local pods=(postgres)
	trap - EXIT INT TERM
	set +e
	if ! profiling_task_stop_if_active "$postgres_run_dir" "$POSTGRES_API_BASE" "$POSTGRES_API_TOKEN" "$postgres_job_id" >> "${postgres_run_dir}/cleanup.log" 2>&1; then status=1; fi
	if ! profiling_irq_load_stop "$postgres_run_dir" >> "${postgres_run_dir}/cleanup.log" 2>&1; then status=1; fi
	[[ $postgres_benchmark_saved == true ]] || pods+=(pgbench:benchmark)
	if ! kubernetes_namespace_cleanup "$postgres_run_dir" "$postgres_namespace" "${pods[@]}"; then status=1; fi
	if ! postgres_result_write "$postgres_run_dir" "$status" "$postgres_job_id" "$((SECONDS - postgres_started))" > "${postgres_run_dir}/result.json"; then
		status=1
		printf 'Failed to write result to %s/result.json.\n' "$postgres_run_dir" >&4
	elif ! cat "${postgres_run_dir}/result.json" >&3; then
		status=1
		printf 'Failed to print result from %s/result.json.\n' "$postgres_run_dir" >&4
	fi
	((status == 0)) || tail -n 20 "${postgres_run_dir}/stderr.log" >&4
	exit "$status"
}

main() {
	local output_dir=$1 node address namespace container_id cpu='' pod_file
	require_commands kubectl ip curl crictl
	require_endpoints_ready "$output_dir" "${POSTGRES_NODE_BASE}/readyz" "${POSTGRES_API_BASE}/readyz"
	node=$(require_local_kubernetes_node "$output_dir")
	cri_images_ensure_present "$output_dir" "$POSTGRES_CRI_ENDPOINT" "$POSTGRES_IMAGE" "$POSTGRES_PGBENCH_IMAGE"
	namespace="huatuo-postgres-${output_dir##*.}"
	namespace=${namespace,,}
	kubectl --request-timeout=10s create namespace "$namespace" > "${output_dir}/namespace.log"
	# Register ownership only after creation succeeds so EXIT never removes an unrelated namespace.
	postgres_namespace=$namespace
	write_config_postgres_pod "$node" > "${output_dir}/service.request.json"
	kubernetes_pod_create "$output_dir" "$namespace" "${output_dir}/service.request.json"
	pod_file=$(kubernetes_pod_wait_ready "$output_dir" "$namespace" postgres)
	kubernetes_pod_container_get "$pod_file" postgres > "${output_dir}/target.json"
	container_id=$(jq -er .container_id "${output_dir}/target.json")
	address=$(jq -er .pod_ip "${output_dir}/target.json")
	node_container_wait_discovered "$output_dir" "$POSTGRES_NODE_BASE" "$POSTGRES_NODE_TOKEN" "$container_id"
	write_config_pgbench_pod "$node" "$address" "$postgres_mode" > "${output_dir}/benchmark.request.json"
	kubernetes_pod_create "$output_dir" "$namespace" "${output_dir}/benchmark.request.json"
	kubernetes_pod_wait_ready "$output_dir" "$namespace" pgbench > /dev/null
	assert_pgbench_progress "$output_dir" "$namespace" || return 1
	if [[ $postgres_mode == irq ]]; then
		profiling_irq_load_start "$output_dir" "$POSTGRES_DURATION"
		cpu=$(cat "${output_dir}/irq-cpu.txt")
		assert_irq_load_active "$output_dir"
	fi
	write_config_profiling_request "$POSTGRES_TARGET_HOST" "$container_id" "$POSTGRES_DURATION" "$postgres_mode" "$cpu" > "${output_dir}/create.request.json"
	postgres_job_id=$(profiling_task_create "$output_dir" "$POSTGRES_API_BASE" "$POSTGRES_API_TOKEN" "${output_dir}/create.request.json" | jq -er .request_id)
	profiling_task_wait_completed "$output_dir" "$POSTGRES_API_BASE" "$POSTGRES_API_TOKEN" "$postgres_job_id" "$((POSTGRES_DURATION + 120))" > "${output_dir}/task.json"
	assert_pgbench_progress "$output_dir" "$namespace" || return 1
	kubectl --request-timeout=10s -n "$namespace" get pod pgbench -o json > "${output_dir}/benchmark-at-exit.json"
	postgres_benchmark_saved=true
	kubernetes_pod_delete "$output_dir" "$namespace" pgbench
	profiling_raw_query_all "$output_dir" "$POSTGRES_API_BASE" "$POSTGRES_API_TOKEN" "$postgres_job_id" > "${output_dir}/raw.json"
	if [[ $postgres_mode == irq ]]; then
		assert_irq_load_active "$output_dir"
		assert_irq_profile "$output_dir"
	else
		assert_native_profile "$output_dir" "$container_id" "$postgres_mode" 3
	fi
	write_config_profile_query "${output_dir}/task.json" "$postgres_mode" > "${output_dir}/profile.json"
	grafana_flamegraph_query "$output_dir" "$POSTGRES_GRAFANA_BASE" "$POSTGRES_GRAFANA_AUTH" "$POSTGRES_REGION" "${output_dir}/profile.json" > "${output_dir}/flamegraph.json"
	assert_grafana_flamegraph "${output_dir}/flamegraph.json" || {
		printf 'Invalid PostgreSQL %s profiling results; inspect %s/raw.json and flamegraph.json.\n' "$postgres_mode" "$output_dir" >&2
		return 1
	}
}

usage() {
	printf '%s\n' \
		'Usage: run_test_postgres_profiling.sh [MODE...]' \
		'Modes: --oncpu --offcpu --virtual-alloc --physical-alloc --physical-usage --irq' \
		'Default: --oncpu, 60s per mode. Modes can be combined.' \
		'Scope: --irq = host (one CPU); others = container.' \
		'Setup: make compose-dev-up'
}

if [[ ${BASH_SOURCE[0]} == "$0" ]]; then
	for postgres_argument in "$@"; do
		case $postgres_argument in --help | -h)
			usage
			exit 0
			;;
		esac
	done
	if ! postgres_selection=$(profiling_native_modes_parse "$@"); then
		usage >&2
		exit 2
	fi
	mapfile -t postgres_modes <<< "$postgres_selection"
	if ! command -v jq > /dev/null; then
		printf '{"status":"failed","grafana_url":null,"error":"Install jq before running this script."}\n'
		exit 1
	fi
	mkdir -p "${ROOT_DIR}/_output/profiling"
	if ((${#postgres_modes[@]} > 1)); then
		postgres_output_dir=$(mktemp -d "${ROOT_DIR}/_output/profiling/postgres.XXXXXX")
		postgres_status=0
		trap 'postgres_status=130' INT
		trap 'postgres_status=143' TERM
		postgres_batch_started=$SECONDS
		postgres_results=()
		# Separate processes retain errexit and the existing per-trial cleanup on failure.
		for postgres_mode in "${postgres_modes[@]}"; do
			postgres_results+=("${postgres_output_dir}/${postgres_mode}.json")
			if ! bash "${BASH_SOURCE[0]}" "--${postgres_mode//_/-}" > "${postgres_output_dir}/${postgres_mode}.json"; then
				if ((postgres_status == 0)); then postgres_status=1; fi
			fi
			((postgres_status < 128)) || break
		done
		profiling_results_write "$postgres_output_dir" "$((SECONDS - postgres_batch_started))" "$postgres_status" "$POSTGRES_GRAFANA_BASE" "$POSTGRES_REGION" false "${postgres_results[@]}" > "${postgres_output_dir}/result.json"
		cat "${postgres_output_dir}/result.json"
		exit "$postgres_status"
	fi
	postgres_mode=${postgres_modes[0]}
	postgres_run_dir=$(mktemp -d "${ROOT_DIR}/_output/profiling/postgres.XXXXXX")
	postgres_started=$SECONDS postgres_namespace='' postgres_job_id='' postgres_benchmark_saved=false
	: > "${postgres_run_dir}/stderr.log"
	# Preserve result output when a signal interrupts the redirected main function.
	exec 3>&1 4>&2
	trap cleanup EXIT
	trap 'exit 130' INT
	trap 'exit 143' TERM
	printf 'Profiling postgres (%s); logs: %s\n' "$postgres_mode" "$postgres_run_dir" >&2
	main "$postgres_run_dir" > "${postgres_run_dir}/execution.log" 2> "${postgres_run_dir}/stderr.log"
fi
