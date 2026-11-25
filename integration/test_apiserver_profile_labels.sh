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

# Verify LabelNames/LabelValues through HTTP, Protobuf and real Elasticsearch.
# Sampling and symbol resolution remain covered by profiler and lifecycle tests.
set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_storage.sh"
source "${ROOT_DIR}/integration/config.sh"
source "${ROOT_DIR}/integration/lib_apiserver_query.sh"

readonly PROFILE_LABEL_INDEX=$(storage_test_index huatuo_profile_labels)
readonly PROFILE_LABEL_TOKEN="profile-label-admin"
readonly PROFILE_LABEL_READER="profile-label-reader"
readonly PROFILE_LABEL_TYPE="process_cpu:cpu:nanoseconds:cpu:nanoseconds"
readonly PROFILE_LABEL_DOCUMENT_NAMES='["id","scope","region","hostname","container_id","container_name","container_hostname","container_namespace","container_type","container_qos"]'

require_commands docker jq curl ss timeout python3
docker info > /dev/null 2>&1 || skip "docker daemon is unavailable"
profile_label_port=$(allocate_available_port) || fatal "failed to allocate an apiserver port"
readonly PROFILE_LABEL_PORT="${profile_label_port}"
readonly PROFILE_LABEL_ADDR="http://127.0.0.1:${PROFILE_LABEL_PORT}"

cleanup() {
	local status=$?
	trap - EXIT
	huatuo_apiserver_stop || status=1
	if [[ ${status} -ne 0 ]]; then storage_dump_logs || true; fi
	storage_stop || status=1
	exit "${status}"
}
trap cleanup EXIT

assert_profile_labels() {
	local method=$1 request=$2 expected=$3
	local result="${HUATUO_BAMAI_TEST_TMPDIR}/labels-${method}.json"
	assert_profile_query "$PROFILE_LABEL_ADDR" "$PROFILE_LABEL_TOKEN" "$method" "$request" "$result"
	assert_eq "$(jq -c '.names' "$result")" "$(jq -c . <<< "$expected")" "$method labels" \
		|| fatal "$method returned unexpected labels; inspect $result"
}

assert_profile_label_error() {
	local method=$1 request=$2
	local result="${HUATUO_BAMAI_TEST_TMPDIR}/invalid-${method}.json"
	assert_profile_query "$PROFILE_LABEL_ADDR" "$PROFILE_LABEL_TOKEN" "$method" "$request" "$result" 400
	assert_eq "$(jq -r '.error.code' "$result")" invalid_request "$method error code" \
		|| fatal "$method must reject the invalid query; inspect $result"
}

storage_start
storage_request "${HUATUO_BAMAI_TEST_TMPDIR}/create-index.json" \
	-X PUT "${STORAGE_ADDR}/${PROFILE_LABEL_INDEX}" \
	-H 'Content-Type: application/json' -d '{"mappings":{"properties":{
  "tracer_id":{"type":"keyword","fields":{"keyword":{"type":"keyword"}}},
  "hostname":{"type":"text","fields":{"keyword":{"type":"keyword"}}},
  "container_id":{"type":"text","fields":{"keyword":{"type":"keyword"}}},
  "region":{"type":"text","fields":{"keyword":{"type":"keyword"}}},
  "container_name":{"type":"text","fields":{"keyword":{"type":"keyword"}}},
  "container_hostname":{"type":"text","fields":{"keyword":{"type":"keyword"}}},
  "container_namespace":{"type":"text","fields":{"keyword":{"type":"keyword"}}},
  "container_type":{"type":"text","fields":{"keyword":{"type":"keyword"}}},
  "container_qos":{"type":"text","fields":{"keyword":{"type":"keyword"}}},
  "uploaded_timestamp":{"type":"date"},"started_timestamp":{"type":"date"},
  "profile_data":{"properties":{"profile_type":{"type":"text","fields":{"keyword":{"type":"keyword"}}}}}
 }}}'
