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

# Exercise SDK request encoding and response decoding against real terms aggregations.
set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_storage.sh"

require_commands docker go jq curl
docker info > /dev/null 2>&1 || skip "docker daemon is unavailable"

readonly VALUES_INDEX="huatuo_storage_values_test"
readonly VALUES_BIN="${HUATUO_BAMAI_TEST_TMPDIR}/storage-values"
readonly VALUES_RESPONSE="${HUATUO_BAMAI_TEST_TMPDIR}/values.json"
readonly VALUES_ERRORS="${HUATUO_BAMAI_TEST_TMPDIR}/values-errors.log"

cleanup() {
	local status=$?
	if ((status != 0)); then
		storage_dump_logs
	fi
	storage_stop
}
trap cleanup EXIT

assert_storage_values() {
	local field=$1 expected=$2 size=${3:-10}
	local status=0
	"${VALUES_BIN}" --address "${STORAGE_ADDR}" --index "${VALUES_INDEX}" \
		--field "${field}" --size "${size}" \
		> "${VALUES_RESPONSE}" 2> "${VALUES_ERRORS}" || status=$?
	report_http_response "${field} values" "${VALUES_RESPONSE}" "${VALUES_ERRORS}"
	((status == 0)) || fatal "Values(${field}) failed with status ${status}"
	jq -e --argjson expected "${expected}" '. == $expected' "${VALUES_RESPONSE}" \
		> /dev/null || fatal "Values(${field}) did not return ${expected}"
}

go build -o "${VALUES_BIN}" "${ROOT_DIR}/integration/testdata/storage_values.go"
storage_start

values_status=0
values_http_status=$(curl -sS "${CURL_TIMEOUT[@]}" \
	-o "${VALUES_RESPONSE}" -w '%{http_code}' \
	-X PUT "${STORAGE_ADDR}/${VALUES_INDEX}" \
	-H 'Content-Type: application/json' \
	-d '{"mappings":{"properties":{"label":{"type":"keyword"},"number":{"type":"long"},"ratio":{"type":"double"},"unsigned":{"type":"unsigned_long"},"empty":{"type":"keyword"}}}}' \
	2> "${VALUES_ERRORS}") || values_status=$?
report_http_response "create values index" "${VALUES_RESPONSE}" "${VALUES_ERRORS}"
((values_status == 0)) || fatal "create values index failed with curl status ${values_status}"
assert_eq "${values_http_status}" "200" "create values index" \
	|| fatal "create values index status ${values_http_status}, want 200"

cat > "${HUATUO_BAMAI_TEST_TMPDIR}/values.ndjson" << 'EOF'
{"index":{}}
{"label":"node-a","number":42,"ratio":1.25,"unsigned":42}
{"index":{}}
{"label":"node-a","number":42,"ratio":1.25,"unsigned":42}
{"index":{}}
{"label":"node-b","number":-7,"ratio":2.5,"unsigned":7}
EOF

values_status=0
values_http_status=$(curl -sS "${CURL_TIMEOUT[@]}" \
	-o "${VALUES_RESPONSE}" -w '%{http_code}' \
	-X POST "${STORAGE_ADDR}/${VALUES_INDEX}/_bulk?refresh=true" \
	-H 'Content-Type: application/x-ndjson' \
	--data-binary "@${HUATUO_BAMAI_TEST_TMPDIR}/values.ndjson" \
	2> "${VALUES_ERRORS}") || values_status=$?
report_http_response "seed values documents" "${VALUES_RESPONSE}" "${VALUES_ERRORS}"
((values_status == 0)) || fatal "seed values documents failed with curl status ${values_status}"
assert_eq "${values_http_status}" "200" "seed values documents" \
	|| fatal "seed values documents status ${values_http_status}, want 200"
jq -e '.errors == false' "${VALUES_RESPONSE}" > /dev/null \
	|| fatal "seed values documents returned failed bulk items"

assert_storage_values label '["node-a","node-b"]'
assert_storage_values label '["node-a"]' 1
assert_storage_values number '["42","-7"]'
assert_storage_values ratio '["1.25","2.5"]'
assert_storage_values unsigned '["42","7"]'
assert_storage_values empty '[]'
assert_storage_values unmapped '[]'
