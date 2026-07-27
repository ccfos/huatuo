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

readonly ES_PASSWORD="huatuo-integration"
readonly API_TOKEN="integration-admin"
readonly OTHER_API_TOKEN="integration-other"
readonly PROFILE_DURATION=12
readonly PROFILE_INTERVAL=5
readonly PROFILE_COUNT=2
readonly PROFILE_FIXTURE_SRC="${ROOT_DIR}/integration/testdata/test_profiler_callchain.user.c"

command -v docker > /dev/null || skip "docker command is not installed"
docker info > /dev/null 2>&1 || skip "docker daemon is unavailable"
command -v jq > /dev/null || skip "jq command is not installed"
command -v ss > /dev/null || skip "ss command is not installed"
command -v timeout > /dev/null || fatal "timeout command is not installed"
[[ -x "${HUATUO_APISERVER_BIN}" ]] \
	|| fatal "huatuo-apiserver binary missing"
[[ -x "${ROOT_DIR}/_output/bin/huatuo-bamai" ]] || fatal "huatuo-bamai binary missing"
[[ -x "${ROOT_DIR}/_output/bin/profiler" ]] || fatal "profiler binary missing"
[[ -r "${ROOT_DIR}/_output/bpf/native_cpu_profiler.o" ]] \
	|| fatal "native CPU profiler BPF object missing"
[[ -r /proc/sys/kernel/perf_event_paranoid ]] || skip "perf_event is unavailable"
readonly PROFILE_PARANOID=$(cat /proc/sys/kernel/perf_event_paranoid)
[[ "${PROFILE_PARANOID}" -le 2 ]] \
	|| skip "kernel.perf_event_paranoid=${PROFILE_PARANOID} blocks sampling"

APISERVER_PORT=$(allocate_available_port) || fatal "failed to allocate an apiserver port"
readonly APISERVER_PORT
readonly APISERVER_ADDR="http://127.0.0.1:${APISERVER_PORT}"

TARGET_PID=""
LAST_PROFILE_DIAGNOSTIC="no raw profile request made"
PROFILE_IDS=()

cleanup() {
	local status=$?
	[[ -n "${TARGET_PID}" ]] && stop_by_pid "${TARGET_PID}" 5 || true
	huatuo_apiserver_stop
	huatuo_bamai_stop "${HUATUO_BAMAI_TEST_TMPDIR}" || true
	if [[ -n "${ELASTICSEARCH_CONTAINER_ID}" ]]; then
		if [[ ${status} -ne 0 ]]; then
			elasticsearch_dump_logs || true
		fi
		elasticsearch_stop || true
	fi
}
trap cleanup EXIT

start_native_cpu_fixture() {
	local fixture_bin="${HUATUO_BAMAI_TEST_TMPDIR}/callchain"
	compile_user_fixture "${PROFILE_FIXTURE_SRC}" "${fixture_bin}"
	"${fixture_bin}" > "${HUATUO_BAMAI_TEST_TMPDIR}/fixture.out" \
		2> "${HUATUO_BAMAI_TEST_TMPDIR}/fixture.err" &
	TARGET_PID=$!
	kill -0 "${TARGET_PID}" 2> /dev/null || fatal "CPU fixture exited immediately"
}

create_native_cpu_profile() {
	local index=$1
	local response_file="${HUATUO_BAMAI_TEST_TMPDIR}/create-profile-${index}.json"
	local status curl_status=0
	status=$(curl -sS "${CURL_TIMEOUT[@]}" -o "${response_file}" -w '%{http_code}' -X POST \
		-H "Authorization: Bearer ${API_TOKEN}" \
		-H 'Content-Type: application/json' \
		"${APISERVER_ADDR}/v1/profiles" \
		-d "{\"type\":\"cpu\",\"language\":\"c\",\"duration_seconds\":${PROFILE_DURATION},\"hostname\":\"127.0.0.1\"}") \
		|| curl_status=$?
	if [[ -r "${response_file}" ]]; then
		log_info "create native CPU profile ${index} response: $(< "${response_file}")"
	else
		log_error "create native CPU profile response file missing: ${response_file}"
	fi
	[[ ${curl_status} -eq 0 ]] \
		|| fatal "create native CPU profile request failed with curl exit code ${curl_status}"
	assert_eq "${status}" "201" "create native CPU profile ${index}" \
		|| fatal "profile creation failed: $(< "${response_file}")"
}

