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

# Verify that all profiling APIs report an actionable error without storage.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"

readonly API_TOKEN="integration-admin"
readonly DISABLED_MESSAGE="profiling is disabled: configure profile storage to enable it"
readonly FAILURE_LOG_PATTERN='panic:|fatal|level=(error|panic|fatal)|"level":"(error|panic|fatal)"'

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

assert_profile_authentication_precedes_feature_state() {
	local response_file="${HUATUO_BAMAI_TEST_TMPDIR}/profile-unauthorized.json"
	local status

	status=$(curl -sS "${CURL_TIMEOUT[@]}" -o "${response_file}" -w '%{http_code}' \
		"${APISERVER_ADDR}/v1/profiles")
	assert_eq "${status}" "401" "disabled profiling authentication" \
		|| fatal "disabled profiling request bypassed authentication"
	jq -e '.error.code == "unauthorized" and .error.message == "missing bearer token"' \
		"${response_file}" > /dev/null \
		|| fatal "disabled profiling authentication response is invalid"
}

assert_profile_routes_are_disabled() {
	local cases=(
		"GET|/v1/profiles"
		"POST|/v1/profiles"
		"GET|/v1/profiles/capabilities"
		"DELETE|/v1/profiles/profile-2026"
		"PUT|/v1/profiles/arbitrary/nested/path"
	)
	local test_case method path response_file status
	local index=0

	for test_case in "${cases[@]}"; do
		IFS='|' read -r method path <<< "${test_case}"
		response_file="${HUATUO_BAMAI_TEST_TMPDIR}/profile-disabled-${index}.json"
		status=$(curl -sS "${CURL_TIMEOUT[@]}" -o "${response_file}" -w '%{http_code}' \
			-X "${method}" -H "Authorization: Bearer ${API_TOKEN}" \
			"${APISERVER_ADDR}${path}")
		log_info "${method} ${path} response: $(< "${response_file}")"
		assert_eq "${status}" "503" "disabled profiling ${method} ${path}" \
			|| fatal "disabled profiling route returned status ${status}"
		jq -e --arg message "${DISABLED_MESSAGE}" \
			'.error.code == "profiling_disabled" and .error.message == $message and (has("data") | not)' \
			"${response_file}" > /dev/null \
			|| fatal "disabled profiling response is invalid for ${method} ${path}"
		index=$((index + 1))
	done
}

integration_huatuo_apiserver_start write_apiserver_profile_disabled_config \
	--log-debug
assert_profile_authentication_precedes_feature_state
assert_profile_routes_are_disabled
! grep -qiE "${FAILURE_LOG_PATTERN}" "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.log" \
	|| fatal "huatuo-apiserver log contains an unexpected failure"
