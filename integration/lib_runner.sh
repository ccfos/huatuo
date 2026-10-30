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

source "${ROOT_DIR}/integration/lib_runner_report.sh"

# Test execution inside the runner's UTS and mount namespaces.

# Unreviewed tests run alone. A group serializes cases sharing kernel resources.
# Network tests share traffic and devices.
declare -A runner_test_groups=(
	["integration/test_apiserver_continuous_profiling_api.sh"]=independent
	["integration/test_apiserver_continuous_profiling_concurrent_jobs.sh"]=independent
	["integration/test_apiserver_continuous_profiling_control.sh"]=independent
	["integration/test_apiserver_job_recovery.sh"]=independent
	["integration/test_apiserver_profile_disabled.sh"]=independent
	["integration/test_apiserver_profile_filters.sh"]=independent
	["integration/test_apiserver_readiness_metrics.sh"]=independent
	["integration/test_autotracing_cpusys.sh"]=independent
	["integration/test_autotracing_iotracing.sh"]=independent
	["integration/test_autotracing_irqtracing.sh"]=independent
	["integration/test_basic_bpf_debug_macro.sh"]=independent
	["integration/test_basic_dockerfile_check.sh"]=independent
	["integration/test_basic_storage_elasticsearch_values.sh"]=independent
	["integration/test_basic_storage_opensearch_elasticsearch.sh"]=network
	["integration/test_basic_startup_log.sh"]=independent
	["integration/test_dropwatch_devlink.sh"]=network
	["integration/test_dropwatch_icmpv6.sh"]=network
	["integration/test_dropwatch_ipv4_options.sh"]=network
	["integration/test_dropwatch_netdevsim.sh"]=network
	["integration/test_dropwatch_reason.sh"]=network
	["integration/test_events_net_rx_latency.sh"]=network
	["integration/test_events_tcp_retransmit.sh"]=network
	["integration/test_metrics.sh"]=independent
	["integration/test_metrics_exclude_filter.sh"]=independent
	["integration/test_metrics_include_filter.sh"]=independent
	["integration/test_node_cloud_events_api.sh"]=independent
	["integration/test_node_config_api.sh"]=independent
	["integration/test_profiler_config.sh"]=independent
	["integration/test_profiler_java_cpu_container.sh"]=profiler-java
	["integration/test_profiler_java_cpu_multi_pid.sh"]=profiler-java
	["integration/test_profiler_java_memory_usage_alloc.sh"]=profiler-java
	["integration/test_profiler_native_cpu.sh"]=profiler
	["integration/test_profiler_native_cpu_container.sh"]=profiler
	["integration/test_profiler_native_cpu_cpp_symbols.sh"]=profiler
	["integration/test_profiler_native_cpu_rust_symbols.sh"]=profiler
	["integration/test_profiler_native_cpu_thread_group.sh"]=profiler
	["integration/test_profiler_native_memory_thread_group.sh"]=independent
	["integration/test_profiler_native_memory_virtual_alloc.sh"]=independent
	["integration/test_profiler_output_flags.sh"]=independent
	["integration/test_profiler_python_cpu_multi_pid.sh"]=profiler
	["integration/test_profiler_user_kernel_frames.sh"]=independent
	["integration/test_tcp_retransmit_ratelimit.sh"]=network
	["integration/test_tcp_retransmit_syn.sh"]=network
	["integration/test_tcp_retransmit_synack.sh"]=network
	["integration/test_tcp_retransmit_tlp.sh"]=network
	["integration/test_tcpshark_retransmit_dropwatch_correlation.sh"]=network
	["integration/test_tcpshark_retransmit_fast.sh"]=network
)

# Execution isolation is independent of scheduling/resource exclusion.
declare -A runner_test_namespaces=(
	["integration/test_events_netdev_events.sh"]="mount net"
	["integration/test_events_netdev_bonding_lacp.sh"]="mount net"
	["integration/test_events_net_rx_latency.sh"]="mount net"
	["integration/test_dropwatch_ratelimit.sh"]="mount net"
	["integration/test_autotracing_memburst.sh"]="mount pid"
	["integration/test_tcpshark_output_cancellation_blocked.sh"]="mount net"
	["integration/test_tcpshark_output_cancellation_healthy.sh"]="mount net"
	["integration/test_tcpshark_output_cancellation_timeout.sh"]="mount net"
)

declare -A runner_shared_storage_tests=(
	["integration/test_apiserver_continuous_profiling_api.sh"]=1
	["integration/test_apiserver_continuous_profiling_concurrent_jobs.sh"]=1
	["integration/test_apiserver_continuous_profiling_control.sh"]=1
	["integration/test_apiserver_job_recovery.sh"]=1
	["integration/test_apiserver_profile_filters.sh"]=1
	["integration/test_autotracing_irqtracing.sh"]=1
	["integration/test_basic_storage_elasticsearch_values.sh"]=1
)

