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

# Keep benchmark traffic separate from the development stack's profile storage.
set -euo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
readonly ROOT_DIR
source "${ROOT_DIR}/build/docker/lib_profiling.sh"
readonly ELASTICSEARCH_IMAGE='docker.elastic.co/elasticsearch/elasticsearch:8.15.5'
readonly ELASTICSEARCH_RALLY_IMAGE='elastic/rally:2.12.0'
readonly ELASTICSEARCH_DURATION=60
readonly ELASTICSEARCH_WARMUP=30
readonly ELASTICSEARCH_DOCUMENTS=5000
readonly ELASTICSEARCH_JAVA_ENTRY='org.elasticsearch.server/org.elasticsearch.bootstrap.Elasticsearch'
readonly ELASTICSEARCH_HTTP_PORT=19200
readonly ELASTICSEARCH_TRANSPORT_PORT=19300
readonly ELASTICSEARCH_API_BASE='http://127.0.0.1:12740'
readonly ELASTICSEARCH_API_TOKEN='huatuo-dev-api-token'
readonly ELASTICSEARCH_NODE_BASE='http://127.0.0.1:19704'
readonly ELASTICSEARCH_NODE_TOKEN='huatuo-dev-node-token'
readonly ELASTICSEARCH_GRAFANA_BASE='http://127.0.0.1:3000'
readonly ELASTICSEARCH_GRAFANA_AUTH='admin:admin'
readonly ELASTICSEARCH_TARGET_HOST='huatuo-dev-node'
readonly ELASTICSEARCH_REGION='pre-test'
readonly ELASTICSEARCH_CRI_ENDPOINT=${CONTAINER_RUNTIME_ENDPOINT:-unix:///run/containerd/containerd.sock}

write_config_elasticsearch_pod() {
	jq -n --arg node "$1" --arg image "$ELASTICSEARCH_IMAGE" \
		--arg http "$ELASTICSEARCH_HTTP_PORT" --arg transport "$ELASTICSEARCH_TRANSPORT_PORT" '{
 apiVersion:"v1",kind:"Pod",metadata:{name:"elasticsearch"},
 spec:{nodeName:$node,restartPolicy:"Never",terminationGracePeriodSeconds:10,securityContext:{fsGroup:1000},
 containers:[{name:"elasticsearch",image:$image,imagePullPolicy:"Never",
 args:["eswrapper","-Ecluster.name=huatuo-profiling-test","-Ediscovery.type=single-node","-Enetwork.host=0.0.0.0",
 "-Ehttp.port="+$http,"-Etransport.port="+$transport,"-Expack.security.enabled=false",
 "-Expack.ml.enabled=false","-Enode.store.allow_mmap=false"],
 env:[{name:"ES_JAVA_OPTS",value:"-Xms1g -Xmx1g -XX:+UnlockDiagnosticVMOptions -XX:+DebugNonSafepoints"}],
 resources:{requests:{cpu:"100m",memory:"1Gi"},limits:{cpu:"2",memory:"3Gi"}},
 startupProbe:{exec:{command:["bash","-ec","curl --noproxy localhost -fsS --max-time 5 \"http://localhost:"+$http+"/_cluster/health?wait_for_status=yellow&timeout=3s\""]},periodSeconds:2,timeoutSeconds:6,failureThreshold:150},
 volumeMounts:[{name:"data",mountPath:"/usr/share/elasticsearch/data"}]}],volumes:[{name:"data",emptyDir:{sizeLimit:"2Gi"}}]}}
 '
}

