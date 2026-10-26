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

require_commands jq timeout unshare ip
# StartDefault uses a fixed endpoint; isolate it from host listeners and other tests.
if [[ ${1:-} != --isolated ]]; then
	exec unshare --net bash "$0" --isolated
fi
ip link set lo up
# The isolated namespace has no access to the host's HTTP proxy.
export no_proxy=127.0.0.1 NO_PROXY=127.0.0.1

bpf_tool_setup dropwatch net_dropwatch
# Keep the BPF event cap well below the UDP flood rate so the test exercises
# both event delivery and rate-limit warnings. Five events/s permits more
# userspace decoding and JSON output than 1/s during the 5-second CPU profile,
# while keeping this a low-rate limiter test rather than a throughput test
# at 100/s. This controls event emission, not packet generation or pprof's
# sampling frequency; events suppressed by the limiter are not perf-buffer loss.
# With the 10-second run and one extra window of headroom, the output bound
# below is 55 events, not an exact expected count. Five is a test workload
# choice, not a production recommendation or a measured perf-buffer capacity.
# CPU profiles can still contain zero samples at this rate; increasing event
# delivery does not guarantee a nonzero CPU sample count.
readonly DROPWATCH_RATE=5
readonly DROPWATCH_DURATION=10
readonly DROPWATCH_TARGET_IP="127.0.0.99"
readonly DROPWATCH_TARGET_PORT=9999
readonly DROPWATCH_EXPECTED_MAX=$((DROPWATCH_RATE * (DROPWATCH_DURATION + 1))) # +1s headroom for the first burst
readonly DROPWATCH_CPU_LIMIT_PERCENT=5                                         # One fully occupied core is 100%.
# Five Ubuntu 26.04 CI runs reported at most 70,330,782 bytes of live heap.
# Add 10% headroom and round up to MiB: ceil(70,330,782 * 1.10 / 2^20) = 74.
readonly DROPWATCH_HEAP_LIMIT_BYTES=$((74 * 1024 * 1024))
readonly DROPWATCH_GOROUTINE_LIMIT=16
readonly DROPWATCH_PPROF_ADDRESS="127.0.0.1:6000"
readonly DROPWATCH_PPROF_DIR="${TOOL_WORK_DIR}/pprof"
dropwatch_pid=""
dropwatch_load_pid=""

cleanup() {
	stop_and_wait_by_pid "${dropwatch_load_pid}" 1 || true
	stop_and_wait_by_pid "${dropwatch_pid}" 5 || true
}
trap cleanup EXIT

assert_dropwatch_ratelimit() {
	local events=$1 warns=$2
	# Kernels vary in how aggressively they coalesce or drop loopback UDP packets.
	((events > 0 && events <= DROPWATCH_EXPECTED_MAX)) \
		|| fatal "dropwatch events: actual=${events}, expected=1..${DROPWATCH_EXPECTED_MAX}"
	((warns >= 1)) \
		|| fatal "dropwatch rate-limit warnings: actual=${warns}, expected>=1"
}

assert_dropwatch_resource_budget() {
	local metric=$1 limit=$2 minimum=${3:-0} actual
	local result="${TOOL_WORK_DIR}/resources.json"
	actual=$(jq -c --arg metric "${metric}" '.[$metric]' "${result}") \
		|| fatal "cannot read dropwatch ${metric}: ${result}"
	jq -e --argjson limit "${limit}" --argjson minimum "${minimum}" \
		'type == "number" and . >= $minimum and . <= $limit' \
		<<< "${actual}" > /dev/null \
		|| fatal "dropwatch ${metric}: actual=${actual}, expected=${minimum}..${limit}; ${result}"
}

# No iptables rule is needed: sending UDP to a closed port on the loopback
# triggers SKB_DROP_REASON_NO_SOCKET inside __udp4_lib_rcv, which calls
# kfree_skb() — exactly the tracepoint dropwatch hooks. Avoiding iptables
# avoids changing firewall rules, so no iptables cleanup is needed.

