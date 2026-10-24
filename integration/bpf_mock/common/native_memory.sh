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

# Shared lifecycle for the three native memory profiler fixtures.
source "${ROOT_DIR}/integration/bpf_mock/common/lib.sh"

# Active process IDs are retained only so EXIT cleanup can stop unfinished work.
cleanup_native_memory_mock() {
	local status=$?
	[[ -z ${native_mock_profiler_pid:-} ]] || stop_by_pid "${native_mock_profiler_pid}" 5 || status=1
	for pid in ${native_mock_target_pid:-} ${native_mock_excluded_pid:-}; do
		kill -TERM "${pid}" 2> /dev/null || true
	done
	for pid in ${native_mock_launcher_pid:-} ${native_mock_excluded_launcher:-}; do
		wait "${pid}" 2> /dev/null || true
	done
	cleanup_bpf_mock "${status}" "${loaded_module:-}" || status=$?
	return "${status}"
}

native_memory_identity_ready() {
	[[ -s $1 ]] && head -n1 "$1" | jq -e '.pid > 0 and .tid > 0 and (.comm | length > 0)' > /dev/null
}

native_memory_triggered() {
	[[ -s $1 ]] && jq -se 'any(.[]; .triggered == true)' "$1" > /dev/null
}

run_native_memory_case() {
	local feature=$1 case_name=$2 default_scenario=$3 trigger=$4
	local namespace=$5 container_id=$6
	local mode=${feature#native_} tool="${ROOT_DIR}/_output/bin/profiler"
	local case_dir="${HUATUO_BAMAI_TEST_TMPDIR}/${case_name}"
	local scenario caller expected_scenario
	local -a profiler_args
	scenario=${case_name}
	caller=main
	expected_scenario=${case_name}
	if [[ ${case_name} == worker || ${case_name} == pid-filtered ]]; then
		caller=thread
		scenario=${default_scenario}
		[[ ${case_name} == pid-filtered ]] || expected_scenario=${scenario}
	fi
	mkdir -p "${case_dir}"
	if [[ -z ${namespace} ]]; then
		"${trigger}" "${scenario}" "${caller}" > "${case_dir}/trigger.jsonl" 2> "${case_dir}/trigger.err" &
	else
		kubectl --request-timeout=120s exec -n "${namespace}" trigger -c trigger -- "/mock/${trigger##*/}" "${scenario}" "${caller}" /host-proc > "${case_dir}/trigger.jsonl" 2> "${case_dir}/trigger.err" &
	fi
	native_mock_launcher_pid=$!
	wait_until 10 0.1 native_memory_identity_ready "${case_dir}/trigger.jsonl" || fatal "trigger did not become ready"
	head -n1 "${case_dir}/trigger.jsonl" > "${case_dir}/identity.json"
	native_mock_target_pid=$(jq -er '.pid' "${case_dir}/identity.json")
	profiler_args=(--pid "${native_mock_target_pid}")
	[[ ${case_name} != worker ]] || profiler_args+=(--thread-group)
	if [[ -n ${namespace} ]]; then
		profiler_args=(--container-id "${container_id}" --huatuo-api-address "${HUATUO_BAMAI_ADDR#http://}")
		# A real host caller runs the same mock while the container CSS filter is active.
		"${trigger}" "${scenario}" main > "${case_dir}/excluded.jsonl" 2> "${case_dir}/excluded.err" &
		native_mock_excluded_launcher=$!
		wait_until 10 0.1 native_memory_identity_ready "${case_dir}/excluded.jsonl" || fatal "excluded host trigger not ready"
		native_mock_excluded_pid=$(head -n1 "${case_dir}/excluded.jsonl" | jq -er '.pid')
	fi
	[[ ${mode} == virtual_alloc ]] || profiler_args+=(--physical-memory-probability 100)
	# Run from the built package so the CLI finds its normal bpf/ directory.
	(cd "${ROOT_DIR}/_output" && "${tool}" --type memory --language c --memory-mode "${mode}" --bpf-mock "${profiler_args[@]}" --duration 5 --aggr-interval 5 --output-format collapsed --output-path "${case_dir}" --verbose) > "${case_dir}/profiler.log" 2>&1 &
	native_mock_profiler_pid=$!
	wait_until 15 0.1 profiler_ready "${case_dir}/profiler.log" || fatal "native profiler did not start"
	grep -q 'symbol=".*_mock"' "${case_dir}/profiler.log" || fatal "profiler did not attach to mock hooks"
	# Keep module addresses to diagnose incorrect kernel stack symbolization.
	awk -v module="[${feature}_mock]" '$4 == module || $3 == "dummy_init_module"' \
		/proc/kallsyms > "${case_dir}/kernel-symbols.log"
	kill -USR1 "${native_mock_target_pid}"
	wait_until 5 0.1 native_memory_triggered "${case_dir}/trigger.jsonl" || fatal "native ioctl failed"
	if [[ -n ${namespace} ]]; then
		kill -USR1 "${native_mock_excluded_pid}"
		wait_until 5 0.1 native_memory_triggered "${case_dir}/excluded.jsonl" || fatal "excluded host ioctl failed"
	fi
	wait "${native_mock_profiler_pid}" || fatal "native profiler failed"
	native_mock_profiler_pid=""
	assert_log_has_no_failure "${case_dir}/profiler.log" "native memory profiler"
	! grep -Eq 'level="(error|panic|fatal)"|BPF perf event samples lost|drain failed' "${case_dir}/profiler.log" || fatal "native profile reported a reader error or lost samples"
	kill -USR2 "${native_mock_target_pid}"
	wait "${native_mock_launcher_pid}" || fatal "native trigger failed"
	native_mock_target_pid=""
	native_mock_launcher_pid=""
	if [[ -n ${namespace} ]]; then
		kill -USR2 "${native_mock_excluded_pid}"
		wait "${native_mock_excluded_launcher}" || fatal "excluded host trigger failed"
		native_mock_excluded_pid=""
		native_mock_excluded_launcher=""
	fi
	run_bpf_mock_checker "${feature}" "${case_dir}" "${case_dir}/identity.json" "${expected_scenario}" "$(getconf PAGESIZE)"
}

run_native_memory_mock() {
	skip_known_mock_kprobe_kernel
	local feature=$1 scope=$2 case_name
	shift 2
	local fixture_dir="${HUATUO_BAMAI_TEST_TMPDIR}/module"
	local trigger_dir="${HUATUO_BAMAI_TEST_TMPDIR}/trigger"
	local trigger_name="nmock-$(head -c 6 /proc/sys/kernel/random/uuid)"
	local source_dir="${ROOT_DIR}/integration/bpf_mock/${feature}"
	local tool="${ROOT_DIR}/_output/bin/profiler" namespace="" container_id=""
	local -a scenarios=("$@")
	require_commands make gcc insmod rmmod jq getconf
	require_bpf_mock_platform
	[[ -x ${tool} && -r ${ROOT_DIR}/_output/bpf/${feature}.o ]] || fatal "profiler or ${feature}.o missing; run make build"
	mkdir -p "${trigger_dir}"
	build_bpf_mock_module "${feature}" "${fixture_dir}" "${ROOT_DIR}/integration/bpf_mock/common/kernel_hook.awk"
	local -a compile_args=(-pthread)
	[[ ${scope} == container ]] && compile_args+=(-static)
	compile_user_fixture "${source_dir}/${feature}_trigger.c" "${trigger_dir}/${trigger_name}" "${compile_args[@]}"
	loaded_module=""
	trap 'cleanup_native_memory_mock || exit $?' EXIT
	load_bpf_mock_module "${feature}" "${fixture_dir}"
	if [[ ${scope} == container ]]; then
		require_commands kubectl ss
		require_readable "${KUBELET_CERT}" "${KUBELET_KEY}"
		namespace="bpf-native-${BASHPID}-${RANDOM}"
		local container_metadata="${HUATUO_BAMAI_TEST_TMPDIR}/container.json"
		local port
		port=$(allocate_available_port) || fatal "cannot allocate daemon port"
		HUATUO_BAMAI_ADDR="http://127.0.0.1:${port}"
		HUATUO_BAMAI_METRICS_API="${HUATUO_BAMAI_ADDR}/metrics"

		write_native_memory_config() {
			write_single_event_config "" ""
			write_bpf_mock_container_config "${port}"
		}
		integration_huatuo_bamai_start write_native_memory_config --region dev --log-debug

		create_bpf_mock_namespace "${namespace}" trigger
		create_bpf_mock_pod "${namespace}" trigger "${trigger_dir}" "/dev/${feature}_mock"
		wait_until 60 0.5 read_bpf_mock_container "${namespace}" trigger memory "${container_metadata}" || fatal "Huatuo did not discover container memory CSS"
		container_id=$(jq -er '.data.id' "${container_metadata}")
	fi
	# Main-only filtering and thread-group filtering exercise distinct BPF branches.
	scenarios+=(worker pid-filtered)
	for case_name in "${scenarios[@]}"; do
		# Container targeting selects by CSS and intentionally has no PID filter.
		[[ ${scope} != container || ${case_name} != pid-filtered ]] || continue
		run_native_memory_case "${feature}" "${case_name}" "${scenarios[0]}" \
			"${trigger_dir}/${trigger_name}" "${namespace}" "${container_id}"
	done
	if [[ ${scope} == container ]]; then
		verify_bpf_mock_container_alive "${namespace}" trigger "${container_id}" || fatal "test container restarted"
		stop_bpf_mock_daemon
	fi
}
