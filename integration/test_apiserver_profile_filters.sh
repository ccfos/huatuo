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

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_storage.sh"
source "${ROOT_DIR}/integration/config.sh"
source "${ROOT_DIR}/integration/lib_apiserver_query.sh"

readonly PROFILE_FILTER_INDEX=$(storage_test_index huatuo_profile_filters)
readonly PROFILE_FILTER_TOKEN="profile-filter-admin"
readonly PROFILE_FILTER_TYPE="process_cpu:cpu:nanoseconds:cpu:nanoseconds"

require_commands docker jq curl ss timeout python3

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
	local matcher=$1 expected=$2
	assert_profile_query_result LabelValues \
		"$(jq -nc --arg matcher "$matcher" '{name:"id",matchers:[$matcher]}')" \
		".names == $expected"
}

assert_profile_query_result() {
	local method=$1 request_json=$2 predicate=$3
	local result="${HUATUO_BAMAI_TEST_TMPDIR}/sample-query.json"
	assert_profile_query "$PROFILE_FILTER_ADDR" "$PROFILE_FILTER_TOKEN" "$method" "$request_json" "$result"
	assert_eq "$(jq "$predicate" "$result")" true "$method result" \
		|| fatal "$method response does not match $predicate; inspect $result"
}

storage_start
assert_profile_storage_request "create index" "${HUATUO_BAMAI_TEST_TMPDIR}/create-index.json" \
	-X PUT "${STORAGE_ADDR}/${PROFILE_FILTER_INDEX}" \
	-H 'Content-Type: application/json' -d '{"mappings":{"properties":{
  "tracer_id":{"type":"keyword","fields":{"keyword":{"type":"keyword"}}},
  "hostname":{"type":"text","fields":{"keyword":{"type":"keyword"}}},
  "container_id":{"type":"text","fields":{"keyword":{"type":"keyword"}}},
  "region":{"type":"text","fields":{"keyword":{"type":"keyword"}}},
  "container_hostname":{"type":"text","fields":{"keyword":{"type":"keyword"}}},
  "container_name":{"type":"text","fields":{"keyword":{"type":"keyword"}}},
  "container_namespace":{"type":"text","fields":{"keyword":{"type":"keyword"}}},
  "container_type":{"type":"text","fields":{"keyword":{"type":"keyword"}}},
  "container_qos":{"type":"text","fields":{"keyword":{"type":"keyword"}}},
  "uploaded_timestamp":{"type":"date"},
  "started_timestamp":{"type":"date"},
  "profile_data":{"properties":{"profile_type":{"type":"text","fields":{"keyword":{"type":"keyword"}}}}}
 }}}'
assert_eq "$(jq -r '.shards_acknowledged' "${HUATUO_BAMAI_TEST_TMPDIR}/create-index.json")" \
	true "profile index primary shard ready" || fatal "profile index primary shard is not active; check storage allocation and disk space"

jq -cn --arg type "${PROFILE_FILTER_TYPE}" '
 [
  {tracer_id:"host-absent",hostname:"host-a"},
  {tracer_id:"host-null",hostname:"host-a",container_id:null},
  {tracer_id:"host-empty",hostname:"host-a",container_id:""},
  {tracer_id:"container-exact",hostname:"host-a",container_id:"container-a",container_hostname:"pod-a",container_name:"worker",container_namespace:"production",container_type:"normal",container_qos:"burstable",tracer_name:"profiling"},
  {tracer_id:"container-phrase",hostname:"host-a",container_id:"container-b",container_name:"worker extra",container_hostname:"pod-a extra",container_namespace:"production extra",container_type:"sidecar",container_qos:"guaranteed"},
  {tracer_id:"host-other",hostname:"host-b"},
  {tracer_id:"host-west",hostname:"host-a",region:"west"},
  {tracer_id:"host-phrase",hostname:"host-a extra"},
  {tracer_id:"host-before",hostname:"host-a",uploaded_timestamp:"1969-12-31T23:59:59.999Z"},
  {tracer_id:"host-after",hostname:"host-a",uploaded_timestamp:"1970-01-01T00:00:00.001Z"}
 ][] | {index:{_id:.tracer_id}},
 ({region:"east",uploaded_timestamp:"1970-01-01T00:00:00.000Z",profile_data:{profile_type:$type,profile:{string_table:["","cpu","nanoseconds"],sample_type:[{type:1,unit:2}],sample:[]}}} + .)
