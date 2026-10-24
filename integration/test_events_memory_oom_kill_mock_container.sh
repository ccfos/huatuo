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

# Exercise OOM attribution and enrichment with a real container and a mock hook.
set -euo pipefail

source "${ROOT_DIR}/integration/bpf_mock/common/lib.sh"

skip_known_mock_kprobe_kernel

require_commands make gcc kubectl jq ss insmod rmmod
require_readable "${KUBELET_CERT}" "${KUBELET_KEY}"
require_bpf_mock_platform

mkdir -p "${HUATUO_BAMAI_TEST_TMPDIR}/trigger"
readonly oom_namespace="bpf-oom-${BASHPID}-${RANDOM}"
readonly oom_pod="oom-trigger"
readonly oom_device=/dev/memory_oom_kill_mock
readonly oom_trigger_name="oom-mock-$(head -c 6 /proc/sys/kernel/random/uuid)"
readonly oom_events="${HUATUO_BAMAI_TEST_TMPDIR}/es-oom-events.json"
readonly -a oom_scenarios=(same-task other-task memcg other-task-memcg null-victim)
readonly oom_metadata="${HUATUO_BAMAI_TEST_TMPDIR}/container.json"

[[ -r "${ROOT_DIR}/_output/bpf/memory_oom_kill.o" ]] || fatal "memory_oom_kill.o is missing; run make build"
fixture_dir="${HUATUO_BAMAI_TEST_TMPDIR}/module"
build_bpf_mock_module memory_oom_kill "${fixture_dir}"
# Busybox does not provide the host libc. A static trigger has no image dependencies.
compile_user_fixture "${ROOT_DIR}/integration/bpf_mock/memory_oom_kill/memory_oom_kill_trigger.c" \
	"${HUATUO_BAMAI_TEST_TMPDIR}/trigger/${oom_trigger_name}" -static

loaded_module=""
trap 'cleanup_bpf_mock_es "$?" "${loaded_module}" || exit $?' EXIT
start_bpf_mock_es
oom_port=$(allocate_available_port) || fatal "cannot allocate a test daemon port"
readonly oom_port
HUATUO_BAMAI_ADDR="http://127.0.0.1:${oom_port}"
HUATUO_BAMAI_METRICS_API="${HUATUO_BAMAI_ADDR}/metrics"
load_bpf_mock_module memory_oom_kill "${fixture_dir}"
create_bpf_mock_namespace "${oom_namespace}" "${oom_pod}"
create_bpf_mock_pod "${oom_namespace}" "${oom_pod}" \
	"${HUATUO_BAMAI_TEST_TMPDIR}/trigger" "${oom_device}"

oom_trigger() {
	kubectl --request-timeout=8s exec -n "${oom_namespace}" "${oom_pod}" -c trigger -- \
		"/mock/${oom_trigger_name}" --host-proc /host-proc --memory-css "${oom_memory_css}" "$@"
}

write_oom_container_config() {
	write_bpf_mock_es_config memory_oom_kill
	write_bpf_mock_container_config "${oom_port}"
}
integration_huatuo_bamai_start write_oom_container_config --region dev --log-debug \
	--bpf-mock --bpf-mock-object memory_oom_kill.o
await_bpf_mock_attached oom_perf_events kprobe:oom_kill_process_mock

wait_until 60 0.5 read_bpf_mock_container "${oom_namespace}" "${oom_pod}" memory "${oom_metadata}" || {
	# The runner prints only the first 160 lines; discovery logs can follow them.
	log_error "----- Huatuo container discovery log -----"
	cat "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" >&2 || true
	fatal "Huatuo did not discover the pod memory CSS"
}
oom_container_id=$(jq -er '.data.id' "${oom_metadata}")
readonly oom_container_id
oom_memory_css=$(jq -er '.data.cgroup_css.memory' "${oom_metadata}")
readonly oom_memory_css
readonly oom_container_expectation="${HUATUO_BAMAI_TEST_TMPDIR}/expected-container.json"
jq --arg pod "${oom_pod}" '.data.hostname = $pod' "${oom_metadata}" > "${oom_container_expectation}"

host_counter() {
	huatuo_bamai_collect_metrics || return 1
	awk '/^huatuo_bamai_memory_oom_kill_host_total\{/ { print $2; found = 1 } END { exit !found }' \
		"${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt"
}
oom_host_before=$(host_counter) || fatal "OOM host counter baseline is missing"
readonly oom_host_before

for scenario in "${oom_scenarios[@]}"; do
	oom_trigger "$scenario" 0x1ace 321 > "${HUATUO_BAMAI_TEST_TMPDIR}/${scenario}.json"
done
oom_trigger null-control 0x1ace 321 > "${HUATUO_BAMAI_TEST_TMPDIR}/null-control.json"

container_counter_is_four() {
	huatuo_bamai_collect_metrics || return 1
	awk -v pod="${oom_pod}" '
  /^huatuo_bamai_memory_oom_kill_container_total\{/ && index($0, "container_host=\"" pod "\"") && index($0, "container_name=\"trigger\"") {
   found++; value = $2
  }
  END { exit !(found == 1 && value == 4) }
 ' "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt"
}
wait_until 20 0.2 container_counter_is_four || fatal "container OOM counter did not increase to four"
# Metrics follow the victim: the missing-victim event increments host_total.
host_counter_is_expected() {
	local after
	after=$(host_counter) || return 1
	awk -v before="${oom_host_before}" -v after="$after" 'BEGIN { exit !(after == before + 1) }'
}
wait_until 20 0.2 host_counter_is_expected \
	|| fatal "missing-victim OOM did not increment the host counter by one"
stop_bpf_mock_daemon

verify_bpf_mock_es_events memory_oom_kill "${oom_events}" -container "${oom_container_expectation}" "${HUATUO_BAMAI_TEST_TMPDIR}" "${oom_scenarios[@]}" null-control

verify_bpf_mock_container_alive "${oom_namespace}" "${oom_pod}" "${oom_container_id}" \
	|| fatal "mock events killed or restarted the test container"

log_info "container mock OOM passed: ES documents, discovery, actor attribution, cgroup snapshots, container_total=4, host_total +1 for null victim"