log_info "dropwatch: rate=${DROPWATCH_RATE}/s, duration=${DROPWATCH_DURATION}s, target=${DROPWATCH_TARGET_IP}:${DROPWATCH_TARGET_PORT}"
"${TOOL_BIN}" --bpf-path "${TOOL_BPF}" \
	--filter "udp and port ${DROPWATCH_TARGET_PORT}" \
	--max-events-per-second "${DROPWATCH_RATE}" \
	--duration "${DROPWATCH_DURATION}" \
	--enable-pprof \
	--output json \
	> "${TOOL_OUT}" 2> "${TOOL_ERR}" &
dropwatch_pid=$!

# Use bash's built-in /dev/udp/HOST/PORT pseudo-device for sending UDP — no
# external dependency (nc/hping3/nping/socat versions vary across distros),
# no privilege beyond what we already need. A tight write-loop bounded by
# `timeout` reaches a few thousand pps in pure bash, which is >>10x the
# rate cap and saturates the limiter every window.
timeout "${DROPWATCH_DURATION}" bash -c "
	while :; do
		printf x >/dev/udp/${DROPWATCH_TARGET_IP}/${DROPWATCH_TARGET_PORT}
	done
" > /dev/null 2>&1 &
dropwatch_load_pid=$!

wait_until 4 0.1 test -s "${TOOL_OUT}" || fatal "dropwatch produced no events under load"
# Refresh the live-heap snapshot after CPU profiling so forced GC is not
# included in the CPU budget.
"${PPROF_TOOL_BIN}" capture --pid "${dropwatch_pid}" \
	--url "http://${DROPWATCH_PPROF_ADDRESS}/debug/pprof" \
	--endpoint "profile?seconds=5" --endpoint "heap?gc=1" --endpoint goroutine \
	--output-dir "${DROPWATCH_PPROF_DIR}" \
	|| fatal "dropwatch pprof capture failed"

dropwatch_load_status=0
wait "${dropwatch_load_pid}" || dropwatch_load_status=$?
dropwatch_load_pid=""
assert_eq "${dropwatch_load_status}" 124 "UDP load exit status" \
	|| fatal "UDP load failed: actual=${dropwatch_load_status}, expected=124 (timeout)"
dropwatch_status=0
wait "${dropwatch_pid}" || dropwatch_status=$?
dropwatch_pid=""
assert_eq "${dropwatch_status}" 0 "dropwatch exit status" \
	|| fatal "dropwatch failed: actual=${dropwatch_status}, expected=0"

dropwatch_events=$(jq -s '[.[] | select(.layers.label == "IPv4/UDP")] | length' "${TOOL_OUT}")
assert_kernel_observation_timestamps "${TOOL_OUT}"
jq -c -s '.[0] | {kernel_observed_timestamp, observed_timestamp, drop_source, drop_reason, layers}' "${TOOL_OUT}"
# Event lines are emitted on stdout; structured logs, including rate-limit
# warnings, are emitted on stderr.
dropwatch_warns=$(grep -h "rate limit hit" "${TOOL_OUT}" "${TOOL_ERR}" 2> /dev/null | wc -l || true)

log_info "events=${dropwatch_events} (cap=${DROPWATCH_EXPECTED_MAX}), rate-limit warnings=${dropwatch_warns}"

assert_dropwatch_ratelimit "${dropwatch_events}" "${dropwatch_warns}"

# Keep budgets in the feature test; analysis runs outside the load window.
"${PPROF_TOOL_BIN}" summary --input-dir "${DROPWATCH_PPROF_DIR}" \
	> "${TOOL_WORK_DIR}/resources.json" \
	|| fatal "dropwatch pprof analysis failed"
log_info "pprof estimates: $(< "${TOOL_WORK_DIR}/resources.json")"
assert_dropwatch_resource_budget cpu_percent "${DROPWATCH_CPU_LIMIT_PERCENT}"
assert_dropwatch_resource_budget heap_inuse_bytes "${DROPWATCH_HEAP_LIMIT_BYTES}"
assert_dropwatch_resource_budget goroutines "${DROPWATCH_GOROUTINE_LIMIT}" 1
assert_log_has_no_failure "${TOOL_ERR}" "dropwatch"
