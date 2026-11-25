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

# Verify irqtracing's completeness contract on a real kernel: under a softirq
# flood on the target cpu the result must not pretend to be complete —
# dropped_samples.collection > 0 and profile present. The drop counter is read after detach;
# that contract is covered by TestDetachAndReadDroppedSamplesDetachBeforeReads.

set -exuo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_profiler.sh"

readonly IRQ_RATE_BIN="${ROOT_DIR}/_output/bin/irqtracing"
readonly IRQ_RATE_BPF="${ROOT_DIR}/_output/bpf/irqtracing.o"
readonly IRQ_RATE_OUTPUT_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/irqtracing"
readonly IRQ_RATE_TARGET_IP="127.0.0.99"
readonly IRQ_RATE_TARGET_PORT=9999
readonly IRQ_RATE_DURATION=3
readonly IRQ_RATE_MAX_EVENTS_PER_SECOND_PER_CPU=2
readonly IRQ_RATE_FLOOD_READY="${IRQ_RATE_OUTPUT_DIR}/flood.ready"

require_commands jq taskset awk
tracepoint_available irq softirq_raise || skip "irq/softirq_raise is unavailable"
tracepoint_available irq softirq_entry || skip "irq/softirq_entry is unavailable"

mkdir -p "${IRQ_RATE_OUTPUT_DIR}"

mapfile -t irq_rate_cpu_ids < <(profiler_allowed_cpu_ids)
[[ ${#irq_rate_cpu_ids[@]} -ge 1 ]] || fatal "no allowed cpu in /proc/self/status"
# Prefer a non-zero cpu because cpu 0 often runs container/host housekeeping.
if [[ ${#irq_rate_cpu_ids[@]} -ge 2 ]]; then
	readonly IRQ_RATE_CPU_ID=${irq_rate_cpu_ids[1]}
else
	readonly IRQ_RATE_CPU_ID=${irq_rate_cpu_ids[0]}
fi

irq_rate_flood_pid=""
irq_rate_tool_pid=""

cleanup() {
	if [[ -n ${irq_rate_flood_pid} ]]; then
		stop_by_pid "${irq_rate_flood_pid}" 2 || true
		wait "${irq_rate_flood_pid}" 2> /dev/null || true
		irq_rate_flood_pid=""
	fi
	if [[ -n ${irq_rate_tool_pid} ]]; then
		stop_by_pid "${irq_rate_tool_pid}" 2 || true
		wait "${irq_rate_tool_pid}" 2> /dev/null || true
		irq_rate_tool_pid=""
	fi
}
trap cleanup EXIT

# irq_rate_flood_udp <cpu>: spam UDP packets at a closed loopback port pinned to <cpu>.
# The ready file is written only after the first packet has been sent.
irq_rate_flood_udp() {
	local cpu=$1
	exec taskset -c "${cpu}" bash -c '
		target_ip=$1
		target_port=$2
		ready=$3
		printf x > "/dev/udp/${target_ip}/${target_port}"
		printf ready > "${ready}"
		while :; do
			printf x > "/dev/udp/${target_ip}/${target_port}"
		done
	' _ "${IRQ_RATE_TARGET_IP}" "${IRQ_RATE_TARGET_PORT}" "${IRQ_RATE_FLOOD_READY}" > /dev/null 2>&1
}

irq_rate_flood_is_ready() {
	[[ -s "${IRQ_RATE_FLOOD_READY}" ]] && kill -0 "${irq_rate_flood_pid}" 2> /dev/null
}

irq_rate_tool_has_exited() {
	! kill -0 "${irq_rate_tool_pid}" 2> /dev/null
}

readonly IRQ_RATE_TOOL_DEADLINE=$((IRQ_RATE_DURATION + 30))
log_info "irqtracing: flood cpu ${IRQ_RATE_CPU_ID}, expect collection loss and a profile"

rm -f "${IRQ_RATE_FLOOD_READY}"
irq_rate_flood_udp "${IRQ_RATE_CPU_ID}" &
irq_rate_flood_pid=$!
wait_until 5 0.1 irq_rate_flood_is_ready \
	|| fatal "flood failed to start on cpu ${IRQ_RATE_CPU_ID}"

"${IRQ_RATE_BIN}" \
	--bpf-path "${IRQ_RATE_BPF}" \
	--cpu-list "${IRQ_RATE_CPU_ID}" \
	--duration "${IRQ_RATE_DURATION}" \
	--max-events-per-second-per-cpu "${IRQ_RATE_MAX_EVENTS_PER_SECOND_PER_CPU}" \
	--output-format json --output-path "${IRQ_RATE_OUTPUT_DIR}/result.json" \
	2> "${IRQ_RATE_OUTPUT_DIR}/err.txt" &
irq_rate_tool_pid=$!

wait_until "${IRQ_RATE_TOOL_DEADLINE}" 1 irq_rate_tool_has_exited \
	|| fatal "irqtracing did not exit within ${IRQ_RATE_TOOL_DEADLINE}s (stderr: $(tail -1 "${IRQ_RATE_OUTPUT_DIR}/err.txt"))"
if ! wait "${irq_rate_tool_pid}"; then
	fatal "irqtracing failed on cpu ${IRQ_RATE_CPU_ID} (stderr: $(tail -1 "${IRQ_RATE_OUTPUT_DIR}/err.txt"))"
fi
kill -0 "${irq_rate_flood_pid}" 2> /dev/null \
	|| fatal "flood exited before irqtracing completed"
kill "${irq_rate_flood_pid}" 2> /dev/null || true
wait "${irq_rate_flood_pid}" || true
irq_rate_flood_pid=""
irq_rate_tool_pid=""

assert_irqtracing_dropped_samples() {
	local result_file=$1 stderr_file=$2
	if ! jq -e '
        def sample_label($profile; $name):
            [.label[]? | select($profile.string_table[.key] == $name) | $profile.string_table[.str]][0];
        .profile as $p |
        .profile_type == "irqtracing:irq:count:irq:count" and
        .dropped_samples.collection > 0 and
        ($p.sample | length) > 0 and $p.duration_nanos > 0 and
        all($p.sample[];
            sample_label($p; "irq_context") != "victim" or
            (sample_label($p; "comm") | startswith("ksoftirqd") | not))
    ' "${result_file}" > /dev/null; then
		log_error "expected an IRQ profile with collection loss and no ksoftirqd victim: ${result_file}"
		return 1
	fi
	if ! grep -q 'profiling samples dropped; profile is incomplete' "${stderr_file}"; then
		log_error "missing dropped-sample warning: ${stderr_file}"
		return 1
	fi
	return 0
}
assert_irqtracing_dropped_samples "${IRQ_RATE_OUTPUT_DIR}/result.json" "${IRQ_RATE_OUTPUT_DIR}/err.txt" \
	|| fatal "IRQ loss assertions failed; inspect ${IRQ_RATE_OUTPUT_DIR}"
log_info "PASS"