report_http_response "create label index" "${HUATUO_BAMAI_TEST_TMPDIR}/create-index.json" "${HUATUO_BAMAI_TEST_TMPDIR}/create-index.json.err"
assert_storage_index_created "${HUATUO_BAMAI_TEST_TMPDIR}/create-index.json"

jq -c '.[] | {index:{}}, .' \
	"${ROOT_DIR}/integration/testdata/test_apiserver_profile_samples.json" \
	> "${HUATUO_BAMAI_TEST_TMPDIR}/labels.ndjson"
# Distinct task, node, Region, scope, type and time values expose leaked windows.
jq -c --arg type "$PROFILE_LABEL_TYPE" '
 .[0] as $base | [
  {tracer_id:"sample-task-a",container_id:"container-a",pid:901,key:"container_a"},
  {tracer_id:"sample-task-a",container_id:"container-b",pid:902,key:"container_b"},
  {tracer_id:"sample-task-a",hostname:"host-b",pid:903,key:"other_node"},
  {tracer_id:"sample-task-a",region:"west",pid:904,key:"other_region"},
  {tracer_id:"sample-task-a",pid:905,key:"other_type",profile_type:"process_offcpu:offcpu:nanoseconds:offcpu:nanoseconds"},
  {tracer_id:"sample-task-a",pid:906,key:"later_window",observed:"1970-01-01T00:00:01Z",uploaded:"1970-01-01T00:00:02Z"},
  {tracer_id:"sample-task-a",pid:123,key:"duplicate"}
 ][] | . as $case | {index:{}},
 ($base + ($case | del(.pid,.key,.observed,.uploaded,.profile_type)) |
  .started_timestamp = ($case.observed // $base.started_timestamp) |
  .uploaded_timestamp = ($case.uploaded // $base.uploaded_timestamp) |
  .profile_data.profile_type = ($case.profile_type // $type) |
  .profile_data.profile.string_table[1] = (if $case.profile_type then "offcpu" else "cpu" end) |
  .profile_data.profile.string_table += [$case.key] |
  .profile_data.profile.sample[0].label = [{key:4,num:$case.pid},{key:9,str:6}])
' "${ROOT_DIR}/integration/testdata/test_apiserver_profile_samples.json" \
	>> "${HUATUO_BAMAI_TEST_TMPDIR}/labels.ndjson"
# Shared stacks must remain separable by off-CPU reason.
jq -c '
 .[0] as $base | [{reason:"blocked",weight:7,cpu:"0"},{reason:"runqueue",weight:11,cpu:"3"}][]
 | . as $case | {index:{}},
 ($base | .tracer_id="offcpu-task"
  | .profile_data.profile_type="process_offcpu:offcpu:nanoseconds:offcpu:nanoseconds"
  | .profile_data.profile |= (
    (.string_table | length) as $key
    | .string_table[1]="offcpu"
    | .string_table += ["offcpu_reason",$case.reason,"pid","123","comm","worker","cpu",$case.cpu]
    | .sample[0].label=[{key:$key,str:($key+1)},{key:($key+2),str:($key+3)},{key:($key+4),str:($key+5)},{key:($key+6),str:($key+7)}]
    | .sample[0].value=[$case.weight]))
' "${ROOT_DIR}/integration/testdata/test_apiserver_profile_samples.json" \
	>> "${HUATUO_BAMAI_TEST_TMPDIR}/labels.ndjson"
# Metadata values must come from indexed fields even when the profile is empty.
jq -nc --arg type "$PROFILE_LABEL_TYPE" '
 {index:{}}, {tracer_id:"metadata-task",tracer_type:"profiling",hostname:"host-a",region:"east",
 container_id:"metadata-container",container_hostname:"pod-a",container_name:"worker",
 container_namespace:"production",container_type:"normal",container_qos:"burstable",tracer_name:"profiling",
 started_timestamp:"1970-01-01T00:00:00Z",uploaded_timestamp:"1970-01-01T00:00:00Z",
 profile_data:{profile_type:$type,profile:{string_table:["","cpu","nanoseconds"],sample_type:[{type:1,unit:2}],sample:[]}}}
' >> "${HUATUO_BAMAI_TEST_TMPDIR}/labels.ndjson"
# Physical usage chooses the latest observation before applying sample filters.
jq -nc '
 [
  {observed:"1970-01-01T00:00:00.100Z",uploaded:"1970-01-01T00:00:05Z",time:100000000,pid:777},
  {observed:"1970-01-01T00:00:00.200Z",uploaded:"1970-01-01T00:00:06Z",time:200000000,pid:null},
  {observed:"1970-01-01T00:00:02Z",uploaded:"1970-01-01T00:00:00.300Z",time:2000000000,pid:888}
 ][] | {index:{}}, {tracer_id:"live-task",tracer_type:"profiling",hostname:"host-a",region:"east",
 started_timestamp:.observed,uploaded_timestamp:.uploaded,
 profile_data:{profile_type:"memory:physical_usage:bytes:space:bytes",profile:{time_nanos:.time,
 string_table:["","physical_usage","bytes","pid"],sample_type:[{type:1,unit:2}],
 sample:(if .pid == null then [] else [{value:[8192],label:[{key:3,num:.pid}]}] end)}}}
' >> "${HUATUO_BAMAI_TEST_TMPDIR}/labels.ndjson"
storage_request "${HUATUO_BAMAI_TEST_TMPDIR}/seed-labels.json" \
	-X POST "${STORAGE_ADDR}/${PROFILE_LABEL_INDEX}/_bulk?refresh=true" \
	-H 'Content-Type: application/x-ndjson' --data-binary "@${HUATUO_BAMAI_TEST_TMPDIR}/labels.ndjson"
report_http_response "seed labels" "${HUATUO_BAMAI_TEST_TMPDIR}/seed-labels.json" "${HUATUO_BAMAI_TEST_TMPDIR}/seed-labels.json.err"
assert_eq "$(jq -r '.errors' "${HUATUO_BAMAI_TEST_TMPDIR}/seed-labels.json")" false "seed label documents" \
	|| fatal "label fixture writes failed"

APISERVER_PORT="$PROFILE_LABEL_PORT" APISERVER_ADDR="$PROFILE_LABEL_ADDR" API_TOKEN="$PROFILE_LABEL_TOKEN" \
	write_config_apiserver_profile_query "$PROFILE_LABEL_INDEX" "$PROFILE_LABEL_READER" \
	> "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.conf"
APISERVER_ADDR="${PROFILE_LABEL_ADDR}" integration_huatuo_apiserver_start :

assert_profile_labels LabelNames '{}' "$PROFILE_LABEL_DOCUMENT_NAMES"
assert_eq "$(wc -c < "${HUATUO_BAMAI_TEST_TMPDIR}/labels-LabelNames.request.proto")" 0 "empty LabelNames request" \
	|| fatal "LabelNames default request must use a zero-byte Protobuf body"
profile_label_selector="{__profile_type__=\"${PROFILE_LABEL_TYPE}\",region=\"east\",hostname=\"host-a\",scope=\"host\",id=\"sample-task-a\"}"
profile_label_names=$(jq -nc --argjson names "$PROFILE_LABEL_DOCUMENT_NAMES" '$names + ["pid","comm","cpu","future_label","duplicate"] | sort')
assert_profile_labels LabelNames \
	"$(jq -nc --arg s "$profile_label_selector" '{matchers:[$s]}')" "$profile_label_names"
for profile_label_pair in 'pid|["123","456"]' 'cpu|["0","1"]' 'comm|["all","worker"]' \
	'future_label|["a+b","other"]' 'unknown_label|[]'; do
	profile_label_name=${profile_label_pair%%|*}
	assert_profile_labels LabelValues \
		"$(jq -nc --arg s "$profile_label_selector" --arg name "$profile_label_name" '{name:$name,matchers:[$s]}')" \
		"${profile_label_pair#*|}"
done
assert_profile_labels LabelValues \
	"$(jq -nc --arg s "${profile_label_selector%\}},comm=\"worker\"}" '{name:"pid",matchers:[$s]}')" '["123"]'
assert_profile_labels LabelValues \
	"$(jq -nc --arg s "${profile_label_selector%\}},comm=\"worker\",pid=\"123\"}" '{name:"cpu",matchers:[$s]}')" '["0"]'
