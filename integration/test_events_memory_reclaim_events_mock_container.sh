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

# Exercise paired reclaim hooks and real CPU-cgroup attribution.
set -euo pipefail

source "${ROOT_DIR}/integration/bpf_mock/common/lib.sh"

skip_known_mock_kprobe_kernel

require_commands make gcc kubectl jq ss insmod rmmod
require_readable "${KUBELET_CERT}" "${KUBELET_KEY}"
require_bpf_mock_platform

mkdir -p "${HUATUO_BAMAI_TEST_TMPDIR}/trigger"
readonly reclaim_namespace="bpf-reclaim-${BASHPID}-${RANDOM}"
readonly reclaim_pod="reclaim-trigger"
readonly reclaim_device=/dev/memory_reclaim_events_mock
readonly reclaim_trigger_name="reclaim-$(head -c 6 /proc/sys/kernel/random/uuid)"
readonly reclaim_events="${HUATUO_BAMAI_TEST_TMPDIR}/es-reclaim-events.json"
readonly reclaim_metadata="${HUATUO_BAMAI_TEST_TMPDIR}/container.json"

[[ -r "${ROOT_DIR}/_output/bpf/memory_reclaim_events.o" ]] || fatal "memory_reclaim_events.o is missing; run make build"
fixture_dir="${HUATUO_BAMAI_TEST_TMPDIR}/module"
build_bpf_mock_module memory_reclaim_events "${fixture_dir}"
# Busybox does not provide the host libc. A static trigger has no image dependencies.
compile_user_fixture "${ROOT_DIR}/integration/bpf_mock/memory_reclaim_events/memory_reclaim_events_trigger.c" \
	"${HUATUO_BAMAI_TEST_TMPDIR}/trigger/${reclaim_trigger_name}" -pthread -static

loaded_module=""
trap 'cleanup_bpf_mock_es "$?" "${loaded_module}" || exit $?' EXIT
start_bpf_mock_es
reclaim_port=$(allocate_available_port) || fatal "cannot allocate a test daemon port"
readonly reclaim_port
HUATUO_BAMAI_ADDR="http://127.0.0.1:${reclaim_port}"
HUATUO_BAMAI_METRICS_API="${HUATUO_BAMAI_ADDR}/metrics"
load_bpf_mock_module memory_reclaim_events "${fixture_dir}"
create_bpf_mock_namespace "${reclaim_namespace}" "${reclaim_pod}"
create_bpf_mock_pod "${reclaim_namespace}" "${reclaim_pod}" \
	"${HUATUO_BAMAI_TEST_TMPDIR}/trigger" "${reclaim_device}"

reclaim_trigger() {
	kubectl --request-timeout=8s exec -n "${reclaim_namespace}" "${reclaim_pod}" -c trigger -- \
		"/mock/${reclaim_trigger_name}" "$1" /host-proc
}

write_reclaim_config() {
	write_bpf_mock_es_config memory_reclaim_events
	write_bpf_mock_container_config "${reclaim_port}"
	cat >> "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" << EOF

[EventTracing.MemoryReclaim]
    BlockedThreshold = ${reclaim_threshold}
EOF
}

# The maximum signed TOML integer filters these short calls regardless of scheduling.
readonly reclaim_filtered_threshold=9223372036854775807
for phase in report filtered; do
	reclaim_threshold=1
	[[ ${phase} == filtered ]] && reclaim_threshold=${reclaim_filtered_threshold}
	integration_huatuo_bamai_start write_reclaim_config --region dev --log-debug \
		--bpf-mock --bpf-mock-object memory_reclaim_events.o
	await_bpf_mock_attached reclaim_perf_events kprobe:try_to_free_pages_mock kretprobe:try_to_free_pages_mock
	wait_until 60 0.5 read_bpf_mock_container "${reclaim_namespace}" "${reclaim_pod}" cpu "${reclaim_metadata}" || {
		cat "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" >&2 || true
		fatal "Huatuo did not discover the pod CPU CSS"
	}
	if [[ ${phase} == report ]]; then
		"${HUATUO_BAMAI_TEST_TMPDIR}/trigger/${reclaim_trigger_name}" main > "${HUATUO_BAMAI_TEST_TMPDIR}/host-main.json"
	fi
	for scenario in main thread; do
		reclaim_trigger "${scenario}" > "${HUATUO_BAMAI_TEST_TMPDIR}/${phase}-${scenario}.json"
	done
	if [[ ${phase} == filtered ]]; then
		# Observe a reader/ES flush window before shutdown when no event is expected.
		sleep 2
	fi
	# Confirm the reader consumed reports before cancellation can close its pipe.
	checker_scenarios=()
	[[ ${phase} != report ]] || checker_scenarios=(host-main report-main report-thread)
	verify_bpf_mock_es_events memory_reclaim_events "${reclaim_events}" \
		"${HUATUO_BAMAI_TEST_TMPDIR}" "${reclaim_metadata}" "${reclaim_filtered_threshold}" "${checker_scenarios[@]}"
	stop_bpf_mock_daemon
	mv "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${HUATUO_BAMAI_TEST_TMPDIR}/${phase}-huatuo.log"
done

verify_bpf_mock_es_events memory_reclaim_events "${reclaim_events}" "${HUATUO_BAMAI_TEST_TMPDIR}" \
	"${reclaim_metadata}" "${reclaim_filtered_threshold}"

verify_bpf_mock_container_alive "${reclaim_namespace}" "${reclaim_pod}" "$(jq -er '.data.id' "${reclaim_metadata}")" \
	|| fatal "mock events killed or restarted the test container"

log_info "mock reclaim passed: container identities, duration bounds, host exclusion, and threshold filtering verified"
