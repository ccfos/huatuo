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

# Verify that dropwatch selects the devlink program from tracefs support. The
# netdevsim test covers packet events; this test covers loading on both kernel
# capability paths.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

readonly TRACEPOINT_ID="/sys/kernel/tracing/events/devlink/devlink_trap_report/id"
readonly DEBUGFS_TRACEPOINT_ID="/sys/kernel/debug/tracing/events/devlink/devlink_trap_report/id"
readonly UNSUPPORTED_WARNING="devlink trap tracepoint unsupported; hardware drop tracing disabled"

tracepoint_available=false
if [[ -e "${TRACEPOINT_ID}" || -e "${DEBUGFS_TRACEPOINT_ID}" ]]; then
	tracepoint_available=true
fi

bpf_tool_setup dropwatch
"${TOOL_BIN}" \
	--bpf-path "${TOOL_BPF}" \
	--duration 1 \
	--output json \
	> "${TOOL_OUT}" 2> "${TOOL_ERR}"

assert_log_has_no_failure "${TOOL_ERR}" "dropwatch"
if [[ "${tracepoint_available}" == true ]]; then
	if grep -Fq "${UNSUPPORTED_WARNING}" "${TOOL_ERR}"; then
		fatal "dropwatch disabled hardware tracing despite devlink tracepoint support"
	fi
	log_info "dropwatch devlink program loaded and attached"
else
	grep -Fq "${UNSUPPORTED_WARNING}" "${TOOL_ERR}" \
		|| fatal "dropwatch did not report unsupported devlink tracepoint"
	log_info "dropwatch excluded unsupported devlink program and loaded software tracing"
fi
