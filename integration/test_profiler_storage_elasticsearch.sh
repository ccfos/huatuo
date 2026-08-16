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

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_storage.sh"

# Bash cannot export arrays from the integration runner to this test process.
CURL_TIMEOUT=(--connect-timeout 2 --max-time 3)

command -v docker >/dev/null || skip "docker command is not installed"
docker info >/dev/null 2>&1 || skip "docker daemon is unavailable"
command -v curl >/dev/null || skip "curl command is not installed"
command -v jq >/dev/null || skip "jq command is not installed"
command -v go >/dev/null || fatal "go command is not installed"

cleanup() {
	local status=$?
	if [[ ${status} -ne 0 ]]; then
		elasticsearch_dump_logs || true
	fi
	elasticsearch_stop || true
}
trap cleanup EXIT

elasticsearch_start

test_profile_storage_id_migration() {
	local index="huatuo-profile-migration-test-${RANDOM}"
	local alias="${index}-alias"
	local alias_index="${index}-alias-target"
	local document

	curl -fsS "${CURL_TIMEOUT[@]}" \
		-H "Content-Type: application/json" \
		-X PUT "${ELASTICSEARCH_ADDR}/${index}" \
		-d '{"mappings":{"properties":{"profile_storage_id":{"type":"keyword"}}}}' \
		>/dev/null

	for document in legacy-a legacy-b; do
		curl -fsS "${CURL_TIMEOUT[@]}" \
			-H "Content-Type: application/json" \
			-X PUT "${ELASTICSEARCH_ADDR}/${index}/_doc/${document}?refresh=true" \
			-d "{
				\"uploaded_time\":\"2026-08-16T02:00:00.000000001Z\",
				\"tracer_id\":\"trace-shared\",
				\"tracer_data\":{\"flamedata\":{\"profile\":{\"time_nanos\":1}}}
			}" >/dev/null
	done
	curl -fsS "${CURL_TIMEOUT[@]}" \
		-H "Content-Type: application/json" \
		-X PUT "${ELASTICSEARCH_ADDR}/${index}/_doc/current?refresh=true" \
		-d '{
			"profile_storage_id":"preserved-current-id",
			"tracer_data":{"flamedata":{"profile_type":"process_cpu:cpu:nanoseconds:cpu:nanoseconds"}}
		}' >/dev/null
	curl -fsS "${CURL_TIMEOUT[@]}" \
		-H "Content-Type: application/json" \
		-X PUT "${ELASTICSEARCH_ADDR}/${index}/_doc/non-profile?refresh=true" \
		-d '{"tracer_data":{"flamedata":{"cpusys":18}}}' >/dev/null
	local check_run checked_id
	check_run=$(ELASTICSEARCH_URL="${ELASTICSEARCH_ADDR}" \
		ELASTICSEARCH_INDEX="${index}" \
		"${ROOT_DIR}/build/migrate-profile-storage-id.sh" --check)
	grep -q 'without profile_storage_id: 2' <<<"${check_run}" ||
		fatal "profile storage ID check did not report legacy documents"
	grep -q 'profile_storage_id.keyword mapping: missing' <<<"${check_run}" ||
		fatal "profile storage ID check did not report the missing mapping"
	grep -q 'without profile_storage_id.keyword value: 1' <<<"${check_run}" ||
		fatal "profile storage ID check did not report unsortable current IDs"
	checked_id=$(curl -fsS "${CURL_TIMEOUT[@]}" \
		"${ELASTICSEARCH_ADDR}/${index}/_doc/legacy-a" |
		jq -r '._source.profile_storage_id // "missing"')
	assert_eq "${checked_id}" "missing" \
		"check mode does not update profile documents" ||
		fatal "profile storage ID check changed a document"

	ELASTICSEARCH_URL="${ELASTICSEARCH_ADDR}" \
		ELASTICSEARCH_INDEX="${index}" \
		"${ROOT_DIR}/build/migrate-profile-storage-id.sh"

	for document in legacy-a legacy-b; do
		local migrated_id
		migrated_id=$(curl -fsS "${CURL_TIMEOUT[@]}" \
			"${ELASTICSEARCH_ADDR}/${index}/_doc/${document}" |
			jq -er '._source.profile_storage_id')
		assert_eq "${migrated_id}" "${document}" \
			"legacy profile_storage_id uses Elasticsearch _id" ||
			fatal "legacy profile storage ID migration failed"
	done

	local current_id non_profile_id
	current_id=$(curl -fsS "${CURL_TIMEOUT[@]}" \
		"${ELASTICSEARCH_ADDR}/${index}/_doc/current" |
		jq -er '._source.profile_storage_id')
	assert_eq "${current_id}" "preserved-current-id" \
		"current profile_storage_id is preserved" ||
		fatal "current profile storage ID changed"
	local current_sort
	current_sort=$(curl -fsS "${CURL_TIMEOUT[@]}" \
		-H "Content-Type: application/json" \
		-X POST "${ELASTICSEARCH_ADDR}/${index}/_search" \
		-d '{"query":{"ids":{"values":["current"]}},"sort":["profile_storage_id.keyword"]}' |
		jq -er '.hits.hits[0].sort[0]')
	assert_eq "${current_sort}" "preserved-current-id" \
		"current profile_storage_id is backfilled into the sort field" ||
		fatal "current profile storage ID is not sortable"
	non_profile_id=$(curl -fsS "${CURL_TIMEOUT[@]}" \
		"${ELASTICSEARCH_ADDR}/${index}/_doc/non-profile" |
		jq -r '._source.profile_storage_id // "missing"')
	assert_eq "${non_profile_id}" "missing" \
		"non-profile document is not migrated" ||
		fatal "non-profile document was changed"
	curl -fsS "${CURL_TIMEOUT[@]}" \
		"${ELASTICSEARCH_ADDR}/${index}/_mapping/field/profile_storage_id.keyword" |
		jq -e 'to_entries[0].value.mappings["profile_storage_id.keyword"].mapping.keyword.type == "keyword"' \
			>/dev/null ||
		fatal "migrated profile_storage_id is not sortable as keyword"

	local second_run
	second_run=$(ELASTICSEARCH_URL="${ELASTICSEARCH_ADDR}" \
		ELASTICSEARCH_INDEX="${index}" \
		"${ROOT_DIR}/build/migrate-profile-storage-id.sh")
	grep -q 'without profile_storage_id: 0' <<<"${second_run}" ||
		fatal "profile storage ID migration is not idempotent"

	curl -fsS "${CURL_TIMEOUT[@]}" \
		-X PUT "${ELASTICSEARCH_ADDR}/${alias_index}" >/dev/null
	curl -fsS "${CURL_TIMEOUT[@]}" \
		-H "Content-Type: application/json" \
		-X POST "${ELASTICSEARCH_ADDR}/_aliases" \
		-d "{\"actions\":[{\"add\":{\"index\":\"${index}\",\"alias\":\"${alias}\"}},{\"add\":{\"index\":\"${alias_index}\",\"alias\":\"${alias}\"}}]}" \
		>/dev/null
	if ELASTICSEARCH_URL="${ELASTICSEARCH_ADDR}" \
		ELASTICSEARCH_INDEX="${alias}" \
		"${ROOT_DIR}/build/migrate-profile-storage-id.sh" >/dev/null 2>&1; then
		fatal "profile storage ID migration accepted a multi-index alias"
	fi

	curl -fsS "${CURL_TIMEOUT[@]}" \
		-X DELETE "${ELASTICSEARCH_ADDR}/${index},${alias_index}" >/dev/null
}

test_profile_storage_id_migration

HUATUO_ES_TEST_ADDR="${ELASTICSEARCH_ADDR}" \
	go test ./internal/profiler/service \
	-run '^TestProfileStorageSearchProfilesElasticsearch$' -count=1
