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

# Verify authentication and static Profiling APIs without result storage.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"

readonly API_TOKEN="integration-admin"

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
		"${APISERVER_ADDR}/v1/profiling") || curl_status=$?
	report_response "GET /v1/profiling without authentication" "${response_file}" "${curl_status}"

	[[ "${status}" == "401" ]] \
		|| fatal "GET /v1/profiling without authentication: status ${status}, want 401"
	jq -e '.error.code == "unauthenticated"' \
		"${response_file}" > /dev/null \
		|| fatal "GET /v1/profiling without authentication did not return unauthenticated"
}

assert_profile_api_without_storage() {
	local path=$1 jq_filter=$2 response_file=$3
	local curl_status=0
	local status

	status=$(curl -sS "${CURL_TIMEOUT[@]}" -o "${response_file}" -w '%{http_code}' \
		-H "Authorization: Bearer ${API_TOKEN}" \
		"${APISERVER_ADDR}${path}") || curl_status=$?
	report_response "GET ${path}" "${response_file}" "${curl_status}"

	[[ "${status}" == "200" ]] \
		|| fatal "GET ${path}: status ${status}, want 200"
	jq -e "${jq_filter}" "${response_file}" > /dev/null \
		|| fatal "GET ${path} returned an invalid response"
}

assert_removed_profile_route_is_not_registered() {
	local response_file="${HUATUO_BAMAI_TEST_TMPDIR}/removed-profile-route.json"
	local status

	status=$(curl -sS "${CURL_TIMEOUT[@]}" -o "${response_file}" -w '%{http_code}' \
		-H "Authorization: Bearer ${API_TOKEN}" \
		"${APISERVER_ADDR}/v1/profiles")
	[[ "${status}" == "404" ]] \
		|| fatal "GET /v1/profiles: status ${status}, want 404"
	jq -e '.error.code == "route_not_found"' "${response_file}" > /dev/null \
		|| fatal "GET /v1/profiles did not return route_not_found"
}

integration_huatuo_apiserver_start write_apiserver_without_profile_storage_config \
	--log-debug
assert_profile_routes_require_authentication
assert_profile_api_without_storage \
	"/v1/profiling" \
	'.data.limit == 100 and .data.offset == 0 and .data.has_more == false and (.data.items | length) == 0' \
	"${HUATUO_BAMAI_TEST_TMPDIR}/profile-list.json"
assert_profile_api_without_storage \
	"/v1/profiling/capabilities" '(.data.items | length) > 0' \
	"${HUATUO_BAMAI_TEST_TMPDIR}/profile-capabilities.json"
assert_removed_profile_route_is_not_registered
assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.log" huatuo-apiserver
