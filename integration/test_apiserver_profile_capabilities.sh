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

# Verify the profiling capabilities API authentication and response contract.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"

readonly API_TOKEN="integration-admin"
readonly CAPABILITIES_AGGREGATION_INTERVAL_SECONDS=7
readonly CAPABILITIES_MAX_CONCURRENT_PROFILERS=3
readonly FAILURE_LOG_PATTERN='panic:|fatal|level=(error|panic|fatal)|"level":"(error|panic|fatal)"'

command -v curl > /dev/null || skip "curl command is not installed"
command -v jq > /dev/null || skip "jq command is not installed"
command -v ss > /dev/null || skip "ss command is not installed"
[[ -x "${HUATUO_APISERVER_BIN}" ]] \
	|| fatal "huatuo-apiserver binary missing: ${HUATUO_APISERVER_BIN}"

APISERVER_PORT=$(allocate_available_port) || fatal "failed to allocate an apiserver port"
readonly APISERVER_PORT
readonly APISERVER_ADDR="http://127.0.0.1:${APISERVER_PORT}"
readonly CAPABILITIES_URL="${APISERVER_ADDR}/v1/profiles/capabilities"

cleanup() {
	huatuo_apiserver_stop
}
trap cleanup EXIT

assert_profile_capabilities_authentication() {
	local cases=(
		"missing||missing bearer token"
		"invalid|Bearer invalid-token|invalid bearer token"
	)
	local test_case name authorization expected_message
	local body status
	local index=0
	local -a curl_args

	for test_case in "${cases[@]}"; do
		IFS='|' read -r name authorization expected_message <<< "${test_case}"
		body="${HUATUO_BAMAI_TEST_TMPDIR}/capabilities-auth-${index}.json"
		curl_args=(
			-sS
			"${CURL_TIMEOUT[@]}"
			-o "${body}"
			-w '%{http_code}'
		)
		if [[ -n "${authorization}" ]]; then
			curl_args+=(-H "Authorization: ${authorization}")
		fi

		status=$(curl "${curl_args[@]}" "${CAPABILITIES_URL}")
		log_info "${name} capabilities response: $(< "${body}")"
		assert_eq "${status}" "401" "${name} capabilities authentication" \
			|| fatal "${name} capabilities request returned status ${status}"
		jq -e --arg message "${expected_message}" \
			'.code != 0 and .message == $message' "${body}" > /dev/null \
			|| fatal "${name} capabilities response has an unexpected error"
		index=$((index + 1))
	done
}

assert_profile_capabilities() {
	local response_file="${HUATUO_BAMAI_TEST_TMPDIR}/profile-capabilities.json"
	local status curl_status=0
	status=$(curl -sS "${CURL_TIMEOUT[@]}" -o "${response_file}" -w '%{http_code}' \
		-H "Authorization: Bearer ${API_TOKEN}" "${CAPABILITIES_URL}") \
		|| curl_status=$?
	if [[ -r "${response_file}" ]]; then
		log_info "profile capabilities response: $(< "${response_file}")"
	else
		log_error "profile capabilities response file missing: ${response_file}"
	fi
	[[ ${curl_status} -eq 0 ]] \
		|| fatal "profile capabilities request failed with curl exit code ${curl_status}"
	assert_eq "${status}" "200" "authorized profile capabilities" \
		|| fatal "profile capabilities returned status ${status}"

	jq -e \
		--argjson aggregation_interval_seconds "${CAPABILITIES_AGGREGATION_INTERVAL_SECONDS}" \
		--argjson max_concurrent_profilers "${CAPABILITIES_MAX_CONCURRENT_PROFILERS}" \
		'
			.code == 0
			and .message == "success"
			and .data.types == ["cpu", "memory"]
			and .data.cpu_languages == ["c", "c++", "go", "java", "python"]
			and .data.memory_languages == ["c", "c++", "go", "java"]
			and .data.memory_modes.c == ["physical_alloc", "physical_usage", "virtual_alloc"]
			and .data.memory_modes["c++"] == ["physical_alloc", "physical_usage", "virtual_alloc"]
			and .data.memory_modes.go == ["physical_alloc", "physical_usage", "virtual_alloc"]
			and .data.memory_modes.java == ["object_alloc", "object_usage"]
			and .data.aggregation_interval_seconds == $aggregation_interval_seconds
			and .data.max_concurrent_profilers == $max_concurrent_profilers
		' "${response_file}" > /dev/null \
		|| fatal "profile capabilities response does not match the API contract"
}

integration_huatuo_apiserver_start write_apiserver_profile_capabilities_config \
	--disable-cgroup \
	--log-debug
assert_profile_capabilities_authentication
assert_profile_capabilities
! grep -qiE "${FAILURE_LOG_PATTERN}" "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.log" \
	|| fatal "huatuo-apiserver log contains an unexpected failure"
