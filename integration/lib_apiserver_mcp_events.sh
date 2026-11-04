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

assert_mcp_events() {
	mcp_client_start '["diagnostic_guide","discover_event_types","query_events","query_event_profiles"]' "${HUATUO_BAMAI_TEST_TMPDIR}"
	local base='{"region":["test-a"],"time_range":"2026-01-15T10:00:00Z/2026-01-15T11:00:00Z"}'
	local expected='["clock-time","clock-legacy","clock-uploaded","clock-priority","clock-custom","clock-observed"]'
	local args target value profile_args tracer weight documents leaf_predicate
	mcp_call discover_event_types "${base}"
	assert_mcp_json "${MCP_PAYLOAD}" \
		'(.tracers | map({key:.tracer_name,value:.has_profiler}) | from_entries) |
		 .frame == true and .pprof == true and .clock == false and .["empty-profile"] == false' \
		"wrong profiler discovery"
	args=$(mcp_args "${base}" '{"tracer_name":["clock"],"host":["node-a"]}')
	mcp_call query_events "${args}"
	assert_mcp_json "${MCP_PAYLOAD}" \
		"[.events[].source.record_id] == ${expected} and
		 ([.events[].timestamp] == ([.events[].timestamp] | sort | reverse)) and
		 .events[0].timestamp == \"2026-01-15T10:30:00Z\" and
		 (.events[-1].observed_timestamp | startswith(\"2099-01-01T00:00:00\")) and
		 ([.events[] | select(.source.record_id == \"clock-priority\")][0].timestamp == \"2026-01-15T10:05:00Z\")" \
		"mixed mapping, clock precedence, ordering or metadata differs"
	for target in container_host container_hostnamespace; do
		value=container-a
		[[ ${target} != container_hostnamespace ]] || value=namespace-a
		mcp_call query_events "$(jq -cn --argjson base "${base}" --arg key "${target}" --arg value "${value}" \
			'$base + {tracer_name:["clock"],($key):[$value]}')"
		assert_mcp_json "${MCP_PAYLOAD}" "[.events[].source.record_id] == ${expected}" "${target} exact predicate failed"
	done
	mcp_call query_events "$(mcp_args "${args}" '{"time_range":"2026-01-15T18:00:00+08:00/2026-01-15T19:00:00+08:00"}')"
	assert_mcp_json "${MCP_PAYLOAD}" "[.events[].source.record_id] == ${expected}" "equivalent timezone query differs"
	mcp_call query_events "$(mcp_args "${args}" '{"time_range":"2026-01-15T10:58:00Z/2026-01-15T10:58:01Z"}')"
	assert_mcp_json "${MCP_PAYLOAD}" '[.events[].source.record_id] == ["clock-priority"]' "time predicates must OR supported clocks"
	mcp_call query_events "$(mcp_args "${args}" '{"host":["absent"]}')"
	assert_mcp_json "${MCP_PAYLOAD}" '.status == "success" and (.events | length == 0)' "empty query was not successful"
	mcp_call query_events "$(mcp_args "${args}" '{"size":2}')"
	assert_mcp_json "${MCP_PAYLOAD}" \
		".status == \"partial\" and .truncated == true and [.events[].source.record_id] == (${expected}[:2])" "missing event truncation"
	# Two tracers force Count and mixed-mapping aggregation before bounded searches.
	mcp_call query_events "$(mcp_args "${args}" '{"tracer_name":["clock","clock-extra"],"size":3}')"
	assert_mcp_json "${MCP_PAYLOAD}" \
		'(.events | length == 3) and any(.events[]; .source.record_id == "wrong-tracer")' "rare tracer lost in bounded query"
	mcp_call query_events '{"tracer_name":["clock"],"host":["node-a"]}' true
	assert_mcp_json "${MCP_PAYLOAD}" '.error_class == "invalid_argument"' "parameter errors lost"
	mcp_call query_events "$(mcp_args "${args}" '{"region":["failed"]}')" true
	assert_mcp_json "${MCP_PAYLOAD}" \
		'(.errors | length > 0) and (.errors[0].error | contains("no field capabilities")) and
		 .errors[0].region == "failed" and .errors[0].retryable == false' "backend cause lost"
	mcp_call query_events "$(mcp_args "${args}" '{"region":["test-a","failed"]}')"
	assert_mcp_json "${MCP_PAYLOAD}" \
		".status == \"partial\" and [.events[].source.record_id] == ${expected} and .errors[0].region == \"failed\"" \
		"partial region failure lost successful events"

	profile_args=$(mcp_args "${base}" '{"host":["node-a"],"top_n":4}')
	for tracer in frame pprof; do
		weight=100 documents=1
		[[ ${tracer} != pprof ]] || {
			weight=300
			documents=2
		}
		mcp_call query_event_profiles "$(jq -cn --argjson args "${profile_args}" --arg tracer "${tracer}" '$args + {tracer_name:$tracer}')"
		assert_mcp_json "${MCP_PAYLOAD}" \
			".summary.total_samples == ${weight} and .summary.documents == ${documents} and (.sources | length == ${documents})" \
			"${tracer} sample weight, count or locators differ"
		leaf_predicate="[.top_functions[] | select(.name == \"leaf\")] | length == 1 and
			(.[0] | .self_percent == 70 and .sample_count == $((weight * 7 / 10)) and
			 any(.call_paths[]; .path[:3] == [\"leaf\",\"worker\",\"root\"]))"
		assert_mcp_json "${MCP_PAYLOAD}" "${leaf_predicate}" "${tracer} leaf hotspot or call path differs"
		if [[ ${tracer} == pprof ]]; then
			assert_mcp_json "${MCP_PAYLOAD}" \
				'.summary.sample_type == "cpu" and .summary.sample_unit == "nanoseconds"' "wrong pprof sample type or unit"
		fi
	done
	args=$(mcp_args "${profile_args}" '{"tracer_name":"pprof","record_id":"pprof-proto"}')
	mcp_call query_event_profiles "${args}"
	assert_mcp_json "${MCP_PAYLOAD}" \
		'.summary.total_samples == 200 and .summary.documents == 1 and .sources[0].source.record_id == "pprof-proto"' \
		"record_id mixed profiles or returned a different locator"
	mcp_call query_event_profiles "$(mcp_args "${args}" '{"host":["absent"]}')" true
	mcp_call query_event_profiles "$(mcp_args "${args}" '{"record_id":"absent"}')" true
	for tracer in mixed-units broken; do
		value=incompatible
		[[ ${tracer} != broken ]] || value=supported
		mcp_call query_event_profiles "$(jq -cn --argjson args "${profile_args}" --arg tracer "${tracer}" '$args + {tracer_name:$tracer}')" true
		assert_mcp_json "${MCP_PAYLOAD}" "tostring | contains(\"${value}\")" "${tracer} failure reason lost"
	done
	mcp_call query_event_profiles "$(mcp_args "${profile_args}" '{"tracer_name":"partial-profile"}')"
	assert_mcp_json "${MCP_PAYLOAD}" \
		'.status == "partial" and .summary.unsupported_documents == 1 and .summary.total_samples == 100 and
		 any(.query_status[]; (.warnings // [] | length > 0))' "malformed profile or parsing warnings silently dropped"
	log_info "MCP event/Profile assertions passed"
}
