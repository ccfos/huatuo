#!/usr/bin/env bash

# Copyright 2026 The HuaTuo Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Verify asynchronous stop and discard, the global Node Operation limit, and
# the stable response for a Tracing service without an executor.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_storage.sh"
source "${ROOT_DIR}/integration/config.sh"
source "${ROOT_DIR}/integration/lib_continuous_profiling.sh"

readonly ES_PASSWORD="huatuo-integration"
readonly API_TOKEN="integration-admin"
readonly OTHER_API_TOKEN="integration-other"
readonly PROFILE_DURATION=30
readonly PROFILE_INTERVAL=5
readonly PROFILE_INDEX="huatuo_continuous_profiling_test"

continuous_profiling_requirements

APISERVER_PORT=$(allocate_available_port) \
	|| fatal "failed to allocate an apiserver port"
readonly APISERVER_PORT
readonly APISERVER_ADDR="http://127.0.0.1:${APISERVER_PORT}"

TARGET_PID=""
RUNNING_PROFILE_ID=""
CAPACITY_PROFILE_ID=""

cleanup() {
	local status=$?
	continuous_profiling_cleanup "${status}" "${TARGET_PID}"
}
trap cleanup EXIT

write_control_bamai_config() {
	write_continuous_profiling_bamai_config
	cat >> "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" << 'EOF'

[Operations]
    MaxConcurrent = 1
EOF
}

start_control_stack() {
	elasticsearch_start
	integration_huatuo_bamai_start \
		write_control_bamai_config \
		--region integration \
		--disable-kubelet \
		--log-debug
	integration_huatuo_apiserver_start \
		write_continuous_profiling_apiserver_config
}

profile_documents_exist() {
	local profile_id=$1
	curl -sf "${CURL_TIMEOUT[@]}" \
		-H 'Content-Type: application/json' \
		-X POST "${ELASTICSEARCH_ADDR}/${PROFILE_INDEX}/_count" \
		-d "{\"query\":{\"term\":{\"tracer_id.keyword\":{\"value\":\"${profile_id}\"}}}}" \
		| jq -e '.count > 0' > /dev/null
}

seed_staging_profile_document() {
	local profile_id=$1
	curl -sf "${CURL_TIMEOUT[@]}" \
		-H 'Content-Type: application/json' \
		-X PUT \
		"${ELASTICSEARCH_ADDR}/${PROFILE_INDEX}/_doc/control-partial-${profile_id}?refresh=wait_for" \
		-d "{\"tracer_id\":\"${profile_id}\",\"record_type\":\"integration_partial\"}" \
		> /dev/null
}

profile_documents_are_deleted() {
	local profile_id=$1
	curl -sf "${CURL_TIMEOUT[@]}" \
		-H 'Content-Type: application/json' \
		-X POST "${ELASTICSEARCH_ADDR}/${PROFILE_INDEX}/_count" \
		-d "{\"query\":{\"term\":{\"tracer_id.keyword\":{\"value\":\"${profile_id}\"}}}}" \
		| jq -e '.count == 0' > /dev/null
}

assert_node_tracing_not_implemented() {
	local response_file="${HUATUO_BAMAI_TEST_TMPDIR}/tracing-not-implemented.json"
	local status

	status=$(curl -sS "${CURL_TIMEOUT[@]}" -o "${response_file}" \
		-w '%{http_code}' -X POST \
		-H 'Authorization: Bearer integration-node-token' \
		-H 'Content-Type: application/json' \
		"${HUATUO_BAMAI_ADDR}/v1/operations" \
		-d '{"request_id":"tracing-not-implemented","duration_seconds":30,"scope":"host","kind":"tracing","spec":{"type":"tcp_retransmit"}}')
	assert_eq "${status}" "501" "Node Tracing without executor" \
		|| fatal "Node Tracing status ${status}, want 501"
	jq -e '.error.code == "service_not_implemented"' "${response_file}" \
		> /dev/null || fatal "Node Tracing did not return service_not_implemented"

	status=$(curl -sS "${CURL_TIMEOUT[@]}" -o "${response_file}" \
		-w '%{http_code}' \
		-H 'Authorization: Bearer integration-node-token' \
		"${HUATUO_BAMAI_ADDR}/v1/operations/tracing-not-implemented")
	assert_eq "${status}" "404" "unimplemented Tracing Operation lookup" \
		|| fatal "unimplemented Tracing created an Operation"
	jq -e '.error.code == "operation_not_found"' "${response_file}" \
		> /dev/null || fatal "missing Tracing Operation returned an invalid error"
}

