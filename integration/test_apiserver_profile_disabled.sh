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

# Verify authentication precedence and disabled profiling APIs without storage.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"

readonly API_TOKEN="integration-admin"
readonly DISABLED_MESSAGE="profiling is disabled: configure profile storage to enable it"

command -v curl > /dev/null || skip "curl command is not installed"
command -v jq > /dev/null || skip "jq command is not installed"
command -v ss > /dev/null || skip "ss command is not installed"
[[ -x "${HUATUO_APISERVER_BIN}" ]] \
	|| fatal "huatuo-apiserver binary missing: ${HUATUO_APISERVER_BIN}"

APISERVER_PORT=$(allocate_available_port) || fatal "failed to allocate an apiserver port"
readonly APISERVER_PORT
readonly APISERVER_ADDR="http://127.0.0.1:${APISERVER_PORT}"

cleanup() {
	huatuo_apiserver_stop
}
trap cleanup EXIT

report_response() {
	local label=$1 response_file=$2 curl_status=$3
	[[ -r "${response_file}" ]] \
		|| fatal "${label}: curl exited ${curl_status}; response file missing: ${response_file}"

	log_info "${label} response: $(< "${response_file}")"
	[[ ${curl_status} -eq 0 ]] \
		|| fatal "${label}: curl exited ${curl_status}"
}

assert_profile_routes_require_authentication() {
	local response_file="${HUATUO_BAMAI_TEST_TMPDIR}/profile-authentication.json"
	local curl_status=0
	local status

	status=$(curl -sS "${CURL_TIMEOUT[@]}" -o "${response_file}" -w '%{http_code}' \
		"${APISERVER_ADDR}/v1/profiles") || curl_status=$?
	report_response "GET /v1/profiles without authentication" "${response_file}" "${curl_status}"

	[[ "${status}" == "401" ]] \
		|| fatal "GET /v1/profiles without authentication: status ${status}, want 401"
	jq -e '.error.code == "unauthorized"' \
		"${response_file}" > /dev/null \
		|| fatal "GET /v1/profiles without authentication did not return unauthorized"
}

assert_profile_disabled() {
	local method=$1 path=$2 response_file=$3
	local curl_status=0
	local status

	status=$(curl -sS "${CURL_TIMEOUT[@]}" -o "${response_file}" -w '%{http_code}' \
		-X "${method}" -H "Authorization: Bearer ${API_TOKEN}" \
		"${APISERVER_ADDR}${path}") || curl_status=$?
	report_response "${method} ${path}" "${response_file}" "${curl_status}"

	[[ "${status}" == "503" ]] \
		|| fatal "${method} ${path}: status ${status}, want 503"
	jq -e --arg message "${DISABLED_MESSAGE}" \
		'.error.code == "profiling_disabled" and .error.message == $message and (has("data") | not)' \
		"${response_file}" > /dev/null \
		|| fatal "${method} ${path} returned an invalid disabled response"
}

assert_profile_routes_are_disabled() {
	local cases=(
		"GET|/v1/profiles"
		"POST|/v1/profiles"
		"GET|/v1/profiles/capabilities"
		"DELETE|/v1/profiles/profile-2026"
		"PUT|/v1/profiles/arbitrary/nested/path"
	)
	local test_case method path response_file
	local index=0

	for test_case in "${cases[@]}"; do
		IFS='|' read -r method path <<< "${test_case}"
		response_file="${HUATUO_BAMAI_TEST_TMPDIR}/profile-disabled-${index}.json"
		assert_profile_disabled "${method}" "${path}" "${response_file}"
		index=$((index + 1))
	done
}

integration_huatuo_apiserver_start write_apiserver_profile_disabled_config \
	--log-debug
assert_profile_routes_require_authentication
assert_profile_routes_are_disabled
assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.log" huatuo-apiserver
