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

set -euo pipefail
source "${ROOT_DIR}/integration/mcp/lib_mcp.sh"
source "${ROOT_DIR}/integration/mcp/assert_events.sh"
source "${ROOT_DIR}/integration/mcp/assert_compatibility.sh"
readonly MCP_SUITE=events
readonly CURL_TIMEOUT=(--connect-timeout 2 --max-time 5)

mcp_setup
storage_start
MCP_BACKEND=${STORAGE_ADDR}
python3 "${MCP_TEST_DIR}/generate_fixtures.py" "${HUATUO_BAMAI_TEST_TMPDIR}"
mcp_load_fixtures escomparable
mcp_load_fixtures compatibility
integration_huatuo_apiserver_start write_mcp_apiserver_config
assert_mcp_events
assert_mcp_compatibility
mcp_assert_shutdown
