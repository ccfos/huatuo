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

# Verify that dropwatch detects and attaches the devlink raw tracepoint.
# Producing a hardware trap requires device-specific setup, so packet-content
# validation belongs in NIC-specific test lanes.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

tracepoint_id="/sys/kernel/tracing/events/devlink/devlink_trap_report/id"
debugfs_tracepoint_id="/sys/kernel/debug/tracing/events/devlink/devlink_trap_report/id"
if [[ ! -e "${tracepoint_id}" && ! -e "${debugfs_tracepoint_id}" ]]; then
	skip "devlink:devlink_trap_report is unavailable"
fi

bpf_tool_setup dropwatch
"${TOOL_BIN}" \
	--bpf-path "${TOOL_BPF}" \
	--duration 1 \
	--output json \
	> "${TOOL_OUT}" 2> "${TOOL_ERR}"

log_info "dropwatch devlink program loaded and attached"
