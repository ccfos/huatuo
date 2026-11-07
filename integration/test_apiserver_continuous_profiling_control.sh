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
readonly CONTROL_PENDING_TIMEOUT=10
readonly CONTROL_STATUS_POLL_INTERVAL=1
readonly CONTROL_COMPLETION_GRACE=60
readonly CONTROL_NATURAL_DURATION=2
# Cover pending, execution/grace, two controller polls, and storage/HTTP headroom.
readonly CONTROL_COMPLETION_TIMEOUT=$((CONTROL_PENDING_TIMEOUT + CONTROL_NATURAL_DURATION + CONTROL_COMPLETION_GRACE + 2 * CONTROL_STATUS_POLL_INTERVAL + 5))

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

write_config_bamai_profiling_control() {
	write_config_bamai_continuous_profiling
	cat << 'EOF'

[Operations]
    MaxConcurrent = 1
EOF
}

write_config_apiserver_profiling_control() {
	write_config_apiserver_continuous_profiling
	cat << EOF

[Jobs.Controller]
    PendingTimeoutSeconds = ${CONTROL_PENDING_TIMEOUT}
    StatusPollIntervalSeconds = ${CONTROL_STATUS_POLL_INTERVAL}
    CompletionGracePeriodSeconds = ${CONTROL_COMPLETION_GRACE}
EOF
}

start_control_stack() {
	storage_start
	integration_huatuo_bamai_start \
		write_config_bamai_profiling_control \
		--region integration \
		--disable-kubelet \
		--log-debug
	integration_huatuo_apiserver_start \
		write_config_apiserver_profiling_control
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

control_profile_completed() {
	local profile_id=$1 response_file=$2
	continuous_profile_status_is "${profile_id}" completed "${response_file}" && return 0
	if jq -e '.data.status == "terminal" and .data.terminal.outcome != "completed"' \
		"${response_file}" > /dev/null 2>&1; then
		# The public Job redacts execution errors; include the matching Node diagnostics.
		grep -F "${profile_id}" "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
			| tail -n 20 >&2 || true
		fatal "profile finished without completing: ${continuous_profile_status_diagnostic}; last response: $(< "${response_file}")"
	fi
	return 1
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

assert_profile_publication() {
	local profile_id=$1 expected_status=$2 status curl_status=0
	local response="${HUATUO_BAMAI_TEST_TMPDIR}/publication-${profile_id}.json"
	local index
	index=$(storage_test_index huatuo_continuous_profiling_test)
	# A terminal task has finished its commit path; realtime GET avoids refresh races.
	status=$(curl -sS "${CURL_TIMEOUT[@]}" -o "${response}" -w '%{http_code}' \
		"${STORAGE_ADDR}/${index}/_doc/profiling-publication:${profile_id}") || curl_status=$?
	log_info "publication ${profile_id}: HTTP ${status}, $(cat "${response}")"
	[[ ${curl_status} == 0 ]] || fatal "publication request failed: curl exit ${curl_status}"
	assert_eq "${status}" "${expected_status}" "profile publication" \
		|| fatal "publication ${profile_id}: HTTP ${status}, want ${expected_status}"
	if [[ ${expected_status} == 404 ]]; then
		jq -e --arg id "profiling-publication:${profile_id}" \
			'.found == false and ._id == $id' "${response}" > /dev/null \
			|| fatal "expected absent completion marker, got $(cat "${response}")"
	else
		jq -e --arg id "${profile_id}" \
			'.found == true and ._source.request_id == $id and (._source.published_at | length > 0)' \
			"${response}" > /dev/null || fatal "invalid completion marker: $(cat "${response}")"
	fi
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

# Duration expiry must still publish: API abort must not turn every stop into discard.
continuous_profile_create_cpu \
	"${HUATUO_BAMAI_TEST_TMPDIR}/create-completing-profile.json" "${CONTROL_NATURAL_DURATION}" \
	"create profile to complete naturally"
control_completed_id=$(jq -er '.data.request_id' \
	"${HUATUO_BAMAI_TEST_TMPDIR}/create-completing-profile.json") \
	|| fatal "completed profile response has no Job request ID"
# Use the same controller limits as the test configuration; return on completion.
wait_until "${CONTROL_COMPLETION_TIMEOUT}" 0.1 control_profile_completed "${control_completed_id}" \
	"${HUATUO_BAMAI_TEST_TMPDIR}/completed-profile-status.json" \
	|| fatal "profile did not complete: ${continuous_profile_status_diagnostic}; last response: $(< "${HUATUO_BAMAI_TEST_TMPDIR}/completed-profile-status.json")"
assert_profile_publication "${control_completed_id}" 200

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

assert_capacity_failure
stop_running_profile
# Status/raw APIs reject stopped jobs independently of publication. Inspect storage
# so an accidental CLI commit cannot be hidden by the stopped status.
assert_profile_publication "${RUNNING_PROFILE_ID}" 404

assert_log_has_no_failure \
	"${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "huatuo-bamai"
assert_log_has_no_failure \
	"${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.log" "huatuo-apiserver"
