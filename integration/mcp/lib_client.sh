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

# Calls run in the current shell so session, protocol and request IDs persist.
MCP_SEQUENCE=0
MCP_SESSION=""
MCP_PROTOCOL="2025-03-26"
MCP_RESPONSE=""
MCP_PAYLOAD=""

mcp_args() {
	jq -cn --argjson base "$1" --argjson overrides "$2" '$base + $overrides'
}

assert_mcp_json() {
	local file=$1 predicate=$2 message=$3
	if ! jq -e "${predicate}" "${file}" > /dev/null; then
		cat "${file}" >&2
		fatal "${message}; response: ${file}"
	fi
}

mcp_header() {
	local file=$1 name=$2
	awk -v name="${name}" '
		{ sub(/\r$/, "") }
		index($0, ":") && tolower(substr($0, 1, index($0, ":") - 1)) == tolower(name) {
			value = substr($0, index($0, ":") + 1)
			sub(/^[ \t]+/, "", value)
		}
		END { print value }
	' "${file}"
}

mcp_rpc() {
	local method=$1 params=$2 notification=${3:-false}
	MCP_SEQUENCE=$((MCP_SEQUENCE + 1))
	local stem="${MCP_CLIENT_DIR}/mcp-$(printf '%03d' "${MCP_SEQUENCE}")-${method//\//-}"
	local status session content_type
	local -a headers=(-H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream'
		-H "Authorization: Bearer ${API_TOKEN}" -H "MCP-Protocol-Version: ${MCP_PROTOCOL}")
	[[ -z ${MCP_SESSION} ]] || headers+=(-H "Mcp-Session-Id: ${MCP_SESSION}")
	jq -n --arg method "${method}" --argjson params "${params}" \
		--argjson id "${MCP_SEQUENCE}" --argjson notification "${notification}" \
		'{jsonrpc:"2.0",method:$method,params:$params} + (if $notification then {} else {id:$id} end)' \
		> "${stem}.request.json"
	status=$(curl --noproxy '*' -sS --connect-timeout 2 --max-time 20 \
		"${headers[@]}" --data-binary "@${stem}.request.json" \
		-D "${stem}.headers" -o "${stem}.response.txt" -w '%{http_code}' \
		"${APISERVER_ADDR}/v1/mcp") || fatal "MCP transport failed: ${method}; artifacts: ${stem}"
	[[ ${status} == 2?? ]] || fatal "MCP HTTP ${status}: ${method}; artifacts: ${stem}"
	session=$(mcp_header "${stem}.headers" Mcp-Session-Id)
	[[ -z ${session} ]] || MCP_SESSION=${session}
	if [[ ${notification} == true ]]; then
		return 0
	fi
	content_type=$(mcp_header "${stem}.headers" Content-Type)
	MCP_RESPONSE="${stem}.response.json"
	if [[ ${content_type,,} == *text/event-stream* ]]; then
		# Join data lines within each SSE event, including CRLF and an EOF event.
		awk '
			function emit() { if (seen) print data; data=""; seen=0 }
			{ sub(/\r$/, "") }
			/^$/ { emit(); next }
			/^data:/ {
				value=substr($0, 6); sub(/^ /, "", value)
				data=data (seen ? "\n" : "") value; seen=1
			}
			END { emit() }
		' "${stem}.response.txt" > "${stem}.events.json"
		jq -s --argjson id "${MCP_SEQUENCE}" \
			'[.[] | select(.id == $id)] | if length == 1 then .[0] else error("expected one matching MCP reply") end' \
			"${stem}.events.json" > "${MCP_RESPONSE}" || fatal "invalid MCP SSE reply: ${stem}"
	else
		jq -s 'if length == 1 then .[0] else error("expected one JSON reply") end' \
			"${stem}.response.txt" > "${MCP_RESPONSE}" || fatal "invalid MCP JSON reply: ${stem}"
	fi
	assert_mcp_json "${MCP_RESPONSE}" \
		".jsonrpc == \"2.0\" and .id == ${MCP_SEQUENCE} and (has(\"error\") | not) and has(\"result\")" \
		"MCP JSON-RPC failure: ${method}"
}

mcp_call() {
	local name=$1 arguments=$2 expected_error=${3:-false}
	local params
	params=$(jq -n --arg name "${name}" --argjson args "${arguments}" '{name:$name,arguments:$args}')
	mcp_rpc tools/call "${params}"
	MCP_PAYLOAD="${MCP_RESPONSE%.response.json}.payload.json"
	jq -e '.result.content | map(select(.type == "text"))[0].text | fromjson' \
		"${MCP_RESPONSE}" > "${MCP_PAYLOAD}" || fatal "missing MCP Tool JSON content: ${name}"
	assert_mcp_json "${MCP_RESPONSE}" \
		"((.result.isError // false) == ${expected_error}) and
		 (.result | if has(\"structuredContent\") then
		 .structuredContent == (.content | map(select(.type == \"text\"))[0].text | fromjson) else true end)" \
		"MCP Tool error flag or structured/text content differs: ${name}"
	if [[ ${expected_error} == true ]]; then
		assert_mcp_json "${MCP_PAYLOAD}" '.status == "error"' "missing Tool error status: ${name}"
	elif [[ ${name} != diagnostic_guide ]]; then
		assert_mcp_json "${MCP_PAYLOAD}" '.status == "success" or .status == "partial"' "unexpected Tool status: ${name}"
	fi
}

mcp_client_start() {
	local expected_tools=$1 token status
	MCP_CLIENT_DIR=$2
	mkdir -p "${MCP_CLIENT_DIR}"
	MCP_SEQUENCE=0 MCP_SESSION="" MCP_PROTOCOL="2025-03-26"
	for token in '' invalid-token; do
		local -a headers=(-H 'Content-Type: application/json')
		[[ -z ${token} ]] || headers+=(-H "Authorization: Bearer ${token}")
		status=$(curl --noproxy '*' -sS --connect-timeout 2 --max-time 20 \
			"${headers[@]}" --data '{}' -o "${MCP_CLIENT_DIR}/auth-${token:-missing}.response.txt" \
			-w '%{http_code}' "${APISERVER_ADDR}/v1/mcp") || fatal "MCP authentication probe failed"
		assert_eq "${status}" 401 "MCP must reject missing or invalid credentials" || fatal "MCP authentication boundary failed"
	done
	mcp_rpc initialize '{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"huatuo-integration","version":"1"}}'
	MCP_PROTOCOL=$(jq -er '.result.protocolVersion | select(type == "string" and length > 0)' "${MCP_RESPONSE}") \
		|| fatal "MCP initialize omitted protocol version"
	mcp_rpc notifications/initialized '{}' true
	mcp_rpc tools/list '{}'
	assert_mcp_json "${MCP_RESPONSE}" \
		"([.result.tools[].name] | sort) == (${expected_tools} | sort)" "unexpected registered MCP Tools"
	mcp_call diagnostic_guide '{}'
	assert_mcp_json "${MCP_PAYLOAD}" '.stage == "overview" and (.resources | length > 0)' "missing embedded diagnostic guide"
}
