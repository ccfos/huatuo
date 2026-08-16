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

usage() {
	cat <<'EOF'
Usage: migrate-profile-storage-id.sh [--check]

Environment:
  ELASTICSEARCH_URL       Elasticsearch/OpenSearch URL (default: http://127.0.0.1:9200)
  ELASTICSEARCH_INDEX     Profile index (default: huatuo_bamai)
  ELASTICSEARCH_USERNAME  Optional basic-auth username
  ELASTICSEARCH_PASSWORD  Optional basic-auth password

--check reports legacy document count and sortable mapping state without writes.
EOF
}

mode="migrate"
case "${1:-}" in
"") ;;
--check) mode="check" ;;
-h | --help)
	usage
	exit 0
	;;
*)
	usage >&2
	exit 2
	;;
esac

command -v curl >/dev/null || {
	echo "curl is required" >&2
	exit 1
}
command -v jq >/dev/null || {
	echo "jq is required" >&2
	exit 1
}

elasticsearch_url=${ELASTICSEARCH_URL:-http://127.0.0.1:9200}
elasticsearch_url=${elasticsearch_url%/}
elasticsearch_index=${ELASTICSEARCH_INDEX:-huatuo_bamai}
elasticsearch_username=${ELASTICSEARCH_USERNAME:-}
elasticsearch_password=${ELASTICSEARCH_PASSWORD:-}

if [[ ! ${elasticsearch_index} =~ ^[a-z0-9][a-z0-9._-]*$ ]]; then
	echo "ELASTICSEARCH_INDEX must be one explicit lowercase index name" >&2
	exit 2
fi
if [[ -n ${elasticsearch_password} && -z ${elasticsearch_username} ]]; then
	echo "ELASTICSEARCH_USERNAME is required when ELASTICSEARCH_PASSWORD is set" >&2
	exit 2
fi

curl_options=(
	--fail-with-body
	--silent
	--show-error
	--connect-timeout 5
	--max-time 1800
	--header "Content-Type: application/json"
)
if [[ -n ${elasticsearch_username} ]]; then
	curl_options+=(--user "${elasticsearch_username}:${elasticsearch_password}")
fi

profile_query='{
  "query": {
    "bool": {
      "filter": [
        {
          "bool": {
            "should": [
              {"exists": {"field": "tracer_data.flamedata.profile_type"}},
              {"exists": {"field": "tracer_data.flamedata.profile"}}
            ],
            "minimum_should_match": 1
          }
        }
      ],
      "must_not": [
        {"exists": {"field": "profile_storage_id"}}
      ]
    }
  }
}'

missing_sort_value_query='{
  "query": {
    "bool": {
      "filter": [
        {
          "bool": {
            "should": [
              {"exists": {"field": "tracer_data.flamedata.profile_type"}},
              {"exists": {"field": "tracer_data.flamedata.profile"}}
            ],
            "minimum_should_match": 1
          }
        },
        {"exists": {"field": "profile_storage_id"}}
      ],
      "must_not": [
        {"exists": {"field": "profile_storage_id.keyword"}}
      ]
    }
  }
}'

index_mapping=$(curl "${curl_options[@]}" \
	--request GET \
	"${elasticsearch_url}/${elasticsearch_index}/_mapping")
resolved_indices=$(jq -r 'keys[]' <<<"${index_mapping}")
if [[ $(wc -l <<<"${resolved_indices}") -ne 1 || ${resolved_indices} != "${elasticsearch_index}" ]]; then
	echo "ELASTICSEARCH_INDEX must resolve to one physical index with the same name" >&2
	echo "Resolved indices: ${resolved_indices//$'\n'/, }" >&2
	exit 2
fi

profile_storage_type=$(jq -r \
	--arg index "${elasticsearch_index}" \
	'.[$index].mappings.properties.profile_storage_id.type // ""' \
	<<<"${index_mapping}")
profile_storage_keyword_type=$(jq -r \
	--arg index "${elasticsearch_index}" \
	'.[$index].mappings.properties.profile_storage_id.fields.keyword.type // ""' \
	<<<"${index_mapping}")

ensure_profile_storage_mapping() {
	if [[ ${profile_storage_keyword_type} == "keyword" ]]; then
		return
	fi
	if [[ -n ${profile_storage_keyword_type} ]]; then
		echo "profile_storage_id.keyword must be mapped as keyword" >&2
		exit 1
	fi
	if [[ -n ${profile_storage_type} && ${profile_storage_type} != "text" && ${profile_storage_type} != "keyword" ]]; then
		echo "profile_storage_id must be text or keyword, got ${profile_storage_type}" >&2
		exit 1
	fi

	local root_type=${profile_storage_type:-text}
	local mapping
	mapping=$(jq -cn --arg root_type "${root_type}" '{
      properties: {
        profile_storage_id: {
          type: $root_type,
          fields: {keyword: {type: "keyword", ignore_above: 256}}
        }
      }
    }')
	curl "${curl_options[@]}" \
		--request PUT \
		--data-binary "${mapping}" \
		"${elasticsearch_url}/${elasticsearch_index}/_mapping" >/dev/null

	local keyword_mapping
	keyword_mapping=$(curl "${curl_options[@]}" \
		--request GET \
		"${elasticsearch_url}/${elasticsearch_index}/_mapping/field/profile_storage_id.keyword")
	if ! jq -e \
		--arg index "${elasticsearch_index}" \
		'.[$index].mappings["profile_storage_id.keyword"].mapping.keyword.type == "keyword"' \
		>/dev/null <<<"${keyword_mapping}"; then
		echo "profile_storage_id.keyword mapping was not created" >&2
		exit 1
	fi
}