assert_profile_labels LabelNames \
	"$(jq -nc --arg s "${profile_label_selector%\}},comm=\"all\"}" '{matchers:[$s]}')" \
	"$(jq -nc --argjson names "$PROFILE_LABEL_DOCUMENT_NAMES" '$names + ["pid","comm","cpu","future_label"] | sort')"

# The Grafana panel context must select the same task and exact-value filters.
profile_label_dashboard_selector=$(jq -r --arg type "$PROFILE_LABEL_TYPE" '
 .panels[] | select(.id == 5) | .targets[0].data | fromjson | .[0]
 | with_entries(.value |= (
   if . == "${type:percentencode}" then $type
   elif . == "${region:percentencode}" then "east"
   elif . == "${hostname:percentencode}" then "host-a"
   elif . == "${scope:text}" then "host"
   elif . == "${task_id:percentencode}" then "sample-task-a"
   else "*" end)) | del(.filters) | .__profile_type__ = .profile_type | del(.profile_type)
 | "{" + (to_entries | map(.key + "=" + (.value | tojson)) | join(",")) + "}"
' "${ROOT_DIR}/build/docker/grafana/dashboards/continuous-profiling.json")
assert_profile_labels LabelValues \
	"$(jq -nc --arg s "$profile_label_dashboard_selector" '{name:"pid",matchers:[$s]}')" '["123","456"]'
assert_profile_labels LabelValues \
	"$(jq -nc --arg s "${profile_label_dashboard_selector%\}},comm=\"worker\"}" '{name:"pid",matchers:[$s]}')" '["123"]'

