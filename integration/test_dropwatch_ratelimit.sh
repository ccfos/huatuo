#!/usr/bin/env bash

# Copyright 2026 The HuaTuo Authors.
#
# Authors:
# Tonghao Zhang <tonghao@bamaicloud.com>
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

# Verify that dropwatch's --max-events-per-second caps emission rate.
# Strategy: spam UDP packets at a closed port on a sentinel loopback IP,
# count the kfree_skb events dropwatch emits, and assert they stay within
# the rate-limit budget while at least one warning fires.

set -exuo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_resources.sh"

require_commands jq
bpf_tool_setup dropwatch net_dropwatch
readonly RATE=1
readonly DURATION=10
readonly TARGET_IP="127.0.0.99"
readonly TARGET_PORT=9999
readonly EXPECTED_MAX=$((RATE * (DURATION + 1))) # +1s headroom for the first burst
readonly DROPWATCH_RESOURCE_DIR="${TOOL_WORK_DIR}/resources"
readonly DROPWATCH_RESOURCE_SECONDS=5
DROPWATCH_PID=""
dropwatch_load_pid=""

cleanup() {
	resource_cleanup "${DROPWATCH_RESOURCE_DIR}"
	# GNU timeout owns this load's process group; never signal a completed job.
	if [[ -n ${dropwatch_load_pid} ]]; then
		local job_pid
		while read -r job_pid; do
			[[ ${job_pid} != "${dropwatch_load_pid}" ]] || kill -KILL -- "-${job_pid}" 2> /dev/null || true
		done < <(
			jobs -pr
			jobs -ps
		)
		wait "${dropwatch_load_pid}" 2> /dev/null || true
	fi
	[[ -n "${DROPWATCH_PID}" ]] && stop_by_pid "${DROPWATCH_PID}" 5 || true
}
trap cleanup EXIT

# No iptables rule is needed: sending UDP to a closed port on the loopback
# triggers SKB_DROP_REASON_NO_SOCKET inside __udp4_lib_rcv, which calls
# kfree_skb() — exactly the tracepoint dropwatch hooks. Avoiding iptables
# means zero host-state pollution and no cleanup trap.

log_info "dropwatch: rate=${RATE}/s, duration=${DURATION}s, target=${TARGET_IP}:${TARGET_PORT}"
resource_build || fatal "failed to build resource sampler"
"${TOOL_BIN}" --bpf-path "${TOOL_BPF}" \
	--filter "udp and port ${TARGET_PORT}" \
	--max-events-per-second "${RATE}" \
	--duration "${DURATION}" \
	--output json \
	> "${TOOL_OUT}" 2> "${TOOL_ERR}" &
DROPWATCH_PID=$!

# Let the BPF program attach before flooding.
sleep 0.5
resource_start "${DROPWATCH_PID}" "${DROPWATCH_RESOURCE_DIR}" || fatal "resource capture did not start"

# Use bash's built-in /dev/udp/HOST/PORT pseudo-device for sending UDP — no
# external dependency (nc/hping3/nping/socat versions vary across distros),
# no privilege beyond what we already need. A tight write-loop bounded by
# `timeout` reaches a few thousand pps in pure bash, which is >>10x the
# rate cap and saturates the limiter every window.
timeout "${DURATION}" bash -c "
	while :; do
		printf x >/dev/udp/${TARGET_IP}/${TARGET_PORT}
	done
" > /dev/null 2>&1 &
dropwatch_load_pid=$!
# Observe part of the flood, ending before dropwatch's own --duration expires.
# The complete DURATION-second flood and rate-limit assertions are unchanged.
sleep "${DROPWATCH_RESOURCE_SECONDS}"
resource_stop "${DROPWATCH_RESOURCE_DIR}" || fatal "resource capture is unusable"
jq -c '{cpu_seconds, cpu_percent, rss_bytes, fds, io}' "${DROPWATCH_RESOURCE_DIR}/result.json"
dropwatch_load_status=0
wait "${dropwatch_load_pid}" || dropwatch_load_status=$?
dropwatch_load_pid=""
((dropwatch_load_status == 124)) || fatal "UDP load did not complete its timeout window"
if ! wait "${DROPWATCH_PID}"; then
	DROPWATCH_PID=""
	fatal "dropwatch failed while tracing rate-limited drops"
fi
DROPWATCH_PID=""

events=$(jq -s '[.[] | select(.layers.label == "IPv4/UDP")] | length' "${TOOL_OUT}")
assert_kernel_observation_timestamps "${TOOL_OUT}"
jq -c -s '.[0] | {kernel_observed_timestamp, observed_timestamp, drop_source, drop_reason, layers}' "${TOOL_OUT}"
# Event lines are emitted on stdout; structured logs, including rate-limit
# warnings, are emitted on stderr.
warns=$(grep -h "rate limit hit" "${TOOL_OUT}" "${TOOL_ERR}" 2> /dev/null | wc -l || true)

log_info "events=${events} (cap=${EXPECTED_MAX}), rate-limit warnings=${warns}"

# Different kernels/distros vary in how aggressively they coalesce or
# drop UDP packets to a closed loopback port, so keep both assertions explicit.
((events <= EXPECTED_MAX)) || fatal "events ${events} exceed cap ${EXPECTED_MAX}"
((warns >= 1)) || fatal "expected at least one rate-limit warning under flood"
assert_log_has_no_failure "${TOOL_ERR}" "dropwatch"