count_legacy_profiles() {
	curl "${curl_options[@]}" \
		--request POST \
		--data-binary "${profile_query}" \
		"${elasticsearch_url}/${elasticsearch_index}/_count" |
		jq -er '.count'
}

count_missing_sort_values() {
	curl "${curl_options[@]}" \
		--request POST \
		--data-binary "${missing_sort_value_query}" \
		"${elasticsearch_url}/${elasticsearch_index}/_count" |
		jq -er '.count'
}

pending=$(count_legacy_profiles)
missing_sort_values=$(count_missing_sort_values)
echo "Legacy profile documents without profile_storage_id: ${pending}"
echo "Profile documents without profile_storage_id.keyword value: ${missing_sort_values}"
if [[ ${mode} == "check" ]]; then
	mapping_state=missing
	if [[ ${profile_storage_keyword_type} == "keyword" ]]; then
		mapping_state=present
	fi
	echo "profile_storage_id.keyword mapping: ${mapping_state}"
	exit 0
fi

ensure_profile_storage_mapping
if [[ ${missing_sort_values} -ne 0 ]]; then
	refresh_response=$(curl "${curl_options[@]}" \
		--request POST \
		--data-binary '{
      "query": {
        "bool": {
          "filter": [
            {
              "bool": {
                "should": [
                  {"exists": {"field": "tracer_data.flamedata.profile_type"}},
                  {"exists": {"field": "tracer_data.flamedata.profile"}}
                ],
                "minimum_should_match": 1
              }
            },
            {"exists": {"field": "profile_storage_id"}}
          ],
          "must_not": [
            {"exists": {"field": "profile_storage_id.keyword"}}
          ]
        }
      },
      "script": {
        "lang": "painless",
        "source": "ctx._source.profile_storage_id = ctx._source.profile_storage_id"
      }
    }' \
		"${elasticsearch_url}/${elasticsearch_index}/_update_by_query?refresh=true")
	if ! jq -e '.failures | length == 0' >/dev/null <<<"${refresh_response}"; then
		echo "profile storage ID sort refresh returned document failures" >&2
		jq '.failures' <<<"${refresh_response}" >&2
		exit 1
	fi
	echo "Reindexed existing profile storage IDs: $(jq -er '.updated' <<<"${refresh_response}")"
fi

if [[ ${pending} -eq 0 ]]; then
	remaining_sort_values=$(count_missing_sort_values)
	if [[ ${remaining_sort_values} -ne 0 ]]; then
		echo "Migration incomplete: ${remaining_sort_values} profile storage IDs remain unsortable" >&2
		exit 1
	fi
	exit 0
fi

update_response=$(curl "${curl_options[@]}" \
	--request POST \
	--data-binary '{
    "query": {
      "bool": {
        "filter": [
          {
            "bool": {
              "should": [
                {"exists": {"field": "tracer_data.flamedata.profile_type"}},
                {"exists": {"field": "tracer_data.flamedata.profile"}}
              ],
              "minimum_should_match": 1
            }
          }
        ],
        "must_not": [
          {"exists": {"field": "profile_storage_id"}}
        ]
      }
    },
    "script": {
      "lang": "painless",
      "source": "ctx._source.profile_storage_id = ctx._id"
    }
  }' \
	"${elasticsearch_url}/${elasticsearch_index}/_update_by_query?refresh=true")

if ! jq -e '.failures | length == 0' >/dev/null <<<"${update_response}"; then
	echo "profile storage ID migration returned document failures" >&2
	jq '.failures' <<<"${update_response}" >&2
	exit 1
fi

updated=$(jq -er '.updated' <<<"${update_response}")
remaining=$(count_legacy_profiles)
remaining_sort_values=$(count_missing_sort_values)
echo "Updated profile documents: ${updated}"
if [[ ${remaining} -ne 0 ]]; then
	echo "Migration incomplete: ${remaining} legacy profile documents remain" >&2
	exit 1
fi
if [[ ${remaining_sort_values} -ne 0 ]]; then
	echo "Migration incomplete: ${remaining_sort_values} profile storage IDs remain unsortable" >&2
	exit 1
fi