write_config_rally_track() {
	# Finish a bounded race naturally so its report covers errors and throughput.
	jq -n --argjson duration "$((ELASTICSEARCH_DURATION + ELASTICSEARCH_WARMUP + 60))" \
		--argjson documents "$ELASTICSEARCH_DOCUMENTS" '{
 version:2,description:"Local Elasticsearch Java CPU profiling workload",
 indices:[{name:"huatuo-benchmark",body:"index.json"}],
 corpora:[{name:"local",documents:[{"source-file":"documents.json","document-count":$documents}]}],
 schedule:[
 {operation:{"operation-type":"create-index"}},
 {operation:{"operation-type":"bulk","bulk-size":500},clients:1},
 {operation:{"operation-type":"refresh"}},
 {operation:{name:"query-and-aggregate","operation-type":"search",index:"huatuo-benchmark",cache:false,
 body:{size:10,track_total_hits:true,query:{match:{message:"java cpu profiling"}},
 aggs:{categories:{terms:{field:"category",size:50},aggs:{prices:{stats:{field:"price"}}}}}}},
 clients:4,"time-period":$duration}]}
 '
}

write_config_rally_pod() {
	jq -n --arg node "$1" --arg address "$2" --arg image "$ELASTICSEARCH_RALLY_IMAGE" '{
 apiVersion:"v1",kind:"Pod",metadata:{name:"rally"},
 spec:{nodeName:$node,restartPolicy:"Never",terminationGracePeriodSeconds:10,securityContext:{fsGroup:1000},
 containers:[{name:"rally",image:$image,imagePullPolicy:"Never",command:["/bin/sh","-ec"],args:[
 "cp /source/* /rally/track/
 esrally race --pipeline=benchmark-only --track-path=/rally/track --target-hosts=\"$ES_TARGET\" --offline --on-error=abort --kill-running-processes --report-format=csv --report-file=/rally/output/report.csv
 echo HUATUO_RALLY_REPORT_BEGIN
 cat /rally/output/report.csv
 echo HUATUO_RALLY_REPORT_END"],
 env:[{name:"ES_TARGET",value:$address},{name:"PYTHONUNBUFFERED",value:"1"}],
 resources:{requests:{cpu:"100m",memory:"256Mi"},limits:{cpu:"2",memory:"1Gi"}},
 readinessProbe:{exec:{command:["python3","-c",
 "import json,os,urllib.request; u=\"http://\"+os.environ[\"ES_TARGET\"]+\"/huatuo-benchmark/_stats/search\"; r=json.load(urllib.request.build_opener(urllib.request.ProxyHandler({})).open(u,timeout=3)); raise SystemExit(0 if r[\"_all\"][\"primaries\"][\"search\"][\"query_total\"]>0 else 1)"]},periodSeconds:2,timeoutSeconds:5},
 volumeMounts:[{name:"source",mountPath:"/source",readOnly:true},{name:"track",mountPath:"/rally/track"},
 {name:"state",mountPath:"/rally/.rally"},{name:"output",mountPath:"/rally/output"}]}],
 volumes:[{name:"source",configMap:{name:"rally-track"}},{name:"track",emptyDir:{}},
 {name:"state",emptyDir:{sizeLimit:"512Mi"}},{name:"output",emptyDir:{sizeLimit:"64Mi"}}]}}
 '
}

rally_start() {
	local output_dir=$1 namespace=$2 node=$3 address=$4
	mkdir -p "${output_dir}/track"
	write_config_rally_track > "${output_dir}/track/track.json"
	jq -n '{settings:{number_of_shards:1,number_of_replicas:0,"index.queries.cache.enabled":false},
 mappings:{dynamic:"strict",properties:{message:{type:"text"},category:{type:"keyword"},price:{type:"integer"}}}}' \
		> "${output_dir}/track/index.json"
	jq -nc --argjson count "$ELASTICSEARCH_DOCUMENTS" 'range($count) | {
 message:("java cpu profiling elasticsearch document "+tostring),category:("category-"+(.%50|tostring)),price:(.%1000)}' \
		> "${output_dir}/track/documents.json"
	kubectl --request-timeout=10s -n "$namespace" create configmap rally-track \
		--from-file="${output_dir}/track" > "${output_dir}/configmap.log"
	write_config_rally_pod "$node" "$address" > "${output_dir}/benchmark.request.json"
	kubernetes_pod_create "$output_dir" "$namespace" "${output_dir}/benchmark.request.json"
	kubernetes_pod_wait_ready "$output_dir" "$namespace" rally > /dev/null
}

