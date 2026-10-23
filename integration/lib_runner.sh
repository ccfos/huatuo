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

# Test execution inside the runner's UTS and mount namespaces.

# Unreviewed tests run alone. A group serializes cases sharing kernel resources.
# Network tests share traffic and devices.
declare -A runner_test_groups=(
	["test_apiserver_continuous_profiling_api.sh"]=independent
	["test_apiserver_continuous_profiling_concurrent_jobs.sh"]=independent
	["test_apiserver_continuous_profiling_control.sh"]=independent
	["test_apiserver_job_recovery.sh"]=independent
	["test_apiserver_profile_disabled.sh"]=independent
	["test_apiserver_profile_filters.sh"]=independent
	["test_apiserver_readiness_metrics.sh"]=independent
	["test_autotracing_cpusys.sh"]=independent
	["test_autotracing_iotracing.sh"]=independent
	["test_autotracing_irqtracing.sh"]=independent
	["test_basic_bpf_debug_macro.sh"]=independent
	["test_basic_dockerfile_check.sh"]=independent
	["test_basic_storage_elasticsearch_values.sh"]=independent
	["test_basic_storage_opensearch_elasticsearch.sh"]=network
	["test_basic_startup_log.sh"]=independent
	["test_dropwatch_devlink.sh"]=network
	["test_dropwatch_icmpv6.sh"]=network
	["test_dropwatch_ipv4_options.sh"]=network
	["test_dropwatch_netdevsim.sh"]=network
	["test_dropwatch_reason.sh"]=network
	["test_events_net_rx_latency.sh"]=network
	["test_events_tcp_retransmit.sh"]=network
	["test_metrics.sh"]=independent
	["test_metrics_exclude_filter.sh"]=independent
	["test_metrics_include_filter.sh"]=independent
	["test_node_cloud_events_api.sh"]=independent
	["test_node_config_api.sh"]=independent
	["test_profiler_config.sh"]=independent
	["test_profiler_java_cpu_container.sh"]=profiler-java
	["test_profiler_java_cpu_multi_pid.sh"]=profiler-java
	["test_profiler_java_memory_usage_alloc.sh"]=profiler-java
	["test_profiler_native_cpu.sh"]=profiler
	["test_profiler_native_cpu_container.sh"]=profiler
	["test_profiler_native_cpu_cpp_symbols.sh"]=profiler
	["test_profiler_native_cpu_rust_symbols.sh"]=profiler
	["test_profiler_native_cpu_thread_group.sh"]=profiler
	["test_profiler_native_memory_physical_usage.sh"]=independent
	["test_profiler_native_memory_thread_group.sh"]=independent
	["test_profiler_native_memory_virtual_alloc.sh"]=independent
	["test_profiler_output_flags.sh"]=independent
	["test_profiler_python_cpu_multi_pid.sh"]=profiler
	["test_profiler_user_kernel_frames.sh"]=independent
	["test_tcp_retransmit_ratelimit.sh"]=network
	["test_tcp_retransmit_syn.sh"]=network
	["test_tcp_retransmit_synack.sh"]=network
	["test_tcp_retransmit_tlp.sh"]=network
	["test_tcpshark_retransmit_dropwatch_correlation.sh"]=network
	["test_tcpshark_retransmit_fast.sh"]=network
)

