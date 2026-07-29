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

# Verify the complete native CPU continuous-profiling path: apiserver creates
# a bamai task, profiler uploads multiple windows through toolstream, and the
# apiserver reads the stored profiles back from Elasticsearch.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_storage.sh"
source "${ROOT_DIR}/integration/config.sh"
source "${ROOT_DIR}/integration/lib_continuous_profiling.sh"

readonly ES_PASSWORD="huatuo-integration"
readonly API_TOKEN="integration-admin"
readonly OTHER_API_TOKEN="integration-other"
readonly PROFILE_DURATION=12
readonly PROFILE_INTERVAL=5

continuous_profiling_requirements

APISERVER_PORT=$(allocate_available_port) || fatal "failed to allocate an apiserver port"
readonly APISERVER_PORT
readonly APISERVER_ADDR="http://127.0.0.1:${APISERVER_PORT}"
readonly PROFILE_CREATE_RESPONSE="${HUATUO_BAMAI_TEST_TMPDIR}/create-profile.json"
readonly PROFILE_STATUS_RESPONSE="${HUATUO_BAMAI_TEST_TMPDIR}/profile-status.json"
readonly PROFILE_RAW_RESPONSE="${HUATUO_BAMAI_TEST_TMPDIR}/profiles-raw.json"

TARGET_PID=""
PROFILE_ID=""

cleanup() {
	local status=$?
	continuous_profiling_cleanup "${status}" "${TARGET_PID}"
}
trap cleanup EXIT

assert_profile_lifecycle() {
	wait_until 10 1 continuous_profile_status_is \
		"${PROFILE_ID}" running "${PROFILE_STATUS_RESPONSE}" \
		|| fatal "profile did not enter running state"

	local status
	status=$(curl -sS "${CURL_TIMEOUT[@]}" \
		-o "${HUATUO_BAMAI_TEST_TMPDIR}/forbidden.json" -w '%{http_code}' \
		-H "Authorization: Bearer ${OTHER_API_TOKEN}" \
		"${APISERVER_ADDR}/v1/profiles/${PROFILE_ID}")
	assert_eq "${status}" "403" "non-owner profile access" \
		|| fatal "profile was visible to a non-owner"

	status=$(curl -sS "${CURL_TIMEOUT[@]}" \
		-o "${HUATUO_BAMAI_TEST_TMPDIR}/delete-running.json" -w '%{http_code}' \
		-X DELETE -H "Authorization: Bearer ${API_TOKEN}" \
		"${APISERVER_ADDR}/v1/profiles/${PROFILE_ID}")
	assert_eq "${status}" "409" "delete running profile" \
		|| fatal "running profile deletion did not return conflict"

	wait_until 60 2 continuous_profile_status_is \
		"${PROFILE_ID}" completed "${PROFILE_STATUS_RESPONSE}" \
		|| fatal "profile did not complete"
	jq -e --argjson duration "${PROFILE_DURATION}" \
		'.data.duration_seconds == $duration
			and .data.created_at != null
			and .data.finished_at != null
			and .data.result_url != null
			and .data.status_reason == null
			and (.data | has("agent_task_id") | not)
			and (.data | has("tracer_args") | not)' \
		"${PROFILE_STATUS_RESPONSE}" > /dev/null \
		|| fatal "completed profile metadata is incomplete"

	wait_until 90 2 continuous_profile_windows_are_stored \
		"${PROFILE_ID}" "${PROFILE_RAW_RESPONSE}" \
		|| fatal "profiling windows were not stored: ${CONTINUOUS_PROFILE_DIAGNOSTIC}"
	# Stack frame ordering is covered by lower-level profiler tests; this test
	# verifies only the API, task lifecycle, transport, and storage contract.

	curl -sf "${CURL_TIMEOUT[@]}" -H "Authorization: Bearer ${API_TOKEN}" \
		"${APISERVER_ADDR}/v1/profiles?type=cpu&hostname=127.0.0.1&status=completed&limit=1&offset=0&sort=-created_at" \
		| jq -e --arg id "${PROFILE_ID}" '.data.total >= 1 and .data.items[0].id == $id' \
			> /dev/null || fatal "profile list filters did not return the completed task"

	status=$(curl -sS "${CURL_TIMEOUT[@]}" -o /dev/null -w '%{http_code}' -X DELETE \
		-H "Authorization: Bearer ${API_TOKEN}" "${APISERVER_ADDR}/v1/profiles/${PROFILE_ID}")
	assert_eq "${status}" "204" "delete completed profile" \
		|| fatal "completed profile deletion failed"
}

continuous_profiling_start_stack

continuous_profiling_start_native_cpu_fixture TARGET_PID
continuous_profile_create_cpu "${PROFILE_CREATE_RESPONSE}" "${PROFILE_DURATION}"
PROFILE_ID=$(jq -er '.data.id' "${PROFILE_CREATE_RESPONSE}") \
	|| fatal "profile creation response has no task ID"
assert_profile_lifecycle
assert_log_has_no_failure \
	"${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "huatuo-bamai"
assert_log_has_no_failure \
	"${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.log" "huatuo-apiserver"