runner_validate_test_metadata() {
	local table key value
	for table in runner_test_groups runner_test_namespaces runner_shared_storage_tests; do
		local -n entries=$table
		for key in "${!entries[@]}"; do
			[[ $key == integration/test_*.sh || $key == e2e/test_*.sh ]] && [[ ${key#*/} != */* && -f $ROOT_DIR/$key ]] \
				|| fatal "$table references an unknown test: $key"
			value=${entries[$key]}
			case $table:$value in
			runner_test_groups:exclusive | runner_test_groups:independent | runner_test_groups:network | runner_test_groups:profiler | runner_test_groups:profiler-java | runner_test_namespaces:mount | "runner_test_namespaces:mount net" | "runner_test_namespaces:mount pid" | runner_shared_storage_tests:1) ;;
			*) fatal "unsupported $table value for $key: $value" ;;
			esac
		done
	done
}

runner_test_group() {
	local name=${1##*/} group
	group=${runner_test_groups["${runner_suite}/${name}"]:-exclusive}
	if [[ ${group} == independent || ${group} == profiler ]]; then group=${name}; fi
	printf '%s\n' "${group}"
}

runner_start_execution() {
	local -a command=(unshare --mount)
	local script=$1 slot=$2 group=$3 workspace name port_dir port_base
	name=${script##*/}
	local namespaces=${runner_test_namespaces["${runner_suite}/${name}"]:-mount}
	case $namespaces in
	"mount net") command+=(--net) ;;
	"mount pid")
		env --default-signal=INT,TERM true 2> /dev/null \
			|| fatal "PID namespace workers require env with --default-signal support"
		# Keep the reset after unshare --fork and before Bash: Bash cannot trap
		# signals inherited as ignored. Resetting them inside runner_worker.sh
		# is too late; cancellation can otherwise skip EXIT cleanup and leave
		# execution.json missing when the runner forcibly kills the worker.
		command+=(--pid --fork --kill-child=TERM --mount-proc env --default-signal=INT,TERM)
		;;
	esac
	command+=(bash "${ROOT_DIR}/integration/runner_worker.sh" "$runner_suite" "$namespaces" "$script")
	workspace=$(mktemp -d "${RUNNER_OUTPUT_DIR}/${name%.sh}.${runner_run_index}.XXXXXX")
	runner_pids[slot]=""
	runner_scripts[slot]=${name}
	runner_workspaces[slot]=${workspace}
	runner_groups[slot]=${group}
	runner_started[slot]=$(report_clock)
	runner_cancelled[slot]=""
	: > "${workspace}/stdout.log"
	: > "${workspace}/stderr.log"
	if ! report_execution_start "${name}" "${runner_run_index}" "${workspace}"; then
		runner_failed=1 runner_report_failed=1
		return 1
	fi
	log_info "🚀🚀 start: ${name} (${runner_run_index}/${runner_runs_per_test})"
	port_dir="${RUNNER_OUTPUT_DIR}/ports/${slot}"
	port_base=$((20000 + slot * 1000))
	unset RUNNER_REASON_DIR RUNNER_REASON_OWNER
	# Job control gives each case its own process group and preserves SIGINT.
	set -m
	HUATUO_BAMAI_TEST_TMPDIR="${workspace}" RUNNER_REASON_DIR="${workspace}" INTEGRATION_PORT_DIR="${port_dir}" INTEGRATION_PORT_BASE="${port_base}" \
		"${command[@]}" >> "${workspace}/stdout.log" 2>> "${workspace}/stderr.log" &
	runner_pids[slot]=$!
	set +m
}

runner_read_execution_result() {
	local path=$1/execution.json
	[[ -f $path ]] || {
		log_error "worker result is missing: $path"
		return 1
	}
	# The final marker preserves trailing newlines through command substitution.
	jq -esr '
		if length != 1 then error("expected one result object") else .[0] end |
		def valid_code: type == "number" and . >= 0 and . <= 255 and floor == .;
		if type != "object" then error("expected an object")
		elif (has("exit_code") | not) or (.exit_code != null and (.exit_code | valid_code | not)) then error("invalid exit_code")
		elif (.cleanup_status != 0 and .cleanup_status != 1) then error("invalid cleanup_status")
		elif (.cleanup_reason | type) != "string" then error("invalid cleanup_reason")
		elif .cleanup_status == 1 and .cleanup_reason == "" then error("missing cleanup_reason")
		else (.exit_code | tostring), (.cleanup_status | tostring), (.cleanup_reason + ".") end
	' "$path" || {
		log_error "worker result is invalid: $path"
		return 1
	}
}

