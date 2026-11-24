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

assert_profile_label_names() {
	local response_file="${HUATUO_BAMAI_TEST_TMPDIR}/label-names.proto"
	local expected_file="${HUATUO_BAMAI_TEST_TMPDIR}/label-names-expected.proto"
	local error_file="${HUATUO_BAMAI_TEST_TMPDIR}/label-names.err"
	local result curl_status=0 name length
	# LabelNamesResponse repeats string field 1; these ASCII names fit one-byte lengths.
	for name in id scope region hostname container_id container_name container_hostname container_namespace container_type container_qos; do
		printf -v length '%03o' "${#name}"
		printf '\012%b%s' "\\${length}" "${name}"
	done > "${expected_file}"

	result=$(curl -sS "${CURL_TIMEOUT[@]}" \
		-H "Authorization: Bearer ${PROFILE_FILTER_TOKEN}" \
		-H 'Content-Type: application/proto' --data-binary '' \
		-o "${response_file}" -w '%{http_code} %{content_type}' \
		"${PROFILE_FILTER_ADDR}/v1/profiling/flamegraph/querier.v1.QuerierService/LabelNames" \
		2> "${error_file}") || curl_status=$?
	log_info "empty LabelNames status/content-type=${result}, curl=${curl_status}"
	report_http_response "empty LabelNames" "${response_file}" "${error_file}"
	assert_eq "${curl_status}" 0 "empty LabelNames transport" || fatal "LabelNames request failed"
	assert_eq "${result}" '200 application/proto' "empty LabelNames response" \
		|| fatal "empty LabelNames request returned unexpected status or content type"
	if ! cmp -s "${expected_file}" "${response_file}"; then
		log_error "expected LabelNames response:"
		od -An -tx1c "${expected_file}"
		log_error "actual LabelNames response:"
		od -An -tx1c "${response_file}"
		fatal "LabelNames returned unexpected labels"
	fi
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
	integration_huatuo_apiserver_start write_config_apiserver_profile_query

assert_profile_label_names
assert_profile_query_result LabelNames '{}' \
	'.names == ["id","scope","region","hostname","container_id","container_name","container_hostname","container_namespace","container_type","container_qos"]'
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
jq -cn --arg type "${PROFILE_FILTER_TYPE}" '
 [
  {id:"sample-task-a",weight:7,pid:123,comm:"worker",cpu:0,tag:"a+b"},
  {id:"sample-task-a",weight:11,pid:456,comm:"all",cpu:1,tag:"other"},
  {id:"sample-task-b",weight:99,pid:789,comm:"worker",cpu:0,tag:"outside"}
 ][] | {index:{}}, {
  tracer_id:.id,tracer_type:"profiling",hostname:"host-a",region:"east",
  started_timestamp:"1970-01-01T00:00:00Z",uploaded_timestamp:"1970-01-01T00:00:00Z",
  profile_data:{profile_type:$type,profile:{
   string_table:["","cpu","nanoseconds","work","pid","comm",.comm,"future_label",.tag],
   sample_type:[{type:1,unit:2}],function:[{id:1,name:3}],
   location:[{id:1,line:[{function_id:1}]}],
   sample:[{location_id:[1],value:[.weight],label:[
    {key:4,num:.pid},{key:5,str:6},{key:1,num:.cpu},{key:7,str:8}
   ]}]
  }}
 }
' > "${HUATUO_BAMAI_TEST_TMPDIR}/sample-profiles.ndjson"
assert_profile_storage_request "seed sample labels" "${HUATUO_BAMAI_TEST_TMPDIR}/seed-sample-labels.json" \
	-X POST "${STORAGE_ADDR}/${PROFILE_FILTER_INDEX}/_bulk?refresh=true" \
	-H 'Content-Type: application/x-ndjson' \
	--data-binary "@${HUATUO_BAMAI_TEST_TMPDIR}/sample-profiles.ndjson"
assert_eq "$(jq -r '.errors' "${HUATUO_BAMAI_TEST_TMPDIR}/seed-sample-labels.json")" \
	false "seed sample labels" || fatal "sample label writes failed"

# Exercise the dashboard context against two tasks so value lookup cannot leak.
profile_filter_dashboard_selector=$(jq -r --arg type "${PROFILE_FILTER_TYPE}" '
	.panels[] | select(.id == 5) | .targets[0].data | fromjson | .[0]
	| with_entries(.value |= (
		if . == "${type:percentencode}" then $type
		elif . == "${region:percentencode}" then "east"
		elif . == "${hostname:percentencode}" then "host-a"
		elif . == "${scope:text}" then "host"
		elif . == "${task_id:percentencode}" then "sample-task-a"
		else "*" end
	)) | del(.filters) | .__profile_type__ = .profile_type | del(.profile_type)
	| "{" + (to_entries | map(.key + "=" + (.value | tojson)) | join(",")) + "}"
' "${ROOT_DIR}/build/docker/grafana/dashboards/continuous-profiling.json")
assert_profile_query_result LabelValues \
	"$(jq -nc --arg selector "$profile_filter_dashboard_selector" '{name:"pid",matchers:[$selector]}')" \
	'.names == ["123","456"]'
assert_profile_query_result LabelValues \
	"$(jq -nc --arg selector "${profile_filter_dashboard_selector%\}},comm=\"worker\"}" '{name:"pid",matchers:[$selector]}')" \
	'.names == ["123"]'

