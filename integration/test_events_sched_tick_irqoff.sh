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

# Verify threshold filtering without disabling host IRQs.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"

readonly SCHED_TICK_REPORT_TIMEOUT=5
readonly SCHED_TICK_REPORT_INTERVAL=0.1
readonly SCHED_TICK_NO_EVENT_WINDOW=2
readonly SCHED_TICK_EVENT="${HUATUO_BAMAI_TEST_TMPDIR}/events/sched_tick"
readonly VALID_SCHED_TICK_EVENT="${HUATUO_BAMAI_TEST_TMPDIR}/valid-sched-tick-event.json"

workload_pid=""

cleanup() {
	[[ -n "${workload_pid}" ]] || return 0
	kill "${workload_pid}" 2> /dev/null || true
	wait "${workload_pid}" 2> /dev/null || true
}
trap cleanup EXIT

start_sched_tick_test() {
	local config_writer=$1

	integration_huatuo_bamai_start \
		"${config_writer}" \
		--region dev \
		--procfs-prefix "${HUATUO_BAMAI_TEST_FIXTURES}" \
		--disable-kubelet \
		--log-debug
}

[[ $EUID -eq 0 ]] || skip "requires root"

command -v jq > /dev/null || skip "jq command is not installed"
command -v taskset > /dev/null || skip "taskset command is not installed"
taskset -c 0 true > /dev/null 2>&1 \
	|| skip "CPU 0 is not available to the test process"
[[ -r "${ROOT_DIR}/_output/bpf/sched_tick.o" ]] \
	|| fatal "sched_tick BPF object not found: ${ROOT_DIR}/_output/bpf/sched_tick.o"

kprobe_available account_process_tick \
	|| skip "account_process_tick is not available for kprobe"

if ! kprobe_available tick_nohz_restart_sched_tick \
	&& ! kprobe_available __tick_nohz_idle_restart_tick; then
	skip "scheduler tick restart kprobe is not available"
fi

tracepoint_available timer tick_stop \
	|| skip "timer/tick_stop tracepoint is not available"

# Kernel symbol resolution is covered by internal/symbol tests. This test verifies
# that a captured BPF stack reaches persistent event storage.
sched_tick_event_is_valid() {
	[[ -s "${SCHED_TICK_EVENT}" ]] || return 1
	jq -s -e '
		first(.[] | select(
			(.hostname | type == "string")
			and .region == "dev"
			and (.uploaded_time | type == "string")
			and (.time | type == "string")
			and .tracer_name == "sched_tick"
			and (.tracer_id | type == "string")
			and (.tracer_time | type == "string")
			and .tracer_type == "event"
			and .tracer_data.tick_interval_threshold_ns == 1
			and (.tracer_data.tick_interval_ns | type == "number")
			and .tracer_data.tick_interval_ns >= .tracer_data.tick_interval_threshold_ns
			and (.tracer_data.comm | type == "string")
			and (.tracer_data.pid | type == "number")
			and (.tracer_data.cpu | type == "number")
			and (.tracer_data | has("now") | not)
			and (.tracer_data | has("ktime_ns") | not)
			and (.tracer_data.stack | type == "string")
			and (.tracer_data.stack | startswith("stack:\n"))
			and ((.tracer_data.stack | ltrimstr("stack:\n") | length) > 0)
		))
	' "${SCHED_TICK_EVENT}" > "${VALID_SCHED_TICK_EVENT}" 2> /dev/null
}
# Attach while a pinned CPU is ticking so NO_HZ idle cannot hide samples.
taskset -c 0 bash -c 'while :; do :; done' &
workload_pid=$!

start_sched_tick_test write_sched_tick_config

huatuo_bamai_collect_metrics
check_metrics "sched_tick high threshold" \
	'tracing_status_running\{[^}]*\} 1$'

sleep "${SCHED_TICK_NO_EVENT_WINDOW}"

[[ ! -s "${SCHED_TICK_EVENT}" ]] \
	|| fatal "sched_tick persisted an event with the configured high threshold"

assert_log_has_no_failure \
	"${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "huatuo-bamai"

huatuo_bamai_stop

start_sched_tick_test write_sched_tick_irqoff_config

wait_until "${SCHED_TICK_REPORT_TIMEOUT}" "${SCHED_TICK_REPORT_INTERVAL}" \
	sched_tick_event_is_valid \
	|| fatal "sched_tick did not persist a valid event within ${SCHED_TICK_REPORT_TIMEOUT}s"

assert_log_has_no_failure \
	"${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "huatuo-bamai"

cleanup

log_info "sched_tick irqoff simulation test passed"
log_info "valid sched_tick event:"
jq '.' "${VALID_SCHED_TICK_EVENT}"