# Endpoints use inclusive Unix-millisecond bounds, not an unbounded default.
assert_profile_labels LabelValues \
	"$(jq -nc --arg s "$profile_label_selector" '{name:"pid",matchers:[$s],start:"1",end:"1000"}')" '[]'
assert_profile_labels LabelNames \
	"$(jq -nc --arg s "$profile_label_selector" '{matchers:[$s],start:"1",end:"1000"}')" \
	"$(jq -nc --argjson names "$PROFILE_LABEL_DOCUMENT_NAMES" '$names | sort')"
assert_profile_labels LabelValues \
	"$(jq -nc --arg s "$profile_label_selector" '{name:"pid",matchers:[$s],start:"2000",end:"2000"}')" '["906"]'
assert_profile_labels LabelNames \
	"$(jq -nc --arg s "$profile_label_selector" '{matchers:[$s],start:"2000",end:"2000"}')" \
	"$(jq -nc --argjson names "$PROFILE_LABEL_DOCUMENT_NAMES" '$names + ["pid","later_window"] | sort')"
for profile_label_pair in 'container-a|901' 'container-b|902'; do
	profile_label_container=${profile_label_pair%%|*}
	assert_profile_labels LabelValues \
		"$(jq -nc --arg type "$PROFILE_LABEL_TYPE" --arg container "$profile_label_container" \
			'{name:"pid",matchers:["{__profile_type__="+($type|tojson)+",id=\"sample-task-a\",scope=\"container\",container_id="+($container|tojson)+"}"]}')" \
		"[\"${profile_label_pair#*|}\"]"
	assert_profile_labels LabelNames \
		"$(jq -nc --arg type "$PROFILE_LABEL_TYPE" --arg container "$profile_label_container" \
			'{matchers:["{__profile_type__="+($type|tojson)+",id=\"sample-task-a\",scope=\"container\",container_id="+($container|tojson)+"}"]}')" \
		"$(jq -nc --argjson names "$PROFILE_LABEL_DOCUMENT_NAMES" --arg key "${profile_label_container//-/_}" '$names + ["pid",$key] | sort')"
done

