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

# Exercise profiling filters through the apiserver and real Elasticsearch.
# LabelValues avoids coupling storage filtering to pprof decoding or sampling.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_storage.sh"
source "${ROOT_DIR}/integration/config.sh"

readonly PROFILE_FILTER_INDEX="huatuo_profile_filters"
readonly PROFILE_FILTER_TOKEN="profile-filter-admin"
readonly PROFILE_FILTER_TYPE="process_cpu:cpu:nanoseconds:cpu:nanoseconds"
readonly PROFILE_FILTER_ENDPOINT="/v1/profiling/flamegraph/querier.v1.QuerierService/LabelValues"
readonly PROFILE_FILTER_REQUEST="${HUATUO_BAMAI_TEST_TMPDIR}/filter-request.proto"
readonly PROFILE_FILTER_RESPONSE="${HUATUO_BAMAI_TEST_TMPDIR}/filter-response.proto"
readonly PROFILE_FILTER_EXPECTED="${HUATUO_BAMAI_TEST_TMPDIR}/filter-expected.proto"

require_commands docker jq curl cmp od ss timeout

docker info > /dev/null 2>&1 || skip "docker daemon is unavailable"
profile_filter_port=$(allocate_available_port) || fatal "failed to allocate an apiserver port"
readonly PROFILE_FILTER_PORT="${profile_filter_port}"
readonly PROFILE_FILTER_ADDR="http://127.0.0.1:${PROFILE_FILTER_PORT}"

cleanup() {
	local status=$?
	trap - EXIT
	huatuo_apiserver_stop || status=1
	if [[ ${status} -ne 0 ]]; then
		storage_dump_logs || true
	fi
	storage_stop || status=1
	exit "${status}"
}
trap cleanup EXIT

assert_profile_storage_request() {
	local operation=$1 response_file=$2 status curl_status=0
	local error_file="${response_file}.err"
	shift 2
	status=$(storage_curl "$@" -o "${response_file}" -w '%{http_code}' \
		2> "${error_file}") || curl_status=$?
	log_info "${operation} status=${status}, curl=${curl_status}"
	report_http_response "${operation}" "${response_file}" "${error_file}"
	assert_eq "${curl_status}" "0" "${operation} transport" || fatal "${operation} request failed"
	assert_eq "${status}" "200" "${operation} HTTP status" || fatal "${operation} returned HTTP ${status}"
}

assert_profile_filter() {
	local matcher=$1 expected=$2 length status curl_status=0
	# LabelValuesRequest: name (field 1), matcher (field 2), epoch time bounds.
	# These fixed ASCII selectors fit a one-byte protobuf length.
	((${#matcher} < 128)) || fatal "profile selector exceeds fixture encoding limit"
	printf -v length '%03o' "${#matcher}"
	printf '\012\002id\022%b%s' "\\${length}" "${matcher}" > "${PROFILE_FILTER_REQUEST}"
	printf '%b' "${expected}" > "${PROFILE_FILTER_EXPECTED}"
	status=$(curl -sS "${CURL_TIMEOUT[@]}" \
		-H "Authorization: Bearer ${PROFILE_FILTER_TOKEN}" \
		-H 'Content-Type: application/proto' \
		--data-binary "@${PROFILE_FILTER_REQUEST}" \
		-o "${PROFILE_FILTER_RESPONSE}" -w '%{http_code}' \
		"${PROFILE_FILTER_ADDR}${PROFILE_FILTER_ENDPOINT}") || curl_status=$?
	log_info "profile filter status=${status}, curl=${curl_status}, matcher=${matcher}"
	od -An -tx1c "${PROFILE_FILTER_RESPONSE}" || true
	assert_eq "${curl_status}" "0" "profile filter transport" || fatal "profile filter request failed"
	assert_eq "${status}" "200" "profile filter HTTP status" || fatal "profile filter response is not successful"
	if ! cmp -s "${PROFILE_FILTER_EXPECTED}" "${PROFILE_FILTER_RESPONSE}"; then
		log_error "expected LabelValues response:"
		od -An -tx1c "${PROFILE_FILTER_EXPECTED}"
		fatal "profile filter returned unexpected IDs; actual response shown above"
	fi
}

storage_start
assert_profile_storage_request "create index" "${HUATUO_BAMAI_TEST_TMPDIR}/create-index.json" \
	-X PUT "${STORAGE_ADDR}/${PROFILE_FILTER_INDEX}" \
	-H 'Content-Type: application/json' -d '{"mappings":{"properties":{
  "tracer_id":{"type":"keyword"},
  "hostname":{"type":"text","fields":{"keyword":{"type":"keyword"}}},
  "container_hostname":{"type":"text","fields":{"keyword":{"type":"keyword"}}},
  "uploaded_timestamp":{"type":"date"},
  "profile_data":{"properties":{"profile_type":{"type":"text","fields":{"keyword":{"type":"keyword"}}}}}
 }}}'

jq -cn --arg type "${PROFILE_FILTER_TYPE}" '
 [
  {tracer_id:"host-absent",hostname:"host-a"},
  {tracer_id:"host-null",hostname:"host-a",container_hostname:null},
  {tracer_id:"host-empty",hostname:"host-a",container_hostname:""},
  {tracer_id:"container-exact",hostname:"host-a",container_hostname:"pod-a"},
  {tracer_id:"container-phrase",hostname:"host-a",container_hostname:"pod-a extra"},
  {tracer_id:"host-other",hostname:"host-b"},
  {tracer_id:"host-phrase",hostname:"host-a extra"},
  {tracer_id:"host-before",hostname:"host-a",uploaded_timestamp:"1969-12-31T23:59:59.999Z"},
  {tracer_id:"host-after",hostname:"host-a",uploaded_timestamp:"1970-01-01T00:00:00.001Z"}
 ][] | {index:{_id:.tracer_id}},
 ({uploaded_timestamp:"1970-01-01T00:00:00.000Z",profile_data:{profile_type:$type}} + .)
' > "${HUATUO_BAMAI_TEST_TMPDIR}/profiles.ndjson"
assert_profile_storage_request "seed profiles" "${HUATUO_BAMAI_TEST_TMPDIR}/seed-profiles.json" \
	-X POST "${STORAGE_ADDR}/${PROFILE_FILTER_INDEX}/_bulk?refresh=true" \
	-H 'Content-Type: application/x-ndjson' \
	--data-binary "@${HUATUO_BAMAI_TEST_TMPDIR}/profiles.ndjson"
assert_eq "$(jq -r '.errors' "${HUATUO_BAMAI_TEST_TMPDIR}/seed-profiles.json")" \
	"false" "seed profile documents" || fatal "profile fixture writes failed"

APISERVER_PORT="${PROFILE_FILTER_PORT}" APISERVER_ADDR="${PROFILE_FILTER_ADDR}" \
	API_TOKEN="${PROFILE_FILTER_TOKEN}" \
	integration_huatuo_apiserver_start write_profile_query_apiserver_config

assert_profile_filter \
	"{__profile_type__=\"${PROFILE_FILTER_TYPE}\",hostname=\"host-a\"}" \
	'\012\013host-absent\012\011host-null'
assert_profile_filter \
	"{__profile_type__=\"${PROFILE_FILTER_TYPE}\",container_hostname=\"pod-a\"}" \
	'\012\017container-exact'
assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.log" "huatuo-apiserver"
