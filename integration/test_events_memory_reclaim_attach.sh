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

set -euo pipefail
source "${ROOT_DIR}/integration/lib_observability.sh"
require_commands python3
[[ -r /sys/kernel/tracing/events/syscalls/sys_enter_getpid/id || -r /sys/kernel/debug/tracing/events/syscalls/sys_enter_getpid/id ]] \
	|| skip "getpid tracepoint is required for the controlled event fixture"
observability_component=memory_reclaim_events
integration_huatuo_bamai_start write_config_bamai_observability \
	--region integration --disable-kubelet --log-debug --bpf-dir "${ROOT_DIR}/_output/bpf"
observability_reclaim_attached() {
	grep -F 'map_name="reclaim_perf_events"' "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" | grep -Fq 'msg="attached BPF and created event pipe"'
}
wait_until 30 .1 observability_reclaim_attached || fatal "reclaim probes did not attach"
assert_long_running_tracing_metrics memory_reclaim_events integration 1
assert_observability_stop

# Replace only the event source. The normal daemon, decoder and store remain real.
readonly reclaim_fixture_dir="${HUATUO_BAMAI_TEST_TMPDIR}/bpf-fixture"
readonly reclaim_event="${HUATUO_BAMAI_TEST_TMPDIR}/events/memory_reclaim"
mkdir -p "${reclaim_fixture_dir}"
compile_bpf_fixture "${ROOT_DIR}/integration/testdata/test_memory_reclaim_events.bpf.c" \
	"${reclaim_fixture_dir}/memory_reclaim_events.o"
cp "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${HUATUO_BAMAI_TEST_TMPDIR}/reclaim-production-attach.log"
# Production reclaim events must not contaminate exact fixture counts.
rm -f "${reclaim_event}"
integration_huatuo_bamai_start write_config_bamai_observability \
	--region integration --disable-kubelet --log-debug --bpf-dir "${reclaim_fixture_dir}"
wait_until 15 .1 observability_reclaim_attached || fatal "fixture event pipe did not attach"

emit_reclaim_fixture() {
	python3 - "$1" << 'PYTHON'
import ctypes, os, sys
lib = ctypes.CDLL(None)
# Preserve fixture order in one per-CPU perf buffer.
os.sched_setaffinity(0, {min(os.sched_getaffinity(0))})
pid = os.getpid()
modes = [0, 1] if sys.argv[1] == "burst" else [1]
for mode in modes:
    name = ("huatuo-reclaim%d" % mode).encode()
    if lib.prctl(15, ctypes.c_char_p(name), 0, 0, 0) != 0:
        raise OSError("cannot set fixture comm")
    for _ in range(32):
        if lib.getpid() != pid:
            raise RuntimeError("fixture syscall returned a different PID")
lib.prctl(15, ctypes.c_char_p(b"python3"), 0, 0, 0)
PYTHON
}
assert_reclaim_fixture_events() {
	local expected=$1
	jq -e -s --argjson expected "${expected}" '
 length == $expected and all(.[];
 .tracer_name == "memory_reclaim" and .tracer_type == "event"
 and (.container_id // "") == ""
 and .tracer_data.reclaim_duration_ns == 1000000001
 and .tracer_data.pid > 0 and .tracer_data.tid > 0
 and (.tracer_data.comm == "huatuo-reclaim0" or .tracer_data.comm == "huatuo-reclaim1"))
 ' "${reclaim_event}" > /dev/null
}
emit_reclaim_fixture burst
wait_until 5 .1 assert_reclaim_fixture_events 1 || fatal "burst did not persist exactly one unresolved event"
# Start after the first persisted event, beyond its acceptance time.
reclaim_cooldown_deadline=$(python3 -c 'import time; print(time.monotonic() + 1.0)')
readonly reclaim_cooldown_deadline
reclaim_cooldown_elapsed() {
	python3 - "${reclaim_cooldown_deadline}" << 'PYTHON'
import sys, time
sys.exit(0 if time.monotonic() >= float(sys.argv[1]) else 1)
PYTHON
}
wait_until 5 .1 reclaim_cooldown_elapsed || fatal "reclaim cooldown boundary was not reached"
emit_reclaim_fixture recovery
wait_until 5 .1 assert_reclaim_fixture_events 2 || fatal "unresolved stream did not recover after cooldown"
jq -e -s '.[0].tracer_data.comm == "huatuo-reclaim0" and .[1].tracer_data.comm == "huatuo-reclaim1"' \
	"${reclaim_event}" > /dev/null || fatal "zero and unknown CSS did not retain host output"
assert_observability_stop
assert_reclaim_fixture_events 2 || fatal "unexpected events persisted before shutdown"
log_info "PASS: production attachment; controlled unresolved event persistence, burst suppression and cooldown recovery"
# The fixture does not validate the kernel reclaim trigger or container discovery.
# Resolved-container bypass and cache failure boundaries are not covered here.
