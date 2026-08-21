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

# Verify sched_tick loads and attaches without forcing scheduler tick delays.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"

[[ $EUID -eq 0 ]] || skip "requires root"

[[ -r "${ROOT_DIR}/_output/bpf/sched_tick.o" ]] \
	|| fatal "sched_tick BPF object not found: ${ROOT_DIR}/_output/bpf/sched_tick.o"

kprobe_available account_process_tick \
	|| skip "account_process_tick is not available for kprobe"

if kprobe_available tick_nohz_restart_sched_tick; then
	readonly RESTART_SCHED_TICK_SYMBOL=tick_nohz_restart_sched_tick
elif kprobe_available __tick_nohz_idle_restart_tick; then
	readonly RESTART_SCHED_TICK_SYMBOL=__tick_nohz_idle_restart_tick
else
	skip "scheduler tick restart kprobe is not available"
fi

tracepoint_available timer tick_stop \
	|| skip "timer/tick_stop tracepoint is not available"

integration_huatuo_bamai_start write_sched_tick_config

huatuo_bamai_collect_metrics
check_metrics "sched_tick lifecycle" \
	'tracing_status_hitcount\{[^}]*tracing="sched_tick"[^}]*\} 0$' \
	'tracing_status_running\{[^}]*\} 1$'

assert_log_has_no_failure \
	"${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "huatuo-bamai"

log_info "sched_tick load and attach test passed (${RESTART_SCHED_TICK_SYMBOL})"
