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

# Run with: bash integration/run.sh test_mcp.sh
# Subcases own their Docker backends; no Kubernetes or production services are used.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"

# Isolate the fake transport from the real backend scenarios below.
assert_mcp_client_protocol() (
	source "${ROOT_DIR}/integration/mcp/lib_client.sh"
	API_TOKEN=protocol-test APISERVER_ADDR=http://127.0.0.1
	local name fixture_type expected fixture_body outcome
	curl() {
		local headers response
		while (($#)); do
			case $1 in
			-D)
				headers=$2
				shift
				;;
			-o)
				response=$2
				shift
				;;
			esac
			shift
		done
		printf 'Content-Type: %s\r\n\r\n' "${fixture_type}" > "${headers}"
		printf '%b' "${fixture_body}" > "${response}"
		printf '200'
	}
	while IFS='|' read -r name fixture_type expected fixture_body; do
		MCP_CLIENT_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/protocol/${name}"
		mkdir -p "${MCP_CLIENT_DIR}"
		if (
			MCP_SEQUENCE=0
			mcp_rpc tools/list '{}'
		) > "${MCP_CLIENT_DIR}/check.log" 2>&1; then
			outcome=pass
		else
			outcome=fail
		fi
		assert_eq "${outcome}" "${expected}" "MCP protocol case ${name}" \
			|| fatal "MCP protocol parsing regression; see ${MCP_CLIENT_DIR}"
	done << 'EOF'
json|application/json|pass|{"jsonrpc":"2.0","id":1,"result":{}}
sse-multiline|text/event-stream|pass|:keepalive\r\ndata: {"jsonrpc":"2.0",\r\ndata: "id":1,"result":{}}\r\n\r\n
sse-eof|text/event-stream|pass|data: {"jsonrpc":"2.0","id":1,"result":{}}
wrong-id|application/json|fail|{"jsonrpc":"2.0","id":2,"result":{}}
rpc-error|application/json|fail|{"jsonrpc":"2.0","id":1,"error":{"code":-1}}
duplicate-id|text/event-stream|fail|data: {"jsonrpc":"2.0","id":1,"result":{}}\n\ndata: {"jsonrpc":"2.0","id":1,"result":{}}\n\n
missing-id|text/event-stream|fail|data: {"jsonrpc":"2.0","id":2,"result":{}}\n\n
malformed-json|text/event-stream|fail|data: {bad json}\n\n
EOF
	log_info "MCP protocol parsing assertions passed"
)

require_commands jq awk
assert_mcp_client_protocol
for suite in events metrics; do
	log_info "running MCP ${suite} integration"
	workspace="${HUATUO_BAMAI_TEST_TMPDIR}/${suite}"
	mkdir -p "${workspace}"
	HUATUO_BAMAI_TEST_TMPDIR="${workspace}" \
		bash "${ROOT_DIR}/integration/mcp/test_mcp_${suite}.sh"
done
