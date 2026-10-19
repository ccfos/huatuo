#!/usr/bin/env bash
# Copyright 2026 The HuaTuo Authors.
# SPDX-License-Identifier: Apache-2.0
# Shared setup, assertions and cleanup for apiserver integration and e2e cases.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_storage.sh"
source "${ROOT_DIR}/integration/config.sh"
source "${ROOT_DIR}/integration/lib_continuous_profiling.sh"

readonly API_TOKEN=integration-admin OTHER_API_TOKEN=integration-other
readonly ES_PASSWORD=huatuo-integration PROFILE_INTERVAL=5
CURL_TIMEOUT=(--connect-timeout 2 --max-time 5)
TARGET_PID=""
LOAD_PID=""
POD_NAMESPACE=""
ORPHAN_PIDS=()
REQUEST_NO=0
APISERVER_GENERATION=0
BAMAI_GENERATION=0
WITH_STORAGE=true
MAX_HOST=3
MAX_TOTAL=500
NODE_CAPACITY=10
readonly SUMMARY="${HUATUO_BAMAI_TEST_TMPDIR}/summary.tsv"
printf 'case\tresult\n' > "$SUMMARY"
CURRENT_CASE=setup
stop_orphans() {
	local pid attempt
	for pid in "${ORPHAN_PIDS[@]}"; do
		kill -TERM -- "-$pid" 2> /dev/null || continue
		for ((attempt = 0; attempt < 5; attempt++)); do
			kill -0 -- "-$pid" 2> /dev/null || break
			sleep 1
		done
		kill -KILL -- "-$pid" 2> /dev/null || true
	done
	ORPHAN_PIDS=()
}
cleanup() {
	local status=$?
	trap - EXIT
	set +e
	[[ -z "$LOAD_PID" ]] || {
		kill -TERM "$LOAD_PID" 2> /dev/null
		wait "$LOAD_PID"
	}
	[[ -z "$TARGET_PID" ]] || {
		stop_by_pid "$TARGET_PID" 5
		wait "$TARGET_PID"
	}
	stop_orphans
	huatuo_apiserver_stop || status=1
	huatuo_bamai_stop || status=1
	if [[ -n "$POD_NAMESPACE" ]]; then
		kubectl --request-timeout=30s -n "$POD_NAMESPACE" get pods -o yaml > "${HUATUO_BAMAI_TEST_TMPDIR}/pods-at-exit.yaml"
		kubectl --request-timeout=30s -n "$POD_NAMESPACE" get events > "${HUATUO_BAMAI_TEST_TMPDIR}/pod-events.txt"
		kubectl --request-timeout=30s -n "$POD_NAMESPACE" logs workload > "${HUATUO_BAMAI_TEST_TMPDIR}/pod-at-exit.log" 2>&1
		kubectl --request-timeout=30s delete namespace "$POD_NAMESPACE" --wait=false || status=1
	fi
	if [[ -n "$STORAGE_CONTAINER_ID" ]]; then
		storage_dump_logs
		storage_stop || status=1
	fi
	if ((status != 0)); then
		printf '%s\t%s\n' "$CURRENT_CASE" "$([[ $status == 77 ]] && echo SKIP || echo FAIL)" >> "$SUMMARY"
	fi
	log_info "reports retained: ${HUATUO_BAMAI_TEST_TMPDIR}"
	exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
pass() {
	printf '%s\tPASS\n' "$CURRENT_CASE" >> "$SUMMARY"
	log_info "PASS: $CURRENT_CASE"
}

continuous_profiling_requirements
require_commands curl python3 go gcc ss pgrep
require_build_output
require_commands "$HUATUO_APISERVER_BIN" "$HUATUO_BAMAI_BIN"
APISERVER_PORT=$(allocate_available_port)
NODE_PORT=$(allocate_available_port)
while [[ $NODE_PORT == "$APISERVER_PORT" ]]; do NODE_PORT=$(allocate_available_port); done
APISERVER_ADDR="http://127.0.0.1:${APISERVER_PORT}"
HUATUO_BAMAI_ADDR="http://127.0.0.1:${NODE_PORT}"
HUATUO_BAMAI_METRICS_API="${HUATUO_BAMAI_ADDR}/metrics"
export APISERVER_ADDR API_TOKEN

go build -o "${HUATUO_BAMAI_TEST_TMPDIR}/probe" ./integration/apiserver/probe.go
PROBE="${HUATUO_BAMAI_TEST_TMPDIR}/probe"

write_server_config() {
	write_apiserver_apis_config
	cat >> "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.conf" << CONFIG
[Agent]
    HTTPPort = ${NODE_PORT}
[Jobs.Profiling]
    MaxConcurrentPerHost = ${MAX_HOST}
    MaxConcurrent = ${MAX_TOTAL}
[Jobs.Controller]
    StatusPollIntervalSeconds = 1
    PendingTimeoutSeconds = 10
    CompletionGracePeriodSeconds = 20
    NodeUnavailableGracePeriodSeconds = 5
[Profiling]
    DashboardBaseURL = "http://grafana.invalid/d"
CONFIG
	if [[ $WITH_STORAGE == true ]]; then
		cat >> "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.conf" << CONFIG
[Elasticsearch]
    Address = "${STORAGE_ADDR}"
    Username = "elastic"
    Password = "${ES_PASSWORD}"
    Index = "huatuo_continuous_profiling_test"
CONFIG
	fi
}
write_node_config() {
	write_continuous_profiling_bamai_config
	# External runtimes must use the same bundled tools as direct profiler tests.
	sed -i '/MaxConcurrentProcesses = 10/a\    ToolDir = "'"${PROFILER_TOOL_DIR}"'"' "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf"
	cat >> "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" << CONFIG
[HTTPServer]
    ListenAddress = "127.0.0.1:${NODE_PORT}"
[Operations]
    MaxConcurrent = ${NODE_CAPACITY}
CONFIG
	if [[ -n "$POD_NAMESPACE" ]]; then
		cat >> "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" << CONFIG
[Pod]
    KubeletReadOnlyPort = 0
    KubeletAuthorizedPort = 10250
    KubeletClientCertPath = "${KUBELET_CERT},${KUBELET_KEY}"
CONFIG
	fi
}
start_server() {
	[[ ! -f "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.log" ]] || cp "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.log" "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver-${APISERVER_GENERATION}.log"
	APISERVER_GENERATION=$((APISERVER_GENERATION + 1))
	integration_huatuo_apiserver_start write_server_config
}
start_node() {
	[[ ! -f "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" ]] || cp "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${HUATUO_BAMAI_TEST_TMPDIR}/bamai-${BAMAI_GENERATION}.log"
	BAMAI_GENERATION=$((BAMAI_GENERATION + 1))
	local args=(--region "${TEST_LOG_TAG,,}")
	[[ -n "$POD_NAMESPACE" ]] || args+=(--disable-kubelet)
	integration_huatuo_bamai_start write_node_config "${args[@]}"
}
request() {
	local method=$1 path=$2 expected=$3 body=${4:-}
	REQUEST_NO=$((REQUEST_NO + 1))
	RESPONSE="${HUATUO_BAMAI_TEST_TMPDIR}/request-${REQUEST_NO}.json"
	local args=() status
	[[ -z "$body" ]] || args=(-H 'Content-Type: application/json' --data-binary "$body")
	printf '%s %s\n%s\n' "$method" "$path" "$body" > "${RESPONSE%.json}.request"
	status=$(curl -sS "${CURL_TIMEOUT[@]}" -X "$method" -H "Authorization: Bearer ${API_TOKEN}" \
		"${args[@]}" -o "$RESPONSE" -w '%{http_code}' "${APISERVER_ADDR}${path}")
	assert_eq "$status" "$expected" "$method $path" || fatal "response: $(cat "$RESPONSE")"
}
create() {
	local duration=${1:-12} extra=${2:-'{}'} body
	body=$(jq -nc --argjson duration "$duration" --argjson extra "$extra" \
		'{hostname:"127.0.0.1",duration_seconds:$duration,scope:"host",type:"cpu",language:"c",mode:"oncpu"} + $extra')
	request POST /v1/profiling 201 "$body"
	JOB_ID=$(jq -er '.data.request_id' "$RESPONSE")
}
job_reached() {
	local id=$1 outcome=$2 file="${HUATUO_BAMAI_TEST_TMPDIR}/status-$1.json"
	continuous_profile_status_is "$id" "$outcome" "$file" && return 0
	if jq -e '.data.status == "terminal"' "$file" > /dev/null 2>&1; then
		fatal "job $id reached an unexpected terminal state: $(cat "$file")"
	fi
	return 1
}
wait_job() {
	wait_until 90 1 job_reached "$1" "$2" || fatal "job $1 did not reach $2"
}

assert_error() { jq -e --arg code "$1" '.error.code == $code' "$RESPONSE" > /dev/null || fatal "unexpected error: $(cat "$RESPONSE")"; }
assert_reason() { jq -e --arg reason "$2" '.data.terminal.reason == $reason' "${HUATUO_BAMAI_TEST_TMPDIR}/status-$1.json" > /dev/null || fatal "job $1 did not fail with $2"; }
verify_result() {
	local id=$1 minimum=${2:-2} expected_type=${3:-process_cpu:cpu:nanoseconds:cpu:nanoseconds}
	wait_job "$id" completed
	request GET "/v1/profiling/$id/raw?limit=100" 200
	cp "$RESPONSE" "${HUATUO_BAMAI_TEST_TMPDIR}/raw-${id}.json"
	jq -e --argjson minimum "$minimum" '(.data.items | length) >= $minimum and all(.data.items[]; .profile != null and .profile_type != null)' "$RESPONSE" > /dev/null || fatal "missing profiling windows for $id"
	jq -e --arg expected "$expected_type" 'all(.data.items[]; .profile_type == $expected)' "$RESPONSE" > /dev/null || fatal "unexpected profile type for $id: expected $expected_type"
	"$PROBE" flamegraph "$APISERVER_ADDR" "$id" "$RESPONSE" "$expected_type" > "${HUATUO_BAMAI_TEST_TMPDIR}/flamegraph-${id}.json"
}
stop_job() {
	request POST "/v1/profiling/$1/stop" 200
	jq -e '.data.status == "stopping" and .data.terminal == null' "$RESPONSE" > /dev/null || fatal 'stop did not return stopping state'
	wait_job "$1" stopped
}

start_test_storage() {
	local image=${STORAGE_TEST_IMAGE:-$STORAGE_DEFAULT_IMAGE} port
	if ! docker image inspect "$image" > /dev/null 2>&1; then
		timeout 5m docker pull "$image" > "${HUATUO_BAMAI_TEST_TMPDIR}/elasticsearch-pull.log" 2>&1 || skip "cannot pull $image"
	fi
	# Keep ES data on the report filesystem; Docker's writable layer may be full.
	mkdir "${HUATUO_BAMAI_TEST_TMPDIR}/elasticsearch-data"
	chown 1000:0 "${HUATUO_BAMAI_TEST_TMPDIR}/elasticsearch-data"
	chmod 0770 "${HUATUO_BAMAI_TEST_TMPDIR}/elasticsearch-data"
	STORAGE_CONTAINER_ID=$(docker run --detach --publish 127.0.0.1::9200 \
		--mount "type=bind,source=${HUATUO_BAMAI_TEST_TMPDIR}/elasticsearch-data,target=/usr/share/elasticsearch/data" \
		--env discovery.type=single-node --env xpack.security.enabled=false \
		--env ES_JAVA_OPTS='-Xms512m -Xmx512m' "$image")
	port=$(docker port "$STORAGE_CONTAINER_ID" 9200/tcp | awk -F: 'NR == 1 {print $NF}')
	[[ -n "$port" ]] || fatal 'ES container has no published port'
	STORAGE_ADDR="http://127.0.0.1:$port"
	wait_until 120 2 storage_ready || fatal "ES not ready at $STORAGE_ADDR"
}