elasticsearch_query_count_read() {
	local output_dir=$1 address=$2
	http_request "$output_dir" benchmark-stats "http://${address}/huatuo-benchmark/_stats/search" --noproxy '*' > /dev/null || return 1
	jq -er '._all.primaries.search.query_total | select(type=="number" and .>0)' "${output_dir}/benchmark-stats.response.json"
}

elasticsearch_progress_read() {
	local output_dir=$1 namespace=$2 address=$3 previous=$4 current pod
	for pod in elasticsearch rally; do
		kubectl --request-timeout=10s -n "$namespace" get pod "$pod" -o json > "${output_dir}/${pod}.response.json" || return 1
		jq -e '.status.phase=="Running" and (.status.containerStatuses | length==1 and all(.[]; .ready and .restartCount==0))' \
			"${output_dir}/${pod}.response.json" > /dev/null || {
			printf 'Expected running %s without restarts; inspect %s/%s.response.json and logs.\n' "$pod" "$output_dir" "$pod" >&2
			return 1
		}
	done
	current=$(elasticsearch_query_count_read "$output_dir" "$address") || return 1
	if ((current <= previous)); then
		printf 'Expected Elasticsearch query count above %s, got %s; inspect %s/benchmark-stats.response.json.\n' "$previous" "$current" "$output_dir" >&2
		return 1
	fi
	printf '%s\n' "$current"
}

rally_report_save() {
	local output_dir=$1 namespace=$2
	kubectl --request-timeout=190s -n "$namespace" wait --for=jsonpath='{.status.phase}'=Succeeded pod/rally --timeout=180s \
		> "${output_dir}/benchmark-completion.log" 2>&1 || {
		printf 'Rally did not complete successfully; inspect %s/benchmark-completion.log and benchmark.log.\n' "$output_dir" >&2
		return 1
	}
	kubectl --request-timeout=10s -n "$namespace" logs rally > "${output_dir}/benchmark.log"
	awk '/^HUATUO_RALLY_REPORT_BEGIN$/ { report=1; next } /^HUATUO_RALLY_REPORT_END$/ { report=0 } report' \
		"${output_dir}/benchmark.log" > "${output_dir}/benchmark-report.csv"
	awk -F, 'tolower($1)=="error rate" { count++; if ($3 !~ /^[0-9]+([.][0-9]+)?$/ || $3+0 != 0) failed=1 }
 END { exit (count==0 || failed) }' "${output_dir}/benchmark-report.csv" || {
		printf 'Expected Rally report with zero query errors; inspect %s/benchmark-report.csv and benchmark.log.\n' "$output_dir" >&2
		return 1
	}
}

assert_elasticsearch_profile() {
	local output_dir=$1 container_id=$2
	assert_profile_windows "${output_dir}/raw.json" 1 'process_cpu:cpu:nanoseconds:cpu:nanoseconds' "$container_id" || return 1
	jq -e '
 any(.items[].profile; . as $p
 | (reduce .function[] as $f ({}; .[$f.id|tostring]=$p.string_table[$f.name])) as $functions
 | (reduce .location[] as $l ({}; .[$l.id|tostring]=any($l.line[]?;
     $functions[.function_id|tostring] | test("org[./](elasticsearch|apache[./]lucene)[./]")))) as $locations
 | any(.sample[]?; any(.value[]?; .>0) and any(.location_id[]?; $locations[tostring])))' "${output_dir}/raw.json" > /dev/null || {
		printf 'Expected positive Java CPU samples with Elasticsearch/Lucene methods; inspect %s/raw.json.\n' "$output_dir" >&2
		return 1
	}
}