create_native_cpu_profiles() {
	local index failed=0
	local pids=()

	for ((index = 0; index < PROFILE_COUNT; index++)); do
		create_native_cpu_profile "${index}" &
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

profile_status_is() {
	local profile_id=$1
	local expected_status=$2
	curl -sf "${CURL_TIMEOUT[@]}" -H "Authorization: Bearer ${API_TOKEN}" \
		"${APISERVER_ADDR}/v1/profiles/${profile_id}" \
		> "${HUATUO_BAMAI_TEST_TMPDIR}/profile-status-${profile_id}.json" \
		|| return 1
	jq -e --arg expected_status "${expected_status}" \
		'.data.status == $expected_status' \
		"${HUATUO_BAMAI_TEST_TMPDIR}/profile-status-${profile_id}.json" > /dev/null
}

all_profiles_have_status() {
	local expected_status=$1
	local profile_id
	for profile_id in "${PROFILE_IDS[@]}"; do
		profile_status_is "${profile_id}" "${expected_status}" || return 1
	done
}

profiles_are_stored() {
	local profile_id=$1
	local response_file="${HUATUO_BAMAI_TEST_TMPDIR}/profiles-raw-${profile_id}.json"
	local status
	status=$(
		curl -sS "${CURL_TIMEOUT[@]}" -o "${response_file}" -w '%{http_code}' \
			-H "Authorization: Bearer ${API_TOKEN}" \
			"${APISERVER_ADDR}/v1/profiles/${profile_id}/raw"
	) || {
		LAST_PROFILE_DIAGNOSTIC="raw profile request failed before receiving an HTTP response"
		return 1
	}
	local count
	count=$(jq -er '
		.data.items
		| map(
			has("uploaded_at")
			and has("captured_at")
			and has("profile_type")
			and has("profile")
			and (has("tracer_id") | not)
			and (has("tracer_data") | not)
		)
		| if all then length else error("invalid raw profile contract") end
	' "${response_file}" 2> /dev/null) || {
		LAST_PROFILE_DIAGNOSTIC="raw profile response status=${status}, invalid body: $(< "${response_file}")"
		return 1
	}
	LAST_PROFILE_DIAGNOSTIC="raw profile response status=${status}, windows=${count}"
	[[ "${status}" == "200" && "${count}" -ge 2 ]]
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
		wait_until 90 2 profiles_are_stored "${profile_id}" \
			|| fatal "profiling windows for ${profile_id} were not stored: ${LAST_PROFILE_DIAGNOSTIC}"
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

elasticsearch_start
integration_huatuo_bamai_start \
	write_continuous_profiling_bamai_config \
	--region integration \
	--disable-kubelet \
	--log-debug
integration_huatuo_apiserver_start write_continuous_profiling_apiserver_config

start_native_cpu_fixture
create_native_cpu_profiles
wait_until 10 1 all_profiles_have_status running \
	|| fatal "profiles were not running concurrently on the same host"
assert_completed_profiles
assert_profiles_are_listed
delete_profiles

readonly PROFILE_FAILURE_LOG_PATTERN='panic:|fatal|level=(error|panic|fatal)|"level":"(error|panic|fatal)"'
! grep -qiE "${PROFILE_FAILURE_LOG_PATTERN}" "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
	|| fatal "huatuo-bamai log contains an unexpected failure"
! grep -qiE "${PROFILE_FAILURE_LOG_PATTERN}" "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.log" \
	|| fatal "huatuo-apiserver log contains an unexpected failure"