runner_finalize_execution() {
	local slot=$1 test_exit_code=null worker_exit_code=0 status=0 result=passed reason="" workspace=${runner_workspaces[$1]}
	local cleanup_status=0 cleanup_reason="" payload="" result_valid=false
	if [[ -n ${runner_pids[slot]} ]]; then
		wait "${runner_pids[slot]}" || worker_exit_code=$?
		status=$worker_exit_code
	else
		status=1 reason="test preparation failed before the test process started"
	fi
	if [[ -n ${runner_pids[slot]} ]] && payload=$(runner_read_execution_result "$workspace"); then
		result_valid=true
		test_exit_code=${payload%%$'\n'*}
		payload=${payload#*$'\n'}
		cleanup_status=${payload%%$'\n'*}
		cleanup_reason=${payload#*$'\n'}
		cleanup_reason=${cleanup_reason%.}
		if [[ $test_exit_code != null ]]; then status=$test_exit_code; fi
	elif [[ -n ${runner_pids[slot]} ]]; then
		# Namespace-local PIDs must not be used for fallback cleanup on the host.
		local namespaces=${runner_test_namespaces["${runner_suite}/${runner_scripts[slot]}"]:-mount}
		RUNNER_CLEANUP_REASON=""
		if [[ $namespaces != "mount pid" ]]; then
			integration_test_cleanup "$workspace" || cleanup_status=$?
		fi
		cleanup_reason=$RUNNER_CLEANUP_REASON
		result=failed
		if [[ -f $workspace/execution.json ]]; then
			reason="worker result is invalid: $workspace/execution.json"
		else
			reason="worker result is missing: $workspace/execution.json"
		fi
	fi
	if [[ $result_valid == true ]]; then
		if [[ $status == 77 ]]; then
			result=skipped reason="test exited with code 77 without a skip reason"
		elif [[ $status != 0 ]]; then
			result=failed reason="test exited with code $status; see stdout.log and stderr.log"
		elif [[ $test_exit_code == null || $worker_exit_code != 0 ]]; then
			result=failed reason="worker exit status $worker_exit_code disagrees with its result; see stderr.log"
		fi
		if [[ -f $workspace/reason.$status.log && $status != 0 ]]; then reason=$(< "$workspace/reason.$status.log"); fi
	elif [[ -z ${runner_pids[slot]} ]]; then
		result=failed
	fi
	if [[ -n ${runner_cancelled[slot]:-} ]] && [[ $status == 0 || $status == 77 || $status == 130 || $status == 137 || $status == 143 ]] \
		&& { [[ $result_valid == true ]] || { [[ ! -e $workspace/execution.json ]] && [[ $worker_exit_code == 130 || $worker_exit_code == 137 || $worker_exit_code == 143 ]]; }; }; then
		result=skipped reason="execution interrupted: ${runner_cancelled[slot]}"
	fi
	find "$workspace" -type f -name timings.log -exec cat {} + || true
	if ((cleanup_status)); then
		result=failed
		reason="${reason:+${reason}; }${cleanup_reason:-test cleanup failed; see service diagnostics}"
	fi
	if [[ $result == failed ]]; then
		runner_failed=1
		dump_text_files "$workspace" || true
	fi
	if ! report_execution_finish "${runner_scripts[slot]}" "$runner_run_index" "$result" "$test_exit_code" "$reason" "${runner_started[slot]}"; then
		runner_failed=1 runner_report_failed=1
		log_error "cannot record ${runner_scripts[slot]} (${runner_run_index}/${runner_runs_per_test}); continuing resource cleanup"
	fi
	unset 'runner_pids[slot]' 'runner_workspaces[slot]' 'runner_groups[slot]' 'runner_cancelled[slot]'
}

runner_stop_storage() {
	if [[ -n ${runner_last_storage_test} ]]; then stop_shared_storage || runner_failed=1; fi
}

runner_cancel_executions() {
	local reason=$1 slot pid
	local -a stopping=()
	for slot in "${!runner_pids[@]}"; do
		pid=${runner_pids[slot]}
		if [[ -n ${pid} ]] && ! kill -0 "${pid}" 2> /dev/null; then
			runner_finalize_execution "${slot}" || runner_failed=1
		else
			runner_cancelled[slot]=${reason}
		fi
	done
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
}

runner_cleanup() {
	local status=$? slot
	local final_status=completed reason="" cancelled=false
	trap - EXIT
	trap '' INT TERM
	if [[ ${status} == 130 || ${status} == 143 ]]; then
		final_status=interrupted
		if [[ ${status} == 130 ]]; then reason="runner received SIGINT"; else reason="runner received SIGTERM"; fi
		runner_cancel_executions "${reason}"
		cancelled=true
	fi
	# Report failures can first occur while draining an ordinary test failure.
	# Recheck before each wait so remaining cases still get bounded cancellation.
	for slot in "${!runner_pids[@]}"; do
		if ((runner_report_failed)) && [[ ${cancelled} == false ]]; then
			runner_cancel_executions "test report write failed"
			cancelled=true
		fi
		[[ -v runner_pids[slot] ]] || continue
		runner_finalize_execution "${slot}" || runner_failed=1
	done
	runner_stop_storage
	if ((runner_failed)) && [[ ${status} != 130 && ${status} != 143 ]]; then status=1; fi
	if [[ ${status} != 0 && ${final_status} != interrupted ]]; then
		final_status=aborted reason="runner stopped after a test or framework failure"
	fi
	[[ ${status} -ne 77 ]] || status=0
	if ((runner_report_failed)); then
		log_error "test report is incomplete: ${RUNNER_OUTPUT_DIR}/result.json"
		status=1
	else
		report_finish "${final_status}" "${status}" "${reason}" || status=1
		report_summary || status=1
	fi
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
			if ! kill -0 "${runner_pids[slot]}" 2> /dev/null; then runner_finalize_execution "${slot}"; fi
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
				runner_start_execution "${pending[index]}" "${slot}" "${group}"
				unset 'pending[index]'
				break
			done
		done
		if ((${#runner_pids[@]})); then sleep 0.1; fi
	done
}

run_test_suite() {
	runner_runs_per_test=$1 runner_suite=$2 runner_jobs=$3
	shift 3
	local -a scripts=("$@") parallel=() profilers=() exclusive=()
	local script group
	runner_last_storage_test="" runner_failed=0 runner_report_failed=0
	declare -ga runner_pids=() runner_workspaces=() runner_started=() runner_groups=() runner_scripts=() runner_cancelled=()
	if [[ -z ${RUNNER_OUTPUT_DIR:-} || ${RUNNER_OUTPUT_DIR} != "${HUATUO_BAMAI_TEST_TMPDIR}" || ! -f ${RUNNER_OUTPUT_DIR}/result.json ]]; then
		fatal "runner report context mismatch: output=${RUNNER_OUTPUT_DIR:-unset}, workspace=${HUATUO_BAMAI_TEST_TMPDIR}; initialize the report before running the suite"
	fi
	runner_validate_test_metadata
	unset RUNNER_REASON_DIR RUNNER_REASON_OWNER
	for script in "${scripts[@]}"; do
		if [[ ${runner_shared_storage_tests["${runner_suite}/${script##*/}"]:-0} == 1 ]]; then
			runner_last_storage_test=$script
		fi
		group=${runner_test_groups["${runner_suite}/${script##*/}"]:-exclusive}
		case ${group} in
		exclusive) exclusive+=("${script}") ;;
		profiler | profiler-java) profilers+=("${script}") ;;
		*) parallel+=("${script}") ;;
		esac
	done
	if [[ -n ${runner_last_storage_test} ]]; then
		source "${ROOT_DIR}/integration/lib_storage.sh"
	fi
	trap runner_cleanup EXIT
	trap 'exit 130' INT
	trap 'exit 143' TERM
	for ((runner_run_index = 1; runner_run_index <= runner_runs_per_test; runner_run_index++)); do
		if [[ -n ${runner_last_storage_test} ]]; then
			STORAGE_SHARED_DIR=$(mktemp -d "${RUNNER_OUTPUT_DIR}/shared-elasticsearch.${runner_run_index}.XXXXXX")
			export STORAGE_SHARED_DIR
		fi
		if ((runner_jobs > 1)); then
			runner_parallel_cases "${runner_jobs}" "${parallel[@]}"
			runner_stop_storage
			((runner_failed == 0)) || exit 1
			# PID/container-isolated profilers run after ES shuts down, with at most
			# two cases competing for CPU. Benchmarks and CPU-pinned cases stay alone.
			# Java cases share an agent library path and serialize within this pool.
			runner_parallel_cases 2 "${profilers[@]}"
			for script in "${exclusive[@]}"; do
				runner_start_execution "${script}" 0 exclusive
				runner_finalize_execution 0
				((runner_failed == 0)) || exit 1
			done
			runner_stop_storage
		else
			for script in "${scripts[@]}"; do
				runner_start_execution "${script}" 0 exclusive
				runner_finalize_execution 0
				((runner_failed == 0)) || exit 1
				if [[ ${script} == "${runner_last_storage_test}" ]]; then
					runner_stop_storage
					((runner_failed == 0)) || exit 1
				fi
			done
		fi
	done
	exit 0
}
