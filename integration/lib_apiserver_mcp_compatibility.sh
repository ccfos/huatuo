#!/usr/bin/env bash

# Copyright 2026 The HuaTuo Authors.
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#     http://www.apache.org/licenses/LICENSE-2.0
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

assert_mcp_compatibility() {
	mcp_client_start '["diagnostic_guide","discover_event_types","query_events","query_event_profiles"]' "${HUATUO_BAMAI_TEST_TMPDIR}/compatibility"
	local region base args profile_args expected
	local window='2026-01-15T10:00:00Z/2026-01-15T11:00:00Z'
	base=$(jq -cn --arg window "${window}" '{scope:"region",region:["aggregate-test"],time_range:$window,group_by:["hostname","tracer_name"]}')
	mcp_call query_events "${base}"
	assert_mcp_json "${MCP_PAYLOAD}" \
		'.status == "success" and .scope == "region" and .purpose == "statistics" and (.usage_hint | contains("not a targeted fault diagnosis")) and .order == "key_asc_per_region" and (.buckets | length == 3) and
		 ([.buckets[].count] | add) == 3 and all(.buckets[]; .region == "aggregate-test" and
		 .first_seen == "2026-01-15T10:10:00Z" and .last_seen == .first_seen) and
		 all(.buckets[]; .key.tracer_name != "hidden-tracer") and .query_status[0].count_exact' \
		"cross-target aggregation lost exact counts, clock, or alias filter"
	args=$(jq -ce '
		(.start_time+"/"+.end_time) as $range |
		.buckets | map(select(.key.hostname != null and .key.tracer_name != null)) | (.[0] // error("no target candidate")) |
		{scope:"target",region:[.region],time_range:$range,host:[.key.hostname],tracer_name:[.key.tracer_name],size:20}' \
		"${MCP_PAYLOAD}") || fatal "no target candidate in aggregate result"
	mcp_call query_events "${args}"
	assert_mcp_json "${MCP_PAYLOAD}" \
		'.status == "success" and (.events | length == 1) and .events[0].hostname == "node-a" and
		 .events[0].tracer_name == "cpu-idle" and .events[0].source.record_id == "0" and (has("buckets") | not)' \
		"regional event candidate did not lead to isolated target context"
	mcp_call query_events "$(mcp_args "${base}" '{"size":1}')"
	assert_mcp_json "${MCP_PAYLOAD}" '.status == "partial" and .truncated and (.buckets | length == 1) and .query_status[0].count_exact' "aggregate lookahead lost"
	mcp_call query_events "$(mcp_args "${base}" '{"host":["node-b"]}')"
	assert_mcp_json "${MCP_PAYLOAD}" '(.buckets | length == 1) and .buckets[0].key.hostname == "node-b" and .buckets[0].count == 1' "aggregate target filter lost"
	mcp_call query_events "$(mcp_args "${base}" '{"tracer_name":["absent"]}')"
	assert_mcp_json "${MCP_PAYLOAD}" '.status == "success" and (.buckets | length == 0)' "empty aggregate became failure"
	mcp_call query_events "$(mcp_args "${base}" '{"region":["source-test"]}')" true
	assert_mcp_json "${MCP_PAYLOAD}" '.errors[0].retryable == false' "aggregate must not scan unindexed source"
	mcp_call query_events "$(mcp_args "${base}" '{"region":["alias-test"]}')" true
	assert_mcp_json "${MCP_PAYLOAD}" 'tostring | contains("one complete keyword projection")' "mixed grouping must not silently drop mappings"
	mcp_call query_events "$(mcp_args "${base}" '{"region":["aggregate-test","source-test"]}')" true
	assert_mcp_json "${MCP_PAYLOAD}" '.error_class == "invalid_argument"' "aggregate must reject multiple regions"
	mcp_call query_events "$(mcp_args "${base}" '{"size":1000,"group_by":["hostname","container_hostname","container_host_namespace","tracer_name"]}')"
	assert_mcp_json "${MCP_PAYLOAD}" '.status == "success" and (.buckets | length == 3) and (.group_by | length == 4)' "aggregate must allow 1000 buckets and every distinct whitelist field"
	mcp_call query_events "$(mcp_args "${base}" '{"time_range":"2026-01-12T00:00:00Z/2026-01-19T00:00:00Z"}')"
	assert_mcp_json "${MCP_PAYLOAD}" '.status == "success" and (.buckets | length == 3)' "aggregate must allow a seven-day window"
	mcp_call query_events "$(mcp_args "${base}" '{"time_range":"2026-01-12T00:00:00Z/2026-01-20T00:00:00Z"}')" true
	assert_mcp_json "${MCP_PAYLOAD}" '.error_class == "invalid_argument"' "aggregate must reject windows over seven days"
	mcp_call query_events "$(jq -cn --argjson base "${base}" '$base | del(.scope)')" true
	assert_mcp_json "${MCP_PAYLOAD}" '.error_class == "invalid_argument"' "missing scope must not enable cross-target aggregation"
	for region in alias-test plain-test source-test; do
		base=$(jq -cn --arg region "${region}" --arg window "${window}" '{region:[$region],time_range:$window}')
		mcp_call discover_event_types "${base}"
		assert_mcp_json "${MCP_PAYLOAD}" \
			'([.tracers[].tracer_name] | unique) == ["cpu-idle"]' "${region}: alias/source tracer catalog differs"
		args=$(mcp_args "${base}" '{"tracer_name":["cpu-idle"],"host":["node-a"],"container_host":["pod-a"],"container_hostnamespace":["ns-a"]}')
		mcp_call query_events "${args}"
		assert_mcp_json "${MCP_PAYLOAD}" '.status == "success" and (.events | length == 2)' "${region}: exact compatibility query differs"
		if [[ ${region} != alias-test ]]; then
			assert_mcp_json "${MCP_PAYLOAD}" \
				'.query_status[0].count_exact == true and .query_status[0].total_count == 2' "${region}: source count differs"
		fi
		mcp_call query_events "$(mcp_args "${args}" '{"host":["node"]}')"
		assert_mcp_json "${MCP_PAYLOAD}" '.status == "success" and (.events | length == 0)' "${region}: text token matched a whole hostname"
		if [[ ${region} == source-test ]]; then
			profile_args=$(mcp_args "${args}" '{"tracer_name":"cpu-idle","top_n":3}')
			mcp_call query_event_profiles "${profile_args}"
			assert_mcp_json "${MCP_PAYLOAD}" \
				'.summary.documents == 2 and .summary.total_samples == 200' "unindexed profile payload lost"
			mcp_call query_event_profiles "$(mcp_args "${profile_args}" '{"record_id":"0"}')"
			assert_mcp_json "${MCP_PAYLOAD}" '.summary.documents == 1' "source compatibility lost record_id predicate"
		fi
	done
	mcp_call query_events "$(jq -cn --arg window "${window}" \
		'{region:["overflow-test"],time_range:$window,tracer_name:["cpu-idle"],host:["node-a"]}')" true
	assert_mcp_json "${MCP_PAYLOAD}" 'tostring | contains("500 candidate documents")' "candidate overflow hidden or reported as no data"
	for region in millis-test payload-test mixed-payload-test scoped-payload-test; do
		expected=2
		[[ ${region} != mixed-payload-test ]] || expected=4
		base=$(jq -cn --arg region "${region}" --arg window "${window}" '{region:[$region],time_range:$window}')
		mcp_call discover_event_types "${base}"
		assert_mcp_json "${MCP_PAYLOAD}" \
			'(.tracers | length == 1) and .tracers[0].has_profiler == true' "${region}: source-only profile discovery lost"
		mcp_call query_event_profiles "$(mcp_args "${base}" '{"host":["node-a"],"tracer_name":"cpu-idle"}')"
		assert_mcp_json "${MCP_PAYLOAD}" \
			".summary.documents == ${expected} and .summary.total_samples == $((expected * 100))" "${region}: source/indexed profile merge differs"
		if [[ ${region} == millis-test ]]; then
			mcp_call query_events "$(mcp_args "${base}" '{"host":["node-a"],"tracer_name":["cpu-idle"]}')"
			assert_mcp_json "${MCP_PAYLOAD}" \
				'(.events | length == 2) and all(.events[]; .timestamp | startswith("2026-01-15T10:10:00.123"))' \
				"integer/string epoch_millis decoding differs"
		fi
	done
	mcp_call query_event_profiles "$(jq -cn --arg window "${window}" \
		'{region:["payload-overflow-test"],time_range:$window,tracer_name:"cpu-idle",host:["node-a"]}')" true
	assert_mcp_json "${MCP_PAYLOAD}" 'tostring | contains("500 candidate documents")' "profile fallback bypassed scan bound"
	log_info "MCP compatibility assertions passed"
}
