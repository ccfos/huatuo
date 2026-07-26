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

readonly ES_PASSWORD="huatuo-integration"
readonly API_TOKEN="integration-admin"
readonly OTHER_API_TOKEN="integration-other"
readonly PROFILE_DURATION=12
readonly PROFILE_INTERVAL=5
readonly FIXTURE_SRC="${ROOT_DIR}/integration/testdata/test_profiler_callchain.user.c"

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
readonly PARANOID=$(cat /proc/sys/kernel/perf_event_paranoid)
[[ "${PARANOID}" -le 2 ]] \
	|| skip "kernel.perf_event_paranoid=${PARANOID} blocks sampling"

APISERVER_PORT=$(allocate_available_port) || fatal "failed to allocate an apiserver port"
readonly APISERVER_PORT
readonly APISERVER_ADDR="http://127.0.0.1:${APISERVER_PORT}"

TARGET_PID=""
LAST_PROFILE_DIAGNOSTIC="no raw profile request made"

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
	compile_user_fixture "${FIXTURE_SRC}" "${fixture_bin}"
	"${fixture_bin}" > "${HUATUO_BAMAI_TEST_TMPDIR}/fixture.out" \
		2> "${HUATUO_BAMAI_TEST_TMPDIR}/fixture.err" &
	TARGET_PID=$!
	kill -0 "${TARGET_PID}" 2> /dev/null || fatal "CPU fixture exited immediately"
}

create_native_cpu_profile() {
	local response_file="${HUATUO_BAMAI_TEST_TMPDIR}/create-profile.json"
	local status curl_status=0
	status=$(curl -sS "${CURL_TIMEOUT[@]}" -o "${response_file}" -w '%{http_code}' -X POST \
		-H "Authorization: Bearer ${API_TOKEN}" \
		-H 'Content-Type: application/json' \
		"${APISERVER_ADDR}/v1/profiles" \
		-d "{\"type\":\"cpu\",\"language\":\"c\",\"duration_seconds\":${PROFILE_DURATION},\"hostname\":\"127.0.0.1\"}") \
		|| curl_status=$?
	if [[ -r "${response_file}" ]]; then
		log_info "create native CPU profile response: $(< "${response_file}")"
	else
		log_error "create native CPU profile response file missing: ${response_file}"
	fi
	[[ ${curl_status} -eq 0 ]] \
		|| fatal "create native CPU profile request failed with curl exit code ${curl_status}"
	assert_eq "${status}" "201" "create native CPU profile" \
		|| fatal "profile creation failed: $(< "${response_file}")"
	PROFILE_ID=$(jq -er '.data.id' "${response_file}") \
		|| fatal "profile creation response has no task ID"
	export PROFILE_ID
}

profile_status_is() {
	local expected_status=$1
	curl -sf "${CURL_TIMEOUT[@]}" -H "Authorization: Bearer ${API_TOKEN}" \
		"${APISERVER_ADDR}/v1/profiles/${PROFILE_ID}" \
		> "${HUATUO_BAMAI_TEST_TMPDIR}/profile-status.json" \
		|| return 1
	jq -e --arg expected_status "${expected_status}" \
		'.data.status == $expected_status' \
		"${HUATUO_BAMAI_TEST_TMPDIR}/profile-status.json" > /dev/null
}

profiles_are_stored() {
	local response_file="${HUATUO_BAMAI_TEST_TMPDIR}/profiles-raw.json"
	local status
	status=$(
		curl -sS "${CURL_TIMEOUT[@]}" -o "${response_file}" -w '%{http_code}' \
			-H "Authorization: Bearer ${API_TOKEN}" \
			"${APISERVER_ADDR}/v1/profiles/${PROFILE_ID}/raw"
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

assert_profile_lifecycle() {
	wait_until 10 1 profile_status_is running || fatal "profile did not enter running state"

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

	wait_until 60 2 profile_status_is completed || fatal "profile did not complete"
	jq -e --argjson duration "${PROFILE_DURATION}" \
		'.data.duration_seconds == $duration
			and .data.created_at != null
			and .data.finished_at != null
			and .data.result_url != null
			and .data.status_reason == null
			and (.data | has("agent_task_id") | not)
			and (.data | has("tracer_args") | not)' \
		"${HUATUO_BAMAI_TEST_TMPDIR}/profile-status.json" > /dev/null \
		|| fatal "completed profile metadata is incomplete"

	wait_until 90 2 profiles_are_stored \
		|| fatal "profiling windows were not stored: ${LAST_PROFILE_DIAGNOSTIC}"
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

elasticsearch_start
integration_huatuo_bamai_start \
	write_continuous_profiling_bamai_config \
	--region integration \
	--disable-kubelet \
	--log-debug
integration_huatuo_apiserver_start write_continuous_profiling_apiserver_config

start_native_cpu_fixture
create_native_cpu_profile
assert_profile_lifecycle
readonly FAILURE_LOG_PATTERN='panic:|fatal|level=(error|panic|fatal)|"level":"(error|panic|fatal)"'
! grep -qiE "${FAILURE_LOG_PATTERN}" "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
	|| fatal "huatuo-bamai log contains an unexpected failure"
! grep -qiE "${FAILURE_LOG_PATTERN}" "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.log" \
	|| fatal "huatuo-apiserver log contains an unexpected failure"