profile_label_offcpu_selector='{__profile_type__="process_offcpu:offcpu:nanoseconds:offcpu:nanoseconds",scope="host",id="offcpu-task"}'
assert_profile_labels LabelNames \
	"$(jq -nc --arg s "$profile_label_offcpu_selector" '{matchers:[$s]}')" \
	"$(jq -nc --argjson names "$PROFILE_LABEL_DOCUMENT_NAMES" '$names + ["comm","cpu","offcpu_reason","pid"] | sort')"
for profile_label_pair in 'pid|["123"]' 'comm|["worker"]' 'cpu|["0","3"]'; do
	profile_label_name=${profile_label_pair%%|*}
	assert_profile_labels LabelValues \
		"$(jq -nc --arg s "$profile_label_offcpu_selector" --arg name "$profile_label_name" '{name:$name,matchers:[$s]}')" \
		"${profile_label_pair#*|}"
done
assert_profile_labels LabelValues \
	"$(jq -nc --arg s "$profile_label_offcpu_selector" '{name:"offcpu_reason",matchers:[$s]}')" '["blocked","runqueue"]'
assert_profile_labels LabelValues \
	"$(jq -nc --arg s "${profile_label_offcpu_selector%\}},offcpu_reason=\"blocked\"}" \
		'{name:"offcpu_reason",matchers:[$s]}')" '["blocked"]'
for profile_label_pair in 'blocked|7' 'runqueue|11'; do
	profile_label_reason=${profile_label_pair%%|*}
	profile_label_result="${HUATUO_BAMAI_TEST_TMPDIR}/offcpu-${profile_label_reason}.json"
	assert_profile_query "$PROFILE_LABEL_ADDR" "$PROFILE_LABEL_TOKEN" SelectMergeStacktraces \
		"$(jq -nc --arg reason "$profile_label_reason" \
			'{profileTypeID:"process_offcpu:offcpu:nanoseconds:offcpu:nanoseconds",
			labelSelector:("{scope=\"host\",id=\"offcpu-task\",offcpu_reason="+($reason|tojson)+"}")}')" \
		"$profile_label_result"
	assert_eq "$(jq -r '.flamegraph.total' "$profile_label_result")" "${profile_label_pair#*|}" \
		"off-CPU reason nanoseconds" || fatal "incorrect weight for offcpu_reason=$profile_label_reason"
done

profile_label_result="${HUATUO_BAMAI_TEST_TMPDIR}/offcpu-combined.json"
assert_profile_query "$PROFILE_LABEL_ADDR" "$PROFILE_LABEL_TOKEN" SelectMergeStacktraces \
	"$(jq -nc '{profileTypeID:"process_offcpu:offcpu:nanoseconds:offcpu:nanoseconds",
	  labelSelector:"{scope=\"host\",id=\"offcpu-task\",pid=\"123\",comm=\"worker\",cpu=~\"0|1\",offcpu_reason=\"blocked\"}"}')" \
	"$profile_label_result"
assert_eq "$(jq -r '.flamegraph.total' "$profile_label_result")" 7 "off-CPU combined labels" \
	|| fatal "off-CPU combined labels selected the wrong samples"

profile_label_metadata_selector="{__profile_type__=\"${PROFILE_LABEL_TYPE}\",id=\"metadata-task\"}"
for profile_label_pair in "profile_type|[\"${PROFILE_LABEL_TYPE}\"]" 'tracer_id|["metadata-task"]' \
	'container_name|["worker"]' 'container_hostname|["pod-a"]' 'container_namespace|["production"]' \
	'container_type|["normal"]' 'container_qos|["burstable"]' 'tracer_name|["profiling"]' 'scope|["host","container"]'; do
	profile_label_name=${profile_label_pair%%|*}
	assert_profile_labels LabelValues \
		"$(jq -nc --arg s "$profile_label_metadata_selector" --arg name "$profile_label_name" '{name:$name,matchers:[$s]}')" \
		"${profile_label_pair#*|}"
