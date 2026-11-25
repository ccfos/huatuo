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

require_commands curl jq ss

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

integration_huatuo_apiserver_start write_config_apiserver_base \
	--log-debug
assert_profile_routes_require_authentication
assert_profile_api_without_storage \
	"/v1/profiling" \
	'.data.limit == 100 and .data.offset == 0 and .data.has_more == false and (.data.items | length) == 0' \
	"${HUATUO_BAMAI_TEST_TMPDIR}/profile-list.json"
assert_profile_api_without_storage \
	"/v1/profiling/capabilities" '
	[.data.items[] | {type, language, modes:(.modes | sort),
		supported_scopes:(.supported_scopes | sort), supports_binary_match}]
	| sort_by(.type, .language) == ([
		(["c", "c++", "go"][] | {type:"cpu", language:.,
			modes:["offcpu", "oncpu"], supported_scopes:["container", "host"], supports_binary_match:false}),
		(["java", "python"][] | {type:"cpu", language:.,
			modes:["oncpu"], supported_scopes:["container"], supports_binary_match:true}),
		(["c", "c++", "go"][] | {type:"memory", language:.,
			modes:["physical_alloc", "physical_usage", "virtual_alloc"],
			supported_scopes:["container"], supports_binary_match:false}),
		{type:"memory", language:"java", modes:["object_alloc", "object_usage"],
			supported_scopes:["container"], supports_binary_match:false},
		{type:"irq", language:null, modes:["softirq"],
			supported_scopes:["host"], supports_binary_match:false}
	] | sort_by(.type, .language))' \
	"${HUATUO_BAMAI_TEST_TMPDIR}/profile-capabilities.json"
jq -e 'all(.data.items[] | select(.type == "irq"); has("language") | not)' \
	"${HUATUO_BAMAI_TEST_TMPDIR}/profile-capabilities.json" > /dev/null \
	|| fatal "IRQ capability exposed a language"
# The shared request schema keeps language conditional on the profiling type.
for profile_disabled_extra in '{"language":"c"}' '{"mode":"oncpu"}' \
	'{"binary_match_path":"/usr/bin/worker"}' \
	'{"scope":"container","container_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}' \
	'{"type":"cpu","mode":"oncpu"}' '{"type":"memory","mode":"physical_usage"}' \
	'{"target_cpu":0}' '{"irq_options":{}}'; do
	profile_disabled_body=$(jq -nc --argjson extra "${profile_disabled_extra}" \
		'{hostname:"127.0.0.1",duration_seconds:10,scope:"host",type:"irq",mode:"softirq"}+$extra')
	profile_disabled_curl_status=0
	profile_disabled_status=$(curl -sS "${CURL_TIMEOUT[@]}" \
		-H "Authorization: Bearer ${API_TOKEN}" -H 'Content-Type: application/json' \
		-o "${HUATUO_BAMAI_TEST_TMPDIR}/invalid-profile.json" -w '%{http_code}' \
		-d "${profile_disabled_body}" "${APISERVER_ADDR}/v1/profiling") || profile_disabled_curl_status=$?
	report_response "POST /v1/profiling ${profile_disabled_body}" \
		"${HUATUO_BAMAI_TEST_TMPDIR}/invalid-profile.json" "${profile_disabled_curl_status}"
	[[ ${profile_disabled_status} == 400 ]] || fatal "invalid profiling combination returned ${profile_disabled_status}"
	jq -e '.error.code == "invalid_request"' "${HUATUO_BAMAI_TEST_TMPDIR}/invalid-profile.json" > /dev/null \
		|| fatal "invalid profiling combination did not return invalid_request"
done
assert_readiness_endpoint "${APISERVER_ADDR}/readyz" "${HUATUO_BAMAI_TEST_TMPDIR}/readyz" \
	|| fatal "apiserver readiness contract failed"
assert_removed_profile_route_is_not_registered
assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.log" huatuo-apiserver
