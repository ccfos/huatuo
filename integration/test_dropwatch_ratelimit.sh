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

bpf_tool_setup dropwatch
readonly RATE=1
readonly DURATION=10
readonly TARGET_IP="127.0.0.99"
readonly TARGET_PORT=9999
readonly EXPECTED_MAX=$((RATE * (DURATION + 1))) # +1s headroom for the first burst

# No iptables rule is needed: sending UDP to a closed port on the loopback
# triggers SKB_DROP_REASON_NO_SOCKET inside __udp4_lib_rcv, which calls
# kfree_skb() — exactly the tracepoint dropwatch hooks. Avoiding iptables
# means zero host-state pollution and no cleanup trap.

log_info "dropwatch: rate=${RATE}/s, duration=${DURATION}s, target=${TARGET_IP}:${TARGET_PORT}"
"${TOOL_BIN}" --bpf-path "${TOOL_BPF}" \
	--filter "udp and port ${TARGET_PORT}" \
	--max-events-per-second "${RATE}" \
	--duration "${DURATION}" \
	--output text \
	> "${TOOL_OUT}" 2> "${TOOL_ERR}" &
dw_pid=$!

# Let the BPF program attach before flooding.
sleep 0.5

# Use bash's built-in /dev/udp/HOST/PORT pseudo-device for sending UDP — no
# external dependency (nc/hping3/nping/socat versions vary across distros),
# no privilege beyond what we already need. A tight write-loop bounded by
# `timeout` reaches a few thousand pps in pure bash, which is >>10x the
# rate cap and saturates the limiter every window.
timeout "${DURATION}" bash -c "
	while :; do
		printf x >/dev/udp/${TARGET_IP}/${TARGET_PORT}
	done
" > /dev/null 2>&1 || true
wait "${dw_pid}" || true

events=$(grep -c "IPv4/UDP" "${TOOL_OUT}" || true)
# Both event lines and rate-limit warnings are emitted on stdout by
# huatuo-bamai/internal/log; ERR captures any unexpected stderr (panics,
# etc.) for post-mortem only.
warns=$(grep -c "rate limit hit" "${TOOL_OUT}" || true)

log_info "events=${events} (cap=${EXPECTED_MAX}), rate-limit warnings=${warns}"

# Different kernels/distros vary in how aggressively they coalesce or
# drop UDP packets to a closed loopback port, so on assertion failure
# dump the captured logs for cross-environment debugging.
((events <= EXPECTED_MAX)) || dump_tool_logs_and_fail "events ${events} exceed cap ${EXPECTED_MAX}"
((warns >= 1)) || dump_tool_logs_and_fail "expected at least one rate-limit warning under flood"
