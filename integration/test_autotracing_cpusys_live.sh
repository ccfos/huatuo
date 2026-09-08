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
require_commands mkfifo getconf
[[ -x ${ROOT_DIR}/_output/bin/perf ]] || fatal "missing standard build artifact: ${ROOT_DIR}/_output/bin/perf; run make build"
readonly observability_cpus=$(getconf _NPROCESSORS_ONLN)
[[ ${observability_cpus} -le 256 ]] || skip "CPU workload is bounded to 64 workers (256 CPUs)"
observability_component=cpusys
integration_huatuo_bamai_start write_observability_config \
	--region integration --disable-kubelet --log-debug \
	--tools-bin-dir "${ROOT_DIR}/_output/bin"
# The acknowledgement follows the first percentage sample. Startup delays
# cannot release the workload before the tracer has established its baseline.
wait_until 30 .1 grep -Fq 'msg="sampled host cpu"' "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
	|| fatal "CPU tracer did not establish a sampling baseline"
for ((observability_i = 0; observability_i < (observability_cpus + 3) / 4; observability_i++)); do
	observability_fifo="${HUATUO_BAMAI_TEST_TMPDIR}/cpu-${observability_i}.fifo"
	mkfifo "${observability_fifo}"
	timeout 45s bash -c 'read -r _ < "$1"; while :; do :; done' _ "${observability_fifo}" &
	observability_workers+=("$!")
	printf 'run\n' > "${observability_fifo}"
done
wait_until 30 .1 observability_event_ready cpusys || fatal "CPU burst did not persist"
assert_observability_cpu() {
	jq -e -s '
	 length == 1 and (.[0] |
	 .tracer_name == "cpusys" and .tracer_type == "autotracing"
	 and (.container_id // "") == ""
	 and (.tracer_data.trigger_reasons | index("user") != null and index("total") != null)
	 and (.tracer_data.flamedata | length > 0))
	' "${HUATUO_BAMAI_TEST_TMPDIR}/events/cpusys" > /dev/null || fatal "invalid CPU burst event"
}
assert_observability_cpu
observability_cleanup
observability_workers=()
assert_observability_stop
