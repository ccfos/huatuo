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

assert_mcp_metrics() {
	mcp_client_start '["diagnostic_guide","discover_metrics","query_metrics"]' "${HUATUO_BAMAI_TEST_TMPDIR}"
	local metric=huatuo_mcp_test_value untyped=huatuo_mcp_untyped_value many=huatuo_mcp_many_value absent=huatuo_mcp_absent
	local sample="${HUATUO_BAMAI_TEST_TMPDIR}/prometheus-anchor.json" end start base args query
	# Anchor evaluation on an ingested sample instead of a guessed sleep interval.
	curl --noproxy '*' -fsS "${CURL_TIMEOUT[@]}" --get --data-urlencode "query=timestamp(${metric})" \
		"${MCP_BACKEND}/api/v1/query" > "${sample}" || fatal "cannot read Prometheus anchor sample"
	end=$(jq -er '.data.result[0].value[1] | tonumber | floor + 1' "${sample}") || fatal "missing Prometheus anchor sample"
	start=$(date -u -d "@$((end - 60))" '+%Y-%m-%dT%H:%M:%SZ')
	end=$(date -u -d "@${end}" '+%Y-%m-%dT%H:%M:%SZ')
	base=$(jq -cn --arg range "${start}/${end}" '{region:["test-a"],time_range:$range}')
	mcp_call discover_metrics "${base}"
	assert_mcp_json "${MCP_PAYLOAD}" \
		"([.metrics[].name] | sort) == ([\"${metric}\",\"${untyped}\",\"${many}\"] | sort) and
		 all(.metrics[]; .availability == \"unknown\")" "wrong candidate metric catalog or unverified presence"
	mcp_call discover_metrics "$(jq -cn --argjson base "${base}" --arg value "${metric}" --arg untyped "${untyped}" --arg absent "${absent}" \
		'$base + {metric_names:[$value,$untyped,$absent]}')"
	assert_mcp_json "${MCP_PAYLOAD}" \
		"(.metrics | map({key:.name,value:.}) | from_entries) |
		 .[\"${metric}\"].availability == \"present\" and
		 ((.[\"${metric}\"].labels | index(\"host\")) != null) and
		 ((.[\"${metric}\"].labels | index(\"region\")) != null) and
		 .[\"${metric}\"].metadata_status == \"available\" and .[\"${metric}\"].type == \"gauge\" and
		 .[\"${metric}\"].help == \"Fixed MCP integration gauge.\" and
		 .[\"${absent}\"].availability == \"absent\" and .[\"${untyped}\"].availability == \"present\" and
		 .[\"${untyped}\"].metadata_status != \"available\" and
		 ((.[\"${untyped}\"].help // \"\") == \"\") and ((.[\"${untyped}\"].unit // \"\") == \"\")" \
		"metric labels, presence or real metadata differs"
	args=$(jq -cn --argjson base "${base}" --arg query "${metric}{host=\"node-a\"}" \
		'$base + {query:$query,host:"node-a",step:"15s"}')
	mcp_call query_metrics "$(mcp_args "${args}" '{"region":["test-a","test-b"]}')"
	assert_mcp_json "${MCP_PAYLOAD}" \
		'.status == "success" and (.results | length == 2) and
		 ([.results[].region] | sort) == ["test-a","test-b"] and
		 all(.results[]; .region as $region | .response.data.result |
		 length == 1 and (.[0] | .metric.host == "node-a" and .metric.region == $region and
		 (.values | length > 0) and all(.values[]; .[1] == (if $region == "test-a" then "10" else "20" end))))' \
		"multi-region metric coverage, target isolation or values differ"
	mcp_call query_metrics "$(jq -cn --argjson args "${args}" --arg query "${absent}{host=\"node-a\"}" '$args + {query:$query}')"
	assert_mcp_json "${MCP_PAYLOAD}" \
		'.status == "success" and (.results[0].response.data.result | length == 0)' "empty metric query failed"
	mcp_call query_metrics "$(jq -cn --argjson args "${args}" --arg query "${many}{host=\"node-a\"}" '$args + {query:$query}')"
	assert_mcp_json "${MCP_PAYLOAD}" \
		'.status == "partial" and (.results[0].response.data.result | length == 2)' "series quota not enforced"
	for query in 'other_test_value{host="node-a"}' "${metric}{region=\"test-a\",host=\"node-a\"}"; do
		mcp_call query_metrics "$(jq -cn --argjson args "${args}" --arg query "${query}" '$args + {query:$query}')" true
		assert_mcp_json "${MCP_PAYLOAD}" 'tostring | contains("invalid")' "PromQL validation lost"
	done
	mcp_call discover_metrics "$(mcp_args "${base}" '{"metric_names":[]}')" true
	mcp_call query_metrics "$(mcp_args "${args}" '{"region":["failed"]}')" true
	assert_mcp_json "${MCP_PAYLOAD}" '(.errors | length > 0) and (tostring | contains("404"))' "backend failure hidden"
	mcp_call query_metrics "$(mcp_args "${args}" '{"region":["test-a","failed"]}')"
	assert_mcp_json "${MCP_PAYLOAD}" \
		'.status == "partial" and (.results | length == 1) and .results[0].region == "test-a" and .errors[0].region == "failed"' \
		"partial metric failure lost"
	log_info "MCP metric assertions passed"
}