elasticsearch_result_write() {
	local output_dir=$1 status=$2 elapsed=$3 result='{}'
	if ((status == 0)); then
		result=$(profiling_result_write "$output_dir" "$ELASTICSEARCH_GRAFANA_BASE" "$ELASTICSEARCH_REGION") || return 1
	fi
	jq -n --argjson status "$status" --argjson result "$result" --arg id "$elasticsearch_job_id" \
		--arg image "$ELASTICSEARCH_IMAGE" --arg tool "$ELASTICSEARCH_RALLY_IMAGE" --argjson duration "$ELASTICSEARCH_DURATION" \
		--argjson elapsed "$elapsed" --arg output "$output_dir" --argjson documents "$ELASTICSEARCH_DOCUMENTS" \
		--rawfile error "${output_dir}/stderr.log" '
 {scope:"container",service:"elasticsearch",language:"java",mode:"oncpu",service_image:$image,benchmark_image:$tool,
 duration_seconds:$duration,elapsed_seconds:$elapsed,documents:$documents,clients:4,http_port:19200,transport_port:19300,
 output_dir:$output,benchmark_log:($output+"/benchmark.log"),benchmark_report:($output+"/benchmark-report.csv"),request_id:$id,grafana_url:null}
 + (if $status==0 then $result
 else {status:"failed",error:("Profiling failed; inspect "+$output+"/*.log. "+($error|rtrimstr("\n")))} end)'
}

cleanup() {
	local status=$?
	trap - EXIT INT TERM
	set +e
	if ! profiling_task_stop_if_active "$elasticsearch_run_dir" "$ELASTICSEARCH_API_BASE" "$ELASTICSEARCH_API_TOKEN" "$elasticsearch_job_id" >> "${elasticsearch_run_dir}/cleanup.log" 2>&1; then status=1; fi
	if ! kubernetes_namespace_cleanup "$elasticsearch_run_dir" "$elasticsearch_namespace" elasticsearch rally:benchmark; then status=1; fi
	if ! elasticsearch_result_write "$elasticsearch_run_dir" "$status" "$((SECONDS - elasticsearch_started))" > "${elasticsearch_run_dir}/result.json"; then
		status=1
		printf 'Failed to write result to %s/result.json.\n' "$elasticsearch_run_dir" >&4
	elif ! cat "${elasticsearch_run_dir}/result.json" >&3; then
		status=1
	fi
	((status == 0)) || tail -n 20 "${elasticsearch_run_dir}/stderr.log" >&4
	exit "$status"
}