' > "${HUATUO_BAMAI_TEST_TMPDIR}/profiles.ndjson"
assert_profile_storage_request "seed profiles" "${HUATUO_BAMAI_TEST_TMPDIR}/seed-profiles.json" \
	-X POST "${STORAGE_ADDR}/${PROFILE_FILTER_INDEX}/_bulk?refresh=true" \
	-H 'Content-Type: application/x-ndjson' \
	--data-binary "@${HUATUO_BAMAI_TEST_TMPDIR}/profiles.ndjson"
assert_eq "$(jq -r '.errors' "${HUATUO_BAMAI_TEST_TMPDIR}/seed-profiles.json")" \
	"false" "seed profile documents" || fatal "profile fixture writes failed"

# Exercise the dashboard's Lucene interpolation against real storage.
assert_profile_dashboard_types() {
	local scope=$1 container=$2 expected=$3 query response_file
	response_file="${HUATUO_BAMAI_TEST_TMPDIR}/dashboard-types.json"
	query=$(jq -r --arg scope "${scope}" --arg container "${container}" '
		.templating.list[] | select(.name == "type") | .query | fromjson | .query |
		split("${region:lucene}") | join("east") |
		split("${hostname:lucene}") | join("host\\-a") |
		split("${scope:raw}") | join($scope) |
		split("${container_id:lucene}") | join($container)
	' "${ROOT_DIR}/build/docker/grafana/dashboards/continuous-profiling.json")
	assert_profile_storage_request "dashboard profile types" "${response_file}" \
		-X POST "${STORAGE_ADDR}/${PROFILE_FILTER_INDEX}/_search" \
		-H 'Content-Type: application/json' \
		-d "$(jq -cn --arg query "${query}" '{size:0,query:{query_string:{query:$query}},aggs:{types:{terms:{field:"profile_data.profile_type.keyword"}}}}')"
	assert_eq "$(jq -r '.aggregations.types.buckets[0].key // empty' "${response_file}")" \
		"${PROFILE_FILTER_TYPE}" "dashboard profile type" || fatal "dashboard profile type is empty"
	assert_eq "$(jq -r '.hits.total.value' "${response_file}")" \
		"${expected}" "dashboard matching windows" || fatal "dashboard scope filter returned unexpected windows"
}
assert_profile_dashboard_types 'NOT _exists_:container_id' '*' 4
assert_profile_dashboard_types '_exists_:container_id' 'container\-a' 1

APISERVER_PORT="${PROFILE_FILTER_PORT}" APISERVER_ADDR="${PROFILE_FILTER_ADDR}" \
	API_TOKEN="${PROFILE_FILTER_TOKEN}" \
	write_config_apiserver_profile_query "${PROFILE_FILTER_INDEX}" > "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.conf"
APISERVER_ADDR="${PROFILE_FILTER_ADDR}" integration_huatuo_apiserver_start :

assert_profile_filter \
	"{__profile_type__=\"${PROFILE_FILTER_TYPE}\",region=\"east\",hostname=\"host-a\",scope=\"host\"}" \
	'["host-absent","host-null"]'
assert_profile_filter \
	"{__profile_type__=\"${PROFILE_FILTER_TYPE}\",region=\"east\",hostname=\"host-a\",scope=\"container\",container_id=\"container-a\"}" \
	'["container-exact"]'
assert_profile_filter \
	"{__profile_type__=\"${PROFILE_FILTER_TYPE}\",region=\"west\",hostname=\"host-a\",scope=\"host\"}" \
	'["host-west"]'
# Shared metadata filters match complete field values.
for metadata_filter in 'container_name="worker"' 'container_namespace="production"' 'container_type="normal"' 'container_qos="burstable"'; do
	assert_profile_filter \
		"{__profile_type__=\"${PROFILE_FILTER_TYPE}\",region=\"east\",hostname=\"host-a\",scope=\"container\",${metadata_filter}}" \
		'["container-exact"]'
done
# Without scope, hostname is only a node filter and includes container records.
assert_profile_filter \
	"{__profile_type__=\"${PROFILE_FILTER_TYPE}\",region=\"east\",hostname=\"host-a\"}" \
	'["container-exact","container-phrase","host-absent","host-empty","host-null"]'
# Observation order differs from delivery order. An out-of-range observation
# delivered inside the range must not replace the latest zero snapshot.
jq -cn '
 [
  {id:"live-old",observed:"1970-01-01T00:00:00.100Z",uploaded:"1970-01-01T00:00:05Z",time:100000000,value:8192},
  {id:"live-zero",observed:"1970-01-01T00:00:00.200Z",uploaded:"1970-01-01T00:00:06Z",time:200000000,value:0},
  {id:"live-outside",observed:"1970-01-01T00:00:02Z",uploaded:"1970-01-01T00:00:00.300Z",time:2000000000,value:4096}
 ][] | {index:{_id:.id}}, {
  tracer_id:"live-task",tracer_type:"profiling",hostname:"host-a",
  started_timestamp:.observed,uploaded_timestamp:.uploaded,
  profile_data:{profile_type:"memory:physical_usage:bytes:space:bytes",profile:{
   time_nanos:.time,string_table:["","physical_usage","bytes","work"],
   sample_type:[{type:1,unit:2}],function:[{id:1,name:3}],
   location:[{id:1,line:[{function_id:1}]}],
   sample:(if .value == 0 then [] else [{location_id:[1],value:[.value]}] end)
  }}
 }
' > "${HUATUO_BAMAI_TEST_TMPDIR}/live-profiles.ndjson"
assert_profile_storage_request "seed live snapshots" "${HUATUO_BAMAI_TEST_TMPDIR}/seed-live.json" \
	-X POST "${STORAGE_ADDR}/${PROFILE_FILTER_INDEX}/_bulk?refresh=true" \
	-H 'Content-Type: application/x-ndjson' \
	--data-binary "@${HUATUO_BAMAI_TEST_TMPDIR}/live-profiles.ndjson"
assert_eq "$(jq -r '.errors' "${HUATUO_BAMAI_TEST_TMPDIR}/seed-live.json")" \
	false "seed live snapshots" || fatal "live snapshot writes failed"

assert_profile_query_result SelectMergeStacktraces \
	'{"profileTypeID":"memory:physical_usage:bytes:space:bytes","labelSelector":"{scope=\"host\",id=\"live-task\"}","end":"1000"}' \
	'.flamegraph | .total == "0" and .names == ["total"] and .levels == [{values:[0,0,0,0]}]'

# Two tasks share stack identities; task bounds and sample filters must both apply.
jq -c '.[] | {index:{}}, .' \
	"${ROOT_DIR}/integration/testdata/test_apiserver_profile_samples.json" \
	> "${HUATUO_BAMAI_TEST_TMPDIR}/sample-profiles.ndjson"
assert_profile_storage_request "seed sample labels" "${HUATUO_BAMAI_TEST_TMPDIR}/seed-sample-labels.json" \
	-X POST "${STORAGE_ADDR}/${PROFILE_FILTER_INDEX}/_bulk?refresh=true" \
	-H 'Content-Type: application/x-ndjson' \
	--data-binary "@${HUATUO_BAMAI_TEST_TMPDIR}/sample-profiles.ndjson"
assert_eq "$(jq -r '.errors' "${HUATUO_BAMAI_TEST_TMPDIR}/seed-sample-labels.json")" \
	false "seed sample labels" || fatal "sample label writes failed"

assert_profile_query_result SelectMergeStacktraces \
	"$(jq -nc --arg type "$PROFILE_FILTER_TYPE" \
		'{profileTypeID:$type,labelSelector:"{scope=\"host\",id=\"sample-task-a\",pid=\"123\"}"}')" \
	'.flamegraph.total == "7"'
# Literal values must not become regexes or an implicit All selection.
assert_profile_query_result SelectMergeStacktraces \
	"$(jq -nc --arg type "$PROFILE_FILTER_TYPE" \
		'{profileTypeID:$type,labelSelector:"{scope=\"host\",id=\"sample-task-a\",comm=\"all\"}"}')" \
	'.flamegraph.total == "11"'
assert_profile_query_result SelectMergeStacktraces \
	"$(jq -nc --arg type "$PROFILE_FILTER_TYPE" \
		'{profileTypeID:$type,labelSelector:"{scope=\"host\",id=\"sample-task-a\",future_label=\"a+b\"}"}')" \
	'.flamegraph.total == "7"'
assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.log" "huatuo-apiserver"