profile_filter_sample_selector="{__profile_type__=\"${PROFILE_FILTER_TYPE}\",scope=\"host\",id=\"sample-task-a\"}"
assert_profile_query_result LabelNames \
	"$(jq -nc --arg selector "$profile_filter_sample_selector" '{matchers:[$selector]}')" \
	'(.names | index("pid") != null) and (.names | index("future_label") != null)'
assert_profile_query_result LabelValues \
	"$(jq -nc --arg selector "$profile_filter_sample_selector" '{name:"pid",matchers:[$selector]}')" \
	'.names == ["123","456"]'
assert_profile_query_result LabelValues \
	"$(jq -nc --arg selector "${profile_filter_sample_selector%\}},comm=\"worker\"}" '{name:"cpu",matchers:[$selector]}')" \
	'.names == ["0"]'
assert_profile_query_result LabelValues \
	"$(jq -nc --arg selector "$profile_filter_sample_selector" '{name:"future_label",matchers:[$selector]}')" \
	'.names == ["a+b","other"]'
assert_profile_query_result LabelValues \
	"$(jq -nc --arg selector "$profile_filter_sample_selector" '{name:"unknown_label",matchers:[$selector]}')" \
	'.names == []'
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
assert_profile_query_result LabelValues \
	'{"name":"pid","matchers":["{__profile_type__=\"memory:physical_usage:bytes:space:bytes\",id=\"live-task\"}"],"end":"1000"}' \
	'.names == []'

# Indexed metadata remains queryable for current documents without decoding pprof.
profile_filter_metadata_selector="{__profile_type__=\"${PROFILE_FILTER_TYPE}\",id=\"container-exact\"}"
for profile_filter_metadata_pair in "profile_type|${PROFILE_FILTER_TYPE}" 'tracer_id|container-exact' \
	'container_name|worker' 'container_namespace|production' 'container_type|normal' 'container_qos|burstable' 'tracer_name|profiling'; do
	profile_filter_metadata_name=${profile_filter_metadata_pair%%|*}
	profile_filter_metadata_value=${profile_filter_metadata_pair#*|}
	assert_profile_query_result LabelValues \
		"$(jq -nc --arg selector "$profile_filter_metadata_selector" --arg name "$profile_filter_metadata_name" '{name:$name,matchers:[$selector]}')" \
		"$(jq -nr --arg value "$profile_filter_metadata_value" '".names == [" + ($value|tojson) + "]"')"
done

assert_profile_query "$PROFILE_FILTER_ADDR" "$PROFILE_FILTER_TOKEN" LabelValues \
	"$(jq -nc --arg selector "$profile_filter_sample_selector" '{name:"__future__",matchers:[$selector]}')" \
	"${HUATUO_BAMAI_TEST_TMPDIR}/invalid-sample-query.json" 400
assert_eq "$(jq -r '.error.code' "${HUATUO_BAMAI_TEST_TMPDIR}/invalid-sample-query.json")" \
	invalid_request "reserved sample label error" || fatal "reserved label returned an unexpected error"
assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.log" "huatuo-apiserver"