done
profile_label_live_selector='{__profile_type__="memory:physical_usage:bytes:space:bytes",id="live-task"}'
assert_profile_labels LabelValues \
	"$(jq -nc --arg s "$profile_label_live_selector" '{name:"pid",matchers:[$s],end:"150"}')" '["777"]'
assert_profile_labels LabelValues \
	"$(jq -nc --arg s "$profile_label_live_selector" '{name:"pid",matchers:[$s],end:"1000"}')" '[]'
assert_profile_labels LabelNames \
	"$(jq -nc --arg s "$profile_label_live_selector" '{matchers:[$s],end:"1000"}')" \
	"$(jq -nc --argjson names "$PROFILE_LABEL_DOCUMENT_NAMES" '$names | sort')"
assert_profile_labels LabelValues \
	"$(jq -nc --arg s "${profile_label_live_selector%\}},pid=\"777\"}" '{name:"pid",matchers:[$s],end:"1000"}')" '[]'

printf '\377' > "${HUATUO_BAMAI_TEST_TMPDIR}/invalid.proto"
for profile_label_method in LabelNames LabelValues; do
	profile_label_request=$(jq -nc --arg method "$profile_label_method" --arg s "$profile_label_selector" \
		'{matchers:[$s]} + (if $method == "LabelValues" then {name:"pid"} else {} end)')
	for profile_label_filter in \
		'.start="2" | .end="1"' \
		'.matchers += ["{}"]' \
		'.matchers=["{"]' \
		'.matchers=["{__profile_type__=\"memory:physical_usage:bytes:space:bytes\"}"] | .end="1000"'; do
		assert_profile_label_error "$profile_label_method" \
			"$(jq -c "$profile_label_filter" <<< "$profile_label_request")"
	done
	for profile_label_conflict in 'id="other-task"' 'container_id="container-a"'; do
		assert_profile_label_error "$profile_label_method" \
			"$(jq -c --arg s "${profile_label_selector%\}},${profile_label_conflict}}" '.matchers=[$s]' <<< "$profile_label_request")"
	done
	for profile_label_auth in '401|unauthenticated|' "403|permission_denied|${PROFILE_LABEL_READER}"; do
		IFS='|' read -r profile_label_status profile_label_code profile_label_token <<< "$profile_label_auth"
		profile_label_result="${HUATUO_BAMAI_TEST_TMPDIR}/${profile_label_code}-${profile_label_method}.json"
		assert_profile_query "$PROFILE_LABEL_ADDR" "$profile_label_token" "$profile_label_method" "$profile_label_request" \
			"$profile_label_result" "$profile_label_status"
		assert_eq "$(jq -r '.error.code' "$profile_label_result")" \
			"$profile_label_code" "$profile_label_method authentication error" \
			|| fatal "$profile_label_method returned an unexpected authentication error; inspect $profile_label_result"
	done
	assert_profile_query_response "$PROFILE_LABEL_ADDR" "$PROFILE_LABEL_TOKEN" "$profile_label_method" \
		"${HUATUO_BAMAI_TEST_TMPDIR}/invalid.proto" "${HUATUO_BAMAI_TEST_TMPDIR}/malformed-${profile_label_method}.json" 400
	assert_eq "$(jq -r '.error.code' "${HUATUO_BAMAI_TEST_TMPDIR}/malformed-${profile_label_method}.json")" \
		invalid_request "$profile_label_method malformed Protobuf" || fatal "$profile_label_method accepted malformed Protobuf"
done
for profile_label_request in '{}' '{"name":"pid"}' '{"name":"pid","matchers":["{}"]}'; do
	assert_profile_label_error LabelValues "$profile_label_request"
done
assert_profile_label_error LabelValues \
	"$(jq -nc --arg s "$profile_label_selector" '{name:"__future__",matchers:[$s]}')"
assert_profile_label_error LabelValues \
	"$(jq -nc --arg s "${profile_label_selector%\}},pid=\"123\"}" '{name:"hostname",matchers:[$s]}')"
assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.log" "huatuo-apiserver"
