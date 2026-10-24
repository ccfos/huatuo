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
[[ -r /sys/kernel/btf/vmlinux ]] || skip "requires kernel BTF"
kernel_version_le 5 7 && skip "host task iterator requires Linux 5.8 or newer"
require_commands python3
case $(uname -m) in x86_64 | aarch64) ;; *) skip "bounded clone workload requires amd64 or arm64" ;; esac
observability_component=dload
integration_huatuo_bamai_start write_observability_config --region integration --disable-kubelet --log-debug
assert_memsnapshot_tracing_metrics dload integration 1
for observability_i in 1 2; do
	python3 "${ROOT_DIR}/integration/testdata/test_observability_fixture.py" blocked \
		"${HUATUO_BAMAI_TEST_TMPDIR}/blocked-${observability_i}.child" &
	observability_workers+=("$!")
	wait_until 10 .1 test -s "${HUATUO_BAMAI_TEST_TMPDIR}/blocked-${observability_i}.child" || fatal "blocked worker did not start"
	observability_workers+=("$(< "${HUATUO_BAMAI_TEST_TMPDIR}/blocked-${observability_i}.child")")
done
observability_dload_blocked() {
	local pid=${observability_workers[0]}
	awk '$1=="State:" {found=1; if ($2!="D") exit 1} END {if (!found) exit 1}' "/proc/${pid}/status"
}
wait_until 10 .1 observability_dload_blocked || fatal "workload did not enter D state"
wait_until 90 .2 observability_event_ready dload || fatal "host dload did not persist"
assert_observability_dload() {
	jq -e -s --arg pid "${observability_workers[0]}" '
 length == 1 and (.[0] |
 .tracer_name == "dload" and .tracer_type == "autotracing"
 and (.container_id // "") == ""
 and .tracer_data.nr_uninterruptible >= 1 and .tracer_data.dload_avg > 0
 and (.tracer_data.stack | contains("Pid: " + $pid + "\n")))
 ' "${HUATUO_BAMAI_TEST_TMPDIR}/events/dload" > /dev/null || fatal "invalid host dload event"
}
assert_observability_dload
assert_observability_stop
