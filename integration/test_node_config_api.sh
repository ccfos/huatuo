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

# Verify the generated Node configuration API and persisted update.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

readonly CONFIG_API_TOKEN="integration-node-token"
readonly CURL_TIMEOUT=(--connect-timeout 2 --max-time 3)
config_response_file=""
config_error_file=""
config_curl_status=0
config_http_status=""

command -v curl > /dev/null || skip "curl command is not installed"
command -v jq > /dev/null || skip "jq command is not installed"
command -v ss > /dev/null || skip "ss command is not installed"
[[ -x "${HUATUO_BAMAI_BIN}" ]] \
	|| fatal "huatuo-bamai binary missing: ${HUATUO_BAMAI_BIN}"

CONFIG_API_PORT=$(allocate_available_port) \
	|| fatal "failed to allocate a huatuo-bamai API port"
readonly CONFIG_API_PORT
HUATUO_BAMAI_ADDR="http://127.0.0.1:${CONFIG_API_PORT}"
HUATUO_BAMAI_METRICS_API="${HUATUO_BAMAI_ADDR}/metrics"
export HUATUO_BAMAI_ADDR HUATUO_BAMAI_METRICS_API

cleanup() {
	huatuo_bamai_stop
}
trap cleanup EXIT

write_config_api_config() {
	cat > "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" << EOF
BlackList = ["metax_gpu", "ascend_npu", "softlockup", "ethtool", "netstat_hw", "iolatency", "memory_free", "memory_reclaim", "reschedipi", "softirq", "iotracing"]

[HTTPServer]
    ListenAddress = "127.0.0.1:${CONFIG_API_PORT}"

[HTTPServer.Auth]
    BearerToken = "${CONFIG_API_TOKEN}"
EOF
}

request_config_api() {
	local label=$1 path=$2 content_type=$3 request_body=$4 authenticated=$5
	local auth_args=()
	config_response_file="${HUATUO_BAMAI_TEST_TMPDIR}/${label}.body"
	config_error_file="${HUATUO_BAMAI_TEST_TMPDIR}/${label}.curl-error"
	config_curl_status=0
	config_http_status=""
	if [[ "${authenticated}" == "true" ]]; then
		auth_args=(-H "Authorization: Bearer ${CONFIG_API_TOKEN}")
	fi

	config_http_status=$(curl -sS "${CURL_TIMEOUT[@]}" \
		-o "${config_response_file}" -w '%{http_code}' -X PUT \
		-H "Content-Type: ${content_type}" "${auth_args[@]}" \
		"${HUATUO_BAMAI_ADDR}${path}" -d "${request_body}" \
		2> "${config_error_file}") || config_curl_status=$?
	report_http_response "${label}" "${config_response_file}" "${config_error_file}"
}

assert_config_error() {
	local label=$1 path=$2 content_type=$3 request_body=$4 expected_status=$5
	local expected_code=$6 authenticated=$7

	request_config_api "${label}" "${path}" "${content_type}" \
		"${request_body}" "${authenticated}"
	if [[ ${config_curl_status} -ne 0 ]]; then
		fatal "${label}: curl exited ${config_curl_status}"
	fi
	assert_eq "${config_http_status}" "${expected_status}" "${label} status" \
		|| fatal "${label} returned status ${config_http_status}, expected ${expected_status}"
	jq -e --arg expected_code "${expected_code}" \
		'.error.code == $expected_code' "${config_response_file}" > /dev/null \
		|| fatal "${label} did not return ${expected_code}: $(< "${config_response_file}")"
}

assert_openapi_contract() {
	local response_file="${HUATUO_BAMAI_TEST_TMPDIR}/node-openapi.json"
	local error_file="${HUATUO_BAMAI_TEST_TMPDIR}/node-openapi.curl-error"
	local curl_status=0 status

	status=$(curl -sS "${CURL_TIMEOUT[@]}" \
		"${HUATUO_BAMAI_ADDR}/openapi.json" -o "${response_file}" \
		-w '%{http_code}' 2> "${error_file}") || curl_status=$?
	if [[ ${curl_status} -ne 0 || "${status}" != "200" ]]; then
		report_http_response "GET /openapi.json" "${response_file}" "${error_file}"
		fatal "GET /openapi.json: curl exited ${curl_status}, status ${status:-unavailable}"
	fi
	jq -e '
        .paths["/v1/config"].put as $operation
        | .components.schemas.UpdateConfigRequest as $request
        | ($operation.security[0].BearerAuth == [])
          and ($operation.requestBody.required == true)
          and ($operation.responses["204"].description != null)
          and ($request.additionalProperties == false)
          and ($request.required == ["config"])
          and ($request.properties.config.minProperties == 1)
          and ($request.properties.config.additionalProperties["x-go-type"]
            == "json.RawMessage")
    ' "${response_file}" > /dev/null \
		|| fatal "Node OpenAPI omitted the configuration update contract"
}

assert_persisted_config() {
	local config_file="${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf"
	grep -Fq 'BlackList = ["dropwatch", "netdev_hw"]' "${config_file}" \
		|| fatal "persisted config omitted the BlackList update"
	grep -Fq 'CPULimitCores = 1.5' "${config_file}" \
		|| fatal "persisted config omitted the CPULimitCores update"
	grep -Fq 'MemoryLimitMiB = 1024' "${config_file}" \
		|| fatal "persisted config omitted the MemoryLimitMiB update"
}

integration_huatuo_bamai_start write_config_api_config
assert_openapi_contract
assert_config_error missing-auth /v1/config application/json \
	'{"config":{"Runtime.MemoryLimitMiB":1024}}' 401 unauthenticated false
assert_config_error unsupported-media /v1/config text/plain \
	'{"config":{"Runtime.MemoryLimitMiB":1024}}' 415 unsupported_media_type true
assert_config_error empty-config /v1/config application/json \
	'{"config":{}}' 400 invalid_request true
assert_config_error unknown-key /v1/config application/json \
	'{"config":{"NotExist":1}}' 400 invalid_request true
assert_config_error legacy-route /config application/json \
	'{"config":{"Runtime.MemoryLimitMiB":1024}}' 404 route_not_found true

request_config_api update-config /v1/config application/json \
	'{"config":{"BlackList":["dropwatch","netdev_hw"],"Runtime.CPULimitCores":1.5,"Runtime.MemoryLimitMiB":1024}}' true
if [[ ${config_curl_status} -ne 0 ]]; then
	fatal "update-config: curl exited ${config_curl_status}"
fi
assert_eq "${config_http_status}" "204" "update-config status" \
	|| fatal "update-config returned status ${config_http_status}, expected 204"
[[ ! -s "${config_response_file}" ]] \
	|| fatal "update-config returned a non-empty response: $(< "${config_response_file}")"
assert_persisted_config
assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" huatuo-bamai