main() {
	local output_dir=$1 node namespace container_id address queries deadline pod_file
	require_commands kubectl ip curl crictl
	require_endpoints_ready "$output_dir" "${ELASTICSEARCH_NODE_BASE}/readyz" "${ELASTICSEARCH_API_BASE}/readyz"
	node=$(require_local_kubernetes_node "$output_dir")
	cri_images_ensure_present "$output_dir" "$ELASTICSEARCH_CRI_ENDPOINT" "$ELASTICSEARCH_IMAGE" "$ELASTICSEARCH_RALLY_IMAGE"
	namespace="huatuo-elasticsearch-${output_dir##*.}"
	namespace=${namespace,,}
	kubectl --request-timeout=10s create namespace "$namespace" > "${output_dir}/namespace.log"
	# Register ownership only after creation so cleanup cannot remove another run.
	elasticsearch_namespace=$namespace
	write_config_elasticsearch_pod "$node" > "${output_dir}/service.request.json"
	kubernetes_pod_create "$output_dir" "$namespace" "${output_dir}/service.request.json"
	pod_file=$(kubernetes_pod_wait_ready "$output_dir" "$namespace" elasticsearch)
	kubernetes_pod_container_get "$pod_file" elasticsearch > "${output_dir}/target.json"
	container_id=$(jq -er .container_id "${output_dir}/target.json")
	address=$(jq -er '.pod_ip | if contains(":") then "["+.+"]" else . end' "${output_dir}/target.json")
	address="${address}:${ELASTICSEARCH_HTTP_PORT}"
	http_request "$output_dir" service-info "http://${address}/" --noproxy '*' > /dev/null
	node_container_wait_discovered "$output_dir" "$ELASTICSEARCH_NODE_BASE" "$ELASTICSEARCH_NODE_TOKEN" "$container_id"
	rally_start "$output_dir" "$namespace" "$node" "$address"
	queries=$(elasticsearch_query_count_read "$output_dir" "$address")
	deadline=$((SECONDS + ELASTICSEARCH_WARMUP))
	while ((SECONDS < deadline)); do
		sleep 5
		queries=$(elasticsearch_progress_read "$output_dir" "$namespace" "$address" "$queries")
	done
	cp "${output_dir}/benchmark-stats.response.json" "${output_dir}/benchmark-before.json"
	# The idle launcher is a parent JVM; filter for the server before grouping processes.
	jq -n --arg entry "$ELASTICSEARCH_JAVA_ENTRY" --arg host "$ELASTICSEARCH_TARGET_HOST" --arg container "$container_id" --argjson duration "$ELASTICSEARCH_DURATION" \
		'{type:"cpu",language:"java",mode:"oncpu",scope:"container",hostname:$host,container_id:$container,duration_seconds:$duration,binary_match_path:$entry}' \
		> "${output_dir}/create.request.json"
	elasticsearch_job_id=$(profiling_task_create "$output_dir" "$ELASTICSEARCH_API_BASE" "$ELASTICSEARCH_API_TOKEN" "${output_dir}/create.request.json" | jq -er .request_id)
	profiling_task_wait_completed "$output_dir" "$ELASTICSEARCH_API_BASE" "$ELASTICSEARCH_API_TOKEN" "$elasticsearch_job_id" "$((ELASTICSEARCH_DURATION + 120))" > "${output_dir}/task.json"
	elasticsearch_progress_read "$output_dir" "$namespace" "$address" "$queries" > /dev/null
	cp "${output_dir}/benchmark-stats.response.json" "${output_dir}/benchmark-after.json"
	profiling_raw_query "$output_dir" "$ELASTICSEARCH_API_BASE" "$ELASTICSEARCH_API_TOKEN" "$elasticsearch_job_id" > "${output_dir}/raw.json"
	assert_elasticsearch_profile "$output_dir" "$container_id"
	write_config_profile_query "${output_dir}/task.json" oncpu | jq 'del(.type)' > "${output_dir}/profile.json"
	grafana_flamegraph_query "$output_dir" "$ELASTICSEARCH_GRAFANA_BASE" "$ELASTICSEARCH_GRAFANA_AUTH" "$ELASTICSEARCH_REGION" "${output_dir}/profile.json" > "${output_dir}/flamegraph.json"
	assert_grafana_flamegraph "${output_dir}/flamegraph.json"
	rally_report_save "$output_dir" "$namespace"
}

if [[ ${BASH_SOURCE[0]} == "$0" ]]; then
	if [[ ${1:-} == --help && $# == 1 ]]; then
		printf '%s\n' \
			'Usage: run_test_elasticsearch_profiling.sh [--help]' \
			'Profile: Java oncpu, 60s, container scope.' \
			'Setup: make install-tools && make compose-dev-up'
		exit 0
	fi
	if (($# != 0)); then
		printf 'Usage: run_test_elasticsearch_profiling.sh [--help]\n' >&2
		exit 2
	fi
	if ! command -v jq > /dev/null; then
		printf '{"status":"failed","grafana_url":null,"error":"Install jq before running this script."}\n'
		exit 1
	fi
	mkdir -p "${ROOT_DIR}/_output/profiling"
	elasticsearch_run_dir=$(mktemp -d "${ROOT_DIR}/_output/profiling/elasticsearch.XXXXXX")
	elasticsearch_started=$SECONDS elasticsearch_namespace='' elasticsearch_job_id=''
	: > "${elasticsearch_run_dir}/stderr.log"
	# Signals can run cleanup while main still redirects its output to log files.
	exec 3>&1 4>&2
	trap cleanup EXIT
	trap 'exit 130' INT
	trap 'exit 143' TERM
	printf 'Profiling Elasticsearch (Java oncpu); logs: %s\n' "$elasticsearch_run_dir" >&2
	main "$elasticsearch_run_dir" > "${elasticsearch_run_dir}/execution.log" 2> "${elasticsearch_run_dir}/stderr.log"
fi
