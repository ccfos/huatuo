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
	mcp_client_start '["diagnostic_guide","discover_tracers","query_events","query_profiles"]' "${HUATUO_BAMAI_TEST_TMPDIR}/compatibility"
	local region base args profile_args expected
	local window='2026-01-15T10:00:00Z/2026-01-15T11:00:00Z'
	for region in alias-test plain-test source-test; do
		base=$(jq -cn --arg region "${region}" --arg window "${window}" '{region:[$region],time_range:$window}')
		mcp_call discover_tracers "${base}"
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
			mcp_call query_profiles "${profile_args}"
			assert_mcp_json "${MCP_PAYLOAD}" \
				'.summary.documents == 2 and .summary.total_samples == 200' "unindexed profile payload lost"
			mcp_call query_profiles "$(mcp_args "${profile_args}" '{"record_id":"0"}')"
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
		mcp_call discover_tracers "${base}"
		assert_mcp_json "${MCP_PAYLOAD}" \
			'(.tracers | length == 1) and .tracers[0].has_profiler == true' "${region}: source-only profile discovery lost"
		mcp_call query_profiles "$(mcp_args "${base}" '{"host":["node-a"],"tracer_name":"cpu-idle"}')"
		assert_mcp_json "${MCP_PAYLOAD}" \
			".summary.documents == ${expected} and .summary.total_samples == $((expected * 100))" "${region}: source/indexed profile merge differs"
		if [[ ${region} == millis-test ]]; then
			mcp_call query_events "$(mcp_args "${base}" '{"host":["node-a"],"tracer_name":["cpu-idle"]}')"
			assert_mcp_json "${MCP_PAYLOAD}" \
				'(.events | length == 2) and all(.events[]; .timestamp | startswith("2026-01-15T10:10:00.123"))' \
				"integer/string epoch_millis decoding differs"
		fi
	done
	mcp_call query_profiles "$(jq -cn --arg window "${window}" \
		'{region:["payload-overflow-test"],time_range:$window,tracer_name:"cpu-idle",host:["node-a"]}')" true
	assert_mcp_json "${MCP_PAYLOAD}" 'tostring | contains("500 candidate documents")' "profile fallback bypassed scan bound"
	log_info "MCP compatibility assertions passed"
}
