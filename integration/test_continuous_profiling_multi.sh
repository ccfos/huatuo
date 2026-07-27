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

# Verify that apiserver can run multiple continuous CPU profiles concurrently
# against one host and keep each task's lifecycle and stored windows isolated.

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
readonly PROFILE_COUNT=2

continuous_profiling_requirements

APISERVER_PORT=$(allocate_available_port) || fatal "failed to allocate an apiserver port"
readonly APISERVER_PORT
readonly APISERVER_ADDR="http://127.0.0.1:${APISERVER_PORT}"

TARGET_PID=""
PROFILE_IDS=()

cleanup() {
	local status=$?
	continuous_profiling_cleanup "${status}" "${TARGET_PID}"
}
trap cleanup EXIT

create_native_cpu_profiles() {
	local index failed=0
	local pids=()

	for ((index = 0; index < PROFILE_COUNT; index++)); do
		continuous_profile_create_cpu \
			"${HUATUO_BAMAI_TEST_TMPDIR}/create-profile-${index}.json" \
			"${PROFILE_DURATION}" \
			"create native CPU profile ${index}" &
		pids+=("$!")
	done
	for index in "${!pids[@]}"; do
		if ! wait "${pids[index]}"; then
			failed=1
		fi
	done
	[[ "${failed}" -eq 0 ]] || fatal "one or more profile creation requests failed"

	local profile_id
	for ((index = 0; index < PROFILE_COUNT; index++)); do
		profile_id=$(jq -er '.data.id' \
			"${HUATUO_BAMAI_TEST_TMPDIR}/create-profile-${index}.json") \
			|| fatal "profile creation response ${index} has no task ID"
		PROFILE_IDS+=("${profile_id}")
	done

	local unique_count
	unique_count=$(printf '%s\n' "${PROFILE_IDS[@]}" | jq -R . | jq -s 'unique | length')
	assert_eq "${unique_count}" "${PROFILE_COUNT}" "unique profile task IDs" \
		|| fatal "concurrent profile creation returned duplicate task IDs"
}

all_profiles_have_status() {
	local expected_status=$1
	local profile_id
	for profile_id in "${PROFILE_IDS[@]}"; do
		continuous_profile_status_is \
			"${profile_id}" "${expected_status}" \
			"${HUATUO_BAMAI_TEST_TMPDIR}/profile-status-${profile_id}.json" \
			|| return 1
	done
}

assert_completed_profiles() {
	wait_until 60 2 all_profiles_have_status completed \
		|| fatal "concurrent profiles did not complete"

	local profile_id
	for profile_id in "${PROFILE_IDS[@]}"; do
		jq -e --argjson duration "${PROFILE_DURATION}" \
			'.data.duration_seconds == $duration
				and .data.created_at != null
				and .data.finished_at != null
				and .data.result_url != null
				and .data.status_reason == null' \
			"${HUATUO_BAMAI_TEST_TMPDIR}/profile-status-${profile_id}.json" > /dev/null \
			|| fatal "completed profile ${profile_id} metadata is incomplete"
		wait_until 90 2 continuous_profile_windows_are_stored \
			"${profile_id}" \
			"${HUATUO_BAMAI_TEST_TMPDIR}/profiles-raw-${profile_id}.json" \
			|| fatal "profiling windows for ${profile_id} were not stored: ${CONTINUOUS_PROFILE_DIAGNOSTIC}"
	done
}

assert_profiles_are_listed() {
	local response_file="${HUATUO_BAMAI_TEST_TMPDIR}/profiles-list.json"
	curl -sf "${CURL_TIMEOUT[@]}" -H "Authorization: Bearer ${API_TOKEN}" \
		"${APISERVER_ADDR}/v1/profiles?type=cpu&hostname=127.0.0.1&status=completed&limit=100&offset=0&sort=-created_at" \
		> "${response_file}" || fatal "failed to list completed profiles"

	local profile_id
	for profile_id in "${PROFILE_IDS[@]}"; do
		jq -e --arg id "${profile_id}" \
			'any(.data.items[]; .id == $id)' "${response_file}" > /dev/null \
			|| fatal "profile list did not contain concurrent task ${profile_id}"
	done
}

delete_profiles() {
	local profile_id status
	for profile_id in "${PROFILE_IDS[@]}"; do
		status=$(curl -sS "${CURL_TIMEOUT[@]}" -o /dev/null -w '%{http_code}' -X DELETE \
			-H "Authorization: Bearer ${API_TOKEN}" \
			"${APISERVER_ADDR}/v1/profiles/${profile_id}")
		assert_eq "${status}" "204" "delete completed profile" \
			|| fatal "completed profile ${profile_id} deletion failed"
	done
}

continuous_profiling_start_stack

continuous_profiling_start_native_cpu_fixture TARGET_PID
create_native_cpu_profiles
wait_until 10 1 all_profiles_have_status running \
	|| fatal "profiles were not running concurrently on the same host"
assert_completed_profiles
assert_profiles_are_listed
delete_profiles

assert_log_has_no_failure \
	"${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "huatuo-bamai"
assert_log_has_no_failure \
	"${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.log" "huatuo-apiserver"