assert_capacity_failure() {
	local create_file="${HUATUO_BAMAI_TEST_TMPDIR}/create-capacity-profile.json"
	local status_file="${HUATUO_BAMAI_TEST_TMPDIR}/capacity-profile-status.json"

	continuous_profile_create_cpu "${create_file}" "${PROFILE_DURATION}" \
		"create capacity-limited profile"
	CAPACITY_PROFILE_ID=$(jq -er '.data.request_id' "${create_file}") \
		|| fatal "capacity profile response has no Job request ID"
	wait_until 10 1 continuous_profile_status_is \
		"${CAPACITY_PROFILE_ID}" failed "${status_file}" \
		|| fatal "capacity-limited profile did not fail"
	jq -e '
		.data.terminal.outcome == "failed"
		and .data.terminal.reason == "execution_capacity_exceeded"
		and .data.result_url == null
	' "${status_file}" > /dev/null \
		|| fatal "capacity-limited profile returned an invalid failure"
}

stop_running_profile() {
	local stop_file="${HUATUO_BAMAI_TEST_TMPDIR}/stop-profile.json"
	local status_file="${HUATUO_BAMAI_TEST_TMPDIR}/stopped-profile-status.json"
	local raw_file="${HUATUO_BAMAI_TEST_TMPDIR}/stopped-profile-raw.json"
	local status

	status=$(curl -sS "${CURL_TIMEOUT[@]}" -o "${stop_file}" \
		-w '%{http_code}' -X POST \
		-H "Authorization: Bearer ${API_TOKEN}" \
		"${APISERVER_ADDR}/v1/profiling/${RUNNING_PROFILE_ID}/stop")
	assert_eq "${status}" "200" "stop active profile" \
		|| fatal "stop active profile status ${status}, want 200"
	jq -e '.data.status == "stopping" and .data.terminal == null' \
		"${stop_file}" > /dev/null \
		|| fatal "stop response did not expose the asynchronous stopping state"

	wait_until 20 1 continuous_profile_status_is \
		"${RUNNING_PROFILE_ID}" stopped "${status_file}" \
		|| fatal "stopped profile did not reach its terminal state"
	jq -e '.data.terminal.outcome == "stopped" and .data.result_url == null' \
		"${status_file}" > /dev/null \
		|| fatal "stopped profile exposed a result or failure"
	profile_documents_are_deleted "${RUNNING_PROFILE_ID}" \
		|| fatal "stopped profile retained staging documents"

	status=$(curl -sS "${CURL_TIMEOUT[@]}" -o "${raw_file}" \
		-w '%{http_code}' \
		-H "Authorization: Bearer ${API_TOKEN}" \
		"${APISERVER_ADDR}/v1/profiling/${RUNNING_PROFILE_ID}/raw")
	assert_eq "${status}" "409" "stopped profile result" \
		|| fatal "stopped profile result status ${status}, want 409"
	jq -e '.error.code == "result_unavailable"' "${raw_file}" \
		> /dev/null || fatal "stopped profile returned an invalid result error"
}

start_control_stack
assert_node_tracing_not_implemented
continuous_profiling_start_native_cpu_fixture TARGET_PID

continuous_profile_create_cpu \
	"${HUATUO_BAMAI_TEST_TMPDIR}/create-running-profile.json" \
	"${PROFILE_DURATION}" "create profile to stop"
RUNNING_PROFILE_ID=$(jq -er '.data.request_id' \
	"${HUATUO_BAMAI_TEST_TMPDIR}/create-running-profile.json") \
	|| fatal "running profile response has no Job request ID"
wait_until 10 1 continuous_profile_status_is \
	"${RUNNING_PROFILE_ID}" running \
	"${HUATUO_BAMAI_TEST_TMPDIR}/running-profile-status.json" \
	|| fatal "profile did not enter running state"
seed_staging_profile_document "${RUNNING_PROFILE_ID}" \
	|| fatal "failed to seed a partial profile document"
profile_documents_exist "${RUNNING_PROFILE_ID}" \
	|| fatal "partial profile document is not queryable"

assert_capacity_failure
stop_running_profile

assert_log_has_no_failure \
	"${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "huatuo-bamai"
assert_log_has_no_failure \
	"${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.log" "huatuo-apiserver"
