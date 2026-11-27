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
readonly IRQ_RATE_PROFILER_BIN="${ROOT_DIR}/_output/bin/profiler"
readonly IRQ_RATE_OUTPUT_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/irqtracing"
readonly IRQ_RATE_TARGET_IP="127.0.0.99"
readonly IRQ_RATE_TARGET_PORT=9999
readonly IRQ_RATE_DURATION=3
readonly IRQ_RATE_MAX_EVENTS_PER_SECOND_PER_CPU=2
readonly IRQ_RATE_FLOOD_READY="${IRQ_RATE_OUTPUT_DIR}/flood.ready"

require_commands jq taskset awk script stty mkfifo timeout
tracepoint_available irq softirq_raise || skip "irq/softirq_raise is unavailable"
tracepoint_available irq softirq_entry || skip "irq/softirq_entry is unavailable"

mkdir -p "${IRQ_RATE_OUTPUT_DIR}"

# Invalid object input must fail without waiting for a FIFO writer.
readonly IRQ_RATE_OBJECT_FIFO="${IRQ_RATE_OUTPUT_DIR}/object.fifo"
mkfifo "${IRQ_RATE_OBJECT_FIFO}"
irq_rate_fifo_status=0
timeout -k 1 5 "${IRQ_RATE_BIN}" --bpf-path "${IRQ_RATE_OBJECT_FIFO}" --duration 1 --output-format json --output-path "${IRQ_RATE_OUTPUT_DIR}/fifo.json" > "${IRQ_RATE_OUTPUT_DIR}/fifo.log" 2>&1 || irq_rate_fifo_status=$?
assert_eq "${irq_rate_fifo_status}" 1 "IRQ FIFO exit code" || fatal "invalid BPF input did not fail promptly: $(cat "${IRQ_RATE_OUTPUT_DIR}/fifo.log")"
grep -Fq 'must be a regular file' "${IRQ_RATE_OUTPUT_DIR}/fifo.log" || fatal "missing BPF file type error: $(cat "${IRQ_RATE_OUTPUT_DIR}/fifo.log")"
[[ ! -e ${IRQ_RATE_OUTPUT_DIR}/fifo.json ]] || fatal "invalid BPF input produced a report"

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
	profiler_pty_cleanup
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
assert_irqtracing_tui_requires_terminal() {
	if "${IRQ_RATE_BIN}" --bpf-path "${IRQ_RATE_BPF}" --output-format tui \
		< /dev/null > "${IRQ_RATE_OUTPUT_DIR}/non-terminal.log" 2>&1; then
		return 1
	fi
	grep -Fq 'tui output requires terminal stdin and stdout' "${IRQ_RATE_OUTPUT_DIR}/non-terminal.log"
}

run_irqtracing_tui() {
	local prefix=$1 report=$2
	shift 2
	profiler_pty_start "${prefix}" "$@"
	wait_until "${IRQ_RATE_TOOL_DEADLINE}" 0.1 grep -Esq '^value: [1-9][0-9]*  self:' "${prefix}.raw" \
		|| fatal "expected a TUI with nonzero sample weight: $(tail -n 30 "${prefix}.raw")"
	grep -Eq 'incomplete: collection=[1-9][0-9]*' "${prefix}.raw" \
		|| fatal "IRQ TUI did not report collection loss: $(tail -n 30 "${prefix}.raw")"
	[[ -s ${report} ]] || fatal "IRQ TUI report missing while viewer is open: ${report}"
	printf g >&"${profiler_pty_input_fd}"
	wait_until 5 0.1 grep -Fq 'group by:' "${prefix}.raw" \
		|| fatal "IRQ grouping editor did not open: $(tail -n 30 "${prefix}.raw")"
	printf 'irq_context,irq_vector\r' >&"${profiler_pty_input_fd}"
	wait_until 5 0.1 grep -Fq 'groups: irq_context,irq_vector' "${prefix}.raw" \
		|| fatal "IRQ grouping did not apply: $(tail -n 30 "${prefix}.raw")"
	printf q >&"${profiler_pty_input_fd}"
	profiler_pty_wait
	assert_profiler_pty_restored "${prefix}" \
		|| fatal "TUI did not restore the terminal: $(tail -n 30 "${prefix}.raw")"
}

irqtracing_tui_weight() {
	awk '/^value: [1-9][0-9]*  self:/ { print $2; exit }' "$1"
}

assert_irqtracing_tui_requires_terminal \
	|| fatal "expected TUI terminal requirement: $(< "${IRQ_RATE_OUTPUT_DIR}/non-terminal.log")"
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

readonly IRQ_RATE_REPORT="${IRQ_RATE_OUTPUT_DIR}/profile.gob"
run_irqtracing_tui "${IRQ_RATE_OUTPUT_DIR}/capture" "${IRQ_RATE_REPORT}" "${IRQ_RATE_BIN}" \
	--bpf-path "${IRQ_RATE_BPF}" --cpu-list "${IRQ_RATE_CPU_ID}" --duration "${IRQ_RATE_DURATION}" \
	--max-events-per-second-per-cpu "${IRQ_RATE_MAX_EVENTS_PER_SECOND_PER_CPU}" \
	--output-format tui --output-path "${IRQ_RATE_REPORT}"
kill "${irq_rate_flood_pid}" 2> /dev/null || true
wait "${irq_rate_flood_pid}" || true
irq_rate_flood_pid=""

assert_eq "$(test -s "${IRQ_RATE_REPORT}" && echo 1 || echo 0)" 1 "TUI report saved before viewing" \
	|| fatal "irqtracing did not save ${IRQ_RATE_REPORT}"
for irq_rate_replay_bin in "${IRQ_RATE_BIN}" "${IRQ_RATE_PROFILER_BIN}"; do
	irq_rate_replay="${IRQ_RATE_OUTPUT_DIR}/replay-${irq_rate_replay_bin##*/}"
	run_irqtracing_tui "${irq_rate_replay}" "${IRQ_RATE_REPORT}" "${irq_rate_replay_bin}" --tui-open "${IRQ_RATE_REPORT}"
	assert_eq "$(irqtracing_tui_weight "${irq_rate_replay}.raw")" \
		"$(irqtracing_tui_weight "${IRQ_RATE_OUTPUT_DIR}/capture.raw")" "replayed IRQ sample weight" \
		|| fatal "${irq_rate_replay_bin} did not replay the expected IRQ profile"
	assert_eq "$(grep -om1 -E 'collection=[0-9]+' "${irq_rate_replay}.raw")" \
		"$(grep -om1 -E 'collection=[0-9]+' "${IRQ_RATE_OUTPUT_DIR}/capture.raw")" "replayed IRQ collection loss" \
		|| fatal "${irq_rate_replay_bin} did not replay the expected IRQ loss"
done
log_info "PASS"