runner_test_group() {
	local name=${1##*/} group
	group=${runner_test_groups[${name}]:-exclusive}
	if [[ ${group} == independent || ${group} == profiler ]]; then group=${name}; fi
	printf '%s\n' "${group}"
}

runner_start_case() {
	local -a command=(bash "$1")
	local script=$1 slot=$2 group=$3 workspace name port_dir="" port_base="" address=${HUATUO_BAMAI_ADDR:-http://127.0.0.1:19704}
	name=${script##*/}
	if ((runner_jobs > 1)) && [[ ${group} != exclusive ]]; then
		command=(unshare --mount bash "${ROOT_DIR}/integration/exec_case.sh" "${script}")
		port_dir="${HUATUO_BAMAI_TEST_TMPDIR}/ports/${slot}"
		port_base=$((20000 + slot * 1000))
		local port
		port=$(INTEGRATION_PORT_DIR="${port_dir}" INTEGRATION_PORT_BASE="${port_base}" allocate_available_port) \
			|| fatal "cannot allocate worker ${slot} daemon port"
		address="http://127.0.0.1:${port}"
	fi
	workspace=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/${name%.sh}.XXXXXX")
	runner_pids[slot]=""
	runner_names[slot]="${name} (${runner_round}/${runner_repeats})"
	runner_workspaces[slot]=${workspace}
	runner_groups[slot]=${group}
	runner_started[slot]=${SECONDS}
	log_info "🚀🚀 start: ${runner_names[slot]}"
	if [[ ${runner_suite} == e2e ]]; then
		HUATUO_BAMAI_TEST_TMPDIR="${workspace}" huatuo_bamai_start "${HUATUO_BAMAI_ARGS_E2E[@]}"
	fi
	# Job control gives each case its own process group and preserves SIGINT.
	set -m
	HUATUO_BAMAI_TEST_TMPDIR="${workspace}" INTEGRATION_PORT_DIR="${port_dir}" INTEGRATION_PORT_BASE="${port_base}" \
		HUATUO_BAMAI_ADDR="${address}" HUATUO_BAMAI_METRICS_API="${address}/metrics" \
		"${command[@]}" > "${workspace}/test.log" 2>&1 &
	runner_pids[slot]=$!
	set +m
}

runner_finish_case() {
	local slot=$1 status=0 workspace=${runner_workspaces[$1]}
	if [[ -n ${runner_pids[slot]} ]]; then wait "${runner_pids[slot]}" || status=$?; else status=1; fi
	find "${workspace}" -type f -name timings.log -exec cat {} + || true
	if [[ ${runner_suite} == e2e ]]; then
		HUATUO_BAMAI_TEST_TMPDIR="${workspace}" e2e_test_teardown "${status}" || status=1
		if [[ ${status} -ne 0 && ${status} -ne 77 ]]; then
			log_error "e2e artifacts: ${workspace}"
			dump_text_files "${workspace}" || true
		fi
	else
		integration_test_exit "${status}" "${workspace}" || status=1
	fi
	test_result_record "${runner_names[slot]} duration=$((SECONDS - runner_started[slot]))s" "${status}"
	if [[ ${status} -ne 0 && ${status} -ne 77 ]]; then runner_failed=1; fi
	unset 'runner_pids[slot]' 'runner_workspaces[slot]' 'runner_groups[slot]'
}

runner_stop_storage() {
	local status=$1
	if [[ -n ${runner_last_storage} ]]; then stop_shared_storage "${status}" || runner_failed=1; fi
}

runner_cleanup() {
	local status=$? slot pid
	local -a stopping=()
	trap - EXIT
	trap '' INT TERM
	if [[ ${status} == 130 || ${status} == 143 ]]; then
		# Cancel all cases together; each gets five seconds for its EXIT cleanup.
		for pid in "${runner_pids[@]}"; do
			[[ -n ${pid} ]] || continue
			(
				trap - EXIT
				stop_by_process_group "${pid}" 5
			) &
			stopping+=("$!")
		done
		for pid in "${stopping[@]}"; do wait "${pid}" || true; done
	fi
	# Ordinary failures still let other in-flight cases finish normally.
	for slot in "${!runner_pids[@]}"; do runner_finish_case "${slot}"; done
	if ((runner_failed)) && [[ ${status} != 130 && ${status} != 143 ]]; then status=1; fi
	runner_stop_storage "${status}"
	if ((runner_failed)) && [[ ${status} != 130 && ${status} != 143 ]]; then status=1; fi
	if [[ ${runner_suite} == integration && ${status} -eq 0 ]]; then
		rm -rf -- "${HUATUO_BAMAI_TEST_TMPDIR}/ports"
		rmdir -- "${HUATUO_BAMAI_TEST_TMPDIR}" || status=1
	fi
	test_results_summary
	[[ ${status} -ne 77 ]] || status=0
	exit "${status}"
}

runner_parallel_cases() {
	local jobs=$1
	shift
	local -a pending=("$@")
	local slot index group busy running_group
	while ((${#pending[@]} || ${#runner_pids[@]})); do
		# Reap every completed case before scheduling more work after a failure.
		for slot in "${!runner_pids[@]}"; do
			if ! kill -0 "${runner_pids[slot]}" 2> /dev/null; then runner_finish_case "${slot}"; fi
		done
		((runner_failed == 0)) || return 1
		for ((slot = 0; slot < jobs; slot++)); do
			[[ -z ${runner_pids[slot]:-} ]] || continue
			for index in "${!pending[@]}"; do
				group=$(runner_test_group "${pending[index]}")
				busy=false
				for running_group in "${runner_groups[@]}"; do
					if [[ ${group} == "${running_group}" ]]; then
						busy=true
						break
					fi
				done
				[[ ${busy} == false ]] || continue
				runner_start_case "${pending[index]}" "${slot}" "${group}"
				unset 'pending[index]'
				break
			done
		done
		if ((${#runner_pids[@]})); then sleep 0.1; fi
	done
}

run_test_suite() {
	runner_repeats=$1 runner_suite=$2 runner_jobs=$3
	shift 3
	local -a scripts=("$@") parallel=() profilers=() exclusive=()
	local script group
	runner_last_storage="" runner_failed=0
	declare -ga runner_pids=() runner_workspaces=() runner_names=() runner_started=() runner_groups=()
	if [[ ${runner_suite} == e2e ]]; then source "${ROOT_DIR}/e2e/lib.sh"; fi
	for script in "${scripts[@]}"; do
		if [[ ${runner_suite} == integration ]]; then
			case ${script##*/} in
			test_apiserver_continuous_profiling_*.sh | test_apiserver_job_recovery.sh | test_apiserver_profile_filters.sh | test_autotracing_irqtracing.sh | test_basic_storage_elasticsearch_values.sh) runner_last_storage=${script} ;;
			esac
		fi
		group=${runner_test_groups[${script##*/}]:-exclusive}
		case ${group} in
		exclusive) exclusive+=("${script}") ;;
		profiler | profiler-java) profilers+=("${script}") ;;
		*) parallel+=("${script}") ;;
		esac
	done
	if [[ -n ${runner_last_storage} ]]; then
		export STORAGE_SHARED_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/default-es"
		source "${ROOT_DIR}/integration/lib_storage.sh"
	fi
	test_results_init
	trap runner_cleanup EXIT
	trap 'exit 130' INT
	trap 'exit 143' TERM
	for ((runner_round = 1; runner_round <= runner_repeats; runner_round++)); do
		if ((runner_jobs > 1)); then
			rm -rf -- "${HUATUO_BAMAI_TEST_TMPDIR}/ports"
			runner_parallel_cases "${runner_jobs}" "${parallel[@]}"
			runner_stop_storage 0
			((runner_failed == 0)) || exit 1
			# PID/container-isolated profilers run after ES shuts down, with at most
			# two cases competing for CPU. Benchmarks and CPU-pinned cases stay alone.
			# Java cases share an agent library path and serialize within this pool.
			runner_parallel_cases 2 "${profilers[@]}"
			for script in "${exclusive[@]}"; do
				runner_start_case "${script}" 0 exclusive
				runner_finish_case 0
				((runner_failed == 0)) || exit 1
			done
			runner_stop_storage 0
		else
			for script in "${scripts[@]}"; do
				runner_start_case "${script}" 0 exclusive
				runner_finish_case 0
				((runner_failed == 0)) || exit 1
				if [[ ${script} == "${runner_last_storage}" ]]; then stop_shared_storage 0; fi
			done
		fi
	done
	exit 0
}
