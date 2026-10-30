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

# Check same-daemon IO recovery after deleting and rediscovering an owned SCSI device.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_io.sh"
io_test_isolate_run_directory

run_io_recovery() {
	set -euo pipefail
	source "${ROOT_DIR}/integration/lib.sh"
	source "${ROOT_DIR}/integration/config.sh"
	source "${ROOT_DIR}/integration/lib_io.sh"
	source "${ROOT_DIR}/integration/lib_io_wait.sh"
	source "${ROOT_DIR}/integration/lib_iolatency.sh"
	require_commands python3 jq timeout flock readlink
	CURL_TIMEOUT=(--connect-timeout 2 --max-time 3)
	WAIT_HUATUO_BAMAI_TIMEOUT=30
	[[ ${EUID} -eq 0 ]] || skip "IO recovery requires root"
	readonly RECOVERY_WORK="${HUATUO_BAMAI_TEST_TMPDIR}/io-recovery"
	readonly WAIT_HELPER="${ROOT_DIR}/integration/testdata/test_io_wait.py"
	mkdir -p "${RECOVERY_WORK}"
	cgroup_root=/sys/fs/cgroup
	control_prefix=io
	parent=""
	leaf=""
	workers=()
	delegated=0
	disks=()
	numbers=()
	locks=()
	scsi_address=""
	scsi_host=""
	scsi_scan=""
	collectors=()
	recovery_context=setup
	recovery_attempt=0
	recovery_directory=""
	recovery_metrics=""
	recovery_started=${SECONDS}
	recovery_control_pid=""

	record_recovery_stage() {
		local event=$1 details=${2:-} record
		printf -v record 'time=%s elapsed_seconds=%s pid=%s phase=%s event=%s %s' \
			"$(date -u +%FT%TZ)" "$((SECONDS - recovery_started))" "${BASHPID}" \
			"${recovery_context}" "${event}" "${details}"
		printf '%s\n' "${record}" >> "${RECOVERY_WORK}/stages.log"
		log_info "IO recovery ${record}"
	}

	recovery_owned() {
		local owner
		((io_test_scsi_owned)) || return 1
		read -r owner < /sys/module/scsi_debug/parameters/inq_vendor || return 1
		[[ ${owner} == "${io_test_scsi_owner}" ]]
	}

	recovery_host_owned() {
		local driver
		recovery_owned || return 1
		read -r driver < "/sys/class/scsi_host/${scsi_host}/proc_name" || return 1
		[[ ${driver} == scsi_debug ]]
	}

	recovery_control_pending() {
		[[ -r ${RECOVERY_WORK}/control.pid ]] \
			&& read -r recovery_control_pid < "${RECOVERY_WORK}/control.pid" \
			&& [[ ${recovery_control_pid} =~ ^[1-9][0-9]*$ && -r /proc/${recovery_control_pid}/cmdline ]] \
			&& timeout --kill-after=1 2 grep -Fxzq "${RECOVERY_WORK}/control.pid" "/proc/${recovery_control_pid}/cmdline"
	}

	recovery_write_control() {
		local path=$1 value=$2 status=0
		rm -f "${RECOVERY_WORK}/control.pid"
		record_recovery_stage control-write-begin "path=${path} value=${value}"
		timeout --kill-after=1 5 bash -c 'printf "%s\n" "$$" > "$3" || exit 1; printf "%s\n" "$2" > "$1"' \
			_ "${path}" "${value}" "${RECOVERY_WORK}/control.pid" || status=$?
		record_recovery_stage control-write-end "path=${path} exit=${status}"
		if ((status == 0)); then rm -f "${RECOVERY_WORK}/control.pid"; fi
		return "${status}"
	}

	recovery_control() {
		local index=$1 enabled=$2 collector=$3 owner value operation
		[[ ${collector} != iolatency ]] || return 0
		recovery_owned || return 1
		read -r owner < "/sys/block/${disks[index]}/device/vendor" || return 1
		[[ ${owner} == "${io_test_scsi_owner}" ]] || return 1
		if [[ ${collector} == blk_throtl ]]; then
			if [[ ${control_prefix} == io ]]; then
				value=max
				((enabled == 0)) || value=8
				recovery_write_control "${leaf}/io.max" "${numbers[index]} riops=${value} wiops=${value}" || return 1
				timeout --kill-after=1 2 cat "${leaf}/io.max" >> "${RECOVERY_WORK}/controls.txt" || return 1
			else
				for operation in read write; do
					recovery_write_control "${leaf}/blkio.throttle.${operation}_iops_device" "${numbers[index]} ${enabled/1/8}" || return 1
					timeout --kill-after=1 2 cat "${leaf}/blkio.throttle.${operation}_iops_device" >> "${RECOVERY_WORK}/controls.txt" || return 1
				done
			fi
		else
			if ((enabled)); then
				recovery_write_control "${cgroup_root}/${control_prefix}.cost.model" \
					"${numbers[index]} ctrl=user model=linear rbps=32768 rseqiops=8 rrandiops=8 wbps=32768 wseqiops=8 wrandiops=8" || return 1
			fi
			recovery_write_control "${cgroup_root}/${control_prefix}.cost.qos" \
				"${numbers[index]} enable=${enabled} ctrl=user rpct=0 rlat=0 wpct=0 wlat=0 min=100 max=100" || return 1
			timeout --kill-after=1 2 cat "${cgroup_root}/${control_prefix}.cost.model" "${cgroup_root}/${control_prefix}.cost.qos" \
				>> "${RECOVERY_WORK}/controls.txt" || return 1
		fi
	}

	recovery_disable_controls() {
		local index collector status=0
		for index in "${!disks[@]}"; do
			[[ -e /sys/block/${disks[index]} ]] || continue
			for collector in "${collectors[@]}"; do
				recovery_control_pending && return 1
				recovery_control "${index}" 0 "${collector}" || status=1
			done
		done
		return "${status}"
	}

	report_recovery_failure() {
		local index file directory attribute pid parents=${BASHPID}
		# Print the focused evidence before teardown resets controls and removes
		# device identity. CI may discard the VM and truncate generic file dumps.
		log_error "IO recovery failed: phase=${recovery_context} last_batch=${recovery_attempt}"
		timeout --kill-after=1 2 tail -n 40 "${RECOVERY_WORK}/stages.log" >&2 || true
		{
			printf 'IO recovery failure: phase=%s last_batch=%s daemon_pid=%s scsi_address=%s\n' \
				"${recovery_context}" "${recovery_attempt}" "${daemon_pid:-not-started}" "${scsi_address}"
			uname -a
			tail -n 40 "${RECOVERY_WORK}/stages.log"
			io_test_report_disk_lock || true
			for pid in "${daemon_pid:-}" "${recovery_control_pid}" "${workers[@]}"; do
				[[ ${pid} =~ ^[1-9][0-9]*$ ]] && parents+=",${pid}"
			done
			if command -v ps > /dev/null; then
				timeout --kill-after=1 2 ps -o pid,ppid,pgid,stat,wchan:32,args -p "${parents}" --ppid "${parents}" || true
				timeout --kill-after=1 2 ps -o pid= -p "${parents}" --ppid "${parents}" > "${RECOVERY_WORK}/failure-pids.txt" || true
				while read -r pid; do
					[[ -d /proc/${pid} ]] || continue
					printf 'process=%s\n' "${pid}"
					timeout --kill-after=1 2 cat "/proc/${pid}/wchan" "/proc/${pid}/stack" || true
				done < "${RECOVERY_WORK}/failure-pids.txt"
			fi
			printf 'cgroup_root=%s leaf=%s collectors=%s last_scrape=%s\n' \
				"${cgroup_root}" "${leaf}" "${collectors[*]}" "${recovery_metrics}"
			printf 'pid_record='
			cat "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid" 2> /dev/null || printf 'unavailable\n'
			for index in "${!disks[@]}"; do
				printf 'disk=%s expected_device=%s\n' "${disks[index]}" "${numbers[index]:-unknown}"
				for attribute in dev stat device/vendor device/model queue/iostats queue/nomerges queue/scheduler; do
					file="/sys/block/${disks[index]}/${attribute}"
					[[ ! -r ${file} ]] || {
						printf '%s=' "${file}"
						timeout --kill-after=1 2 cat "${file}" || printf 'read_exit=%s\n' "$?"
					}
				done
				for file in "${leaf}/io.max" "${leaf}"/blkio.throttle.*_device \
					"${cgroup_root}/${control_prefix}.cost.model" "${cgroup_root}/${control_prefix}.cost.qos"; do
					[[ ! -r ${file} ]] || {
						printf '%s:\n' "${file}"
						timeout --kill-after=1 2 awk -v device="${numbers[index]:-unknown}" '$1 == device' "${file}" || printf 'read_exit=%s\n' "$?"
					}
				done
			done
			while IFS= read -r directory; do
				[[ -d ${directory} ]] || continue
				printf 'workload_evidence=%s\n' "${directory}"
				for file in "${directory}"/{0,1}.json{,.diskstats,.log,.started} \
					"${directory}"/{0,1}.timing.json "${directory}"/{0,1}.{iocost,blk_throtl}.json \
					"${directory}"/sample.metrics.latency-{0,1}.{json,err}; do
					[[ ! -s ${file} ]] || {
						printf '%s:\n' "${file}"
						case ${file} in
						*.json) jq -c . "${file}" || cat "${file}" ;;
						*.started) printf '%s\n' "$(< "${file}")" ;;
						*) cat "${file}" ;;
						esac
					}
				done
				if [[ -r ${directory}/sample.metrics ]]; then
					awk '/^# TYPE huatuo_bamai_(iolatency|blk_throtl|iocost)/ { print; next }
					/^huatuo_bamai_(iolatency|blk_throtl|iocost|scrape_collector_success)/ &&
						(!/iolatency/ || /le="\+Inf"/ || /interval_/) { print }' "${directory}/sample.metrics"
				fi
			done < <(printf '%s\n' "${recovery_metrics%/*}" "${recovery_directory}" "${RECOVERY_WORK}/initial-baseline" \
				| awk 'NF && !seen[$0]++')
			if [[ -n ${io_test_bamai_log_dir:-} && -r ${io_test_bamai_log_dir}/huatuo.log ]]; then
				grep -E 'msg="(loaded BPF|attached BPF|.*session failed)' "${io_test_bamai_log_dir}/huatuo.log" | tail -n 60
			fi
		} > "${RECOVERY_WORK}/failure.txt" 2>&1
		timeout --kill-after=1 2 cat "${RECOVERY_WORK}/failure.txt" >&2 || true
	}

	cleanup_io_recovery() {
		local status=$? clean=1 fd worker
		trap - EXIT
		trap '' INT TERM
		set +e
		record_recovery_stage cleanup-begin "exit=${status}"
		recovery_control_pending || recovery_control_pid=""
		if ((status != 0 && status != 77)); then
			report_recovery_failure || log_error "could not collect all IO recovery failure context"
		fi
		recovery_disable_controls || clean=0
		for worker in "${workers[@]}"; do
			stop_and_wait_by_pid "${worker}" 3
			if kill -0 "${worker}" 2> /dev/null; then
				log_error "IO recovery cleanup retained workload ${worker}"
				clean=0
			fi
		done
		huatuo_bamai_stop || clean=0
		for fd in "${locks[@]}"; do exec {fd}<&-; done
		if recovery_control_pending; then
			log_error "IO control writer ${recovery_control_pid} remains; retaining owned cgroups and disks; inspect ${RECOVERY_WORK}/control.pid"
			clean=0
		else
			io_test_remove_cgroup "${leaf}" || clean=0
			io_test_remove_cgroup "${parent}" || clean=0
			if ((delegated && clean)); then
				recovery_write_control "${cgroup_root}/cgroup.subtree_control" -io || clean=0
			fi
		fi
		if ((clean)); then io_test_cleanup_disks || clean=0; fi
		if ((clean == 0)); then
			log_error "IO recovery cleanup incomplete; inspect ${RECOVERY_WORK}/resources.txt and io-disks.resources"
			status=1
		fi
		if ((status != 0 && status != 77)); then io_test_report_daemon_logs; fi
		record_recovery_stage cleanup-end "exit=${status} clean=${clean}"
		exit "${status}"
	}
	trap cleanup_io_recovery EXIT
	trap 'exit 130' INT
	trap 'exit 143' TERM
	if [[ ! -f ${cgroup_root}/cgroup.controllers ]]; then
		cgroup_root=/sys/fs/cgroup/blkio
		control_prefix=blkio
	fi
	[[ -d ${cgroup_root} ]] || skip "IO recovery requires a cgroup IO fixture"
	recovery_context=scsi-prepare
	record_recovery_stage begin "count=2"
	io_test_lock_disks 30
	io_test_prepare_scsi_disks 2
	record_recovery_stage end "owned=${io_test_scsi_owned} disks=${io_test_disks[*]}"
	((io_test_scsi_owned && ${#io_test_disks[@]} == 2)) || skip "IO recovery requires two owned scsi_debug disks"
	if [[ ${control_prefix} == io ]] && grep -qw io "${cgroup_root}/cgroup.controllers" \
		&& ! grep -qw io "${cgroup_root}/cgroup.subtree_control"; then
		delegated=1
		recovery_write_control "${cgroup_root}/cgroup.subtree_control" +io
	fi
	recovery_context=cgroup-prepare
	record_recovery_stage begin "root=${cgroup_root}"
	parent="${cgroup_root}/huatuo-io-recovery-$(< /proc/sys/kernel/random/uuid)"
	leaf="${parent}/load"
	printf 'parent=%s\nleaf=%s\nowner=%s\ndisks=%s\n' "${parent}" "${leaf}" \
		"${io_test_scsi_owner}" "${io_test_disks[*]}" > "${RECOVERY_WORK}/resources.txt"
	mkdir "${parent}"
	if [[ ${control_prefix} == io ]] && grep -qw io "${cgroup_root}/cgroup.subtree_control"; then
		recovery_write_control "${parent}/cgroup.subtree_control" +io
	fi
	mkdir "${leaf}"
	if [[ -w ${cgroup_root}/${control_prefix}.cost.model && -w ${cgroup_root}/${control_prefix}.cost.qos &&
		(! -r /sys/kernel/realtime || $(< /sys/kernel/realtime) != 1) ]]; then
		collectors+=(iocost)
	else
		log_info "NOT_RUN IOCOST recovery: writable cost controls and non-RT kernel are required"
	fi
	if [[ -w ${leaf}/io.max || -w ${leaf}/blkio.throttle.read_iops_device ]]; then
		collectors+=(blk_throtl)
	else
		log_info "NOT_RUN blk-throttle recovery: writable throttle controls are unavailable"
	fi
	collectors+=(iolatency)
	disks=("${io_test_disks[@]:0:2}")
	for index in 0 1; do
		numbers[index]=$(< "/sys/block/${disks[index]}/dev")
	done
	scsi_address=$(basename "$(readlink -f "/sys/block/${disks[0]}/device")")
	[[ ${scsi_address} =~ ^[0-9]+:[0-9]+:[0-9]+:[0-9]+$ ]] || fatal "invalid owned SCSI address: ${scsi_address}"
	scsi_host="host${scsi_address%%:*}"
	scsi_scan="${scsi_address#*:}"
	scsi_scan="${scsi_scan//:/ }"
	recovery_host_owned || fatal "selected SCSI host is not owned scsi_debug: ${scsi_host}"
	[[ -w /sys/class/scsi_device/${scsi_address}/device/delete && -w /sys/class/scsi_host/${scsi_host}/scan ]] \
		|| skip "SCSI delete/rescan controls are unavailable"
	printf 'scsi_address=%s host=%s scan=%s\n' "${scsi_address}" "${scsi_host}" "${scsi_scan}" >> "${RECOVERY_WORK}/resources.txt"
	record_recovery_stage end "leaf=${leaf} collectors=${collectors[*]}"

	write_config_bamai_io_recovery() {
		local collector
		local -a substitutions=()
		for collector in "${collectors[@]}"; do
			substitutions+=(-e "s/\"${collector}\"/\"sched_tick\"/")
		done
		write_config_bamai_sched_tick | sed "${substitutions[@]}"
	}

	assert_recovery_same_daemon() {
		local pid
		pid=$(< "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")
		[[ ${pid} == "${daemon_pid}" ]] && kill -0 "${pid}" 2> /dev/null \
			|| fatal "IO daemon exited or was replaced during recovery"
	}

	recovery_scrape() {
		local input=$1 operation=${2:-read} index
		recovery_metrics=${input}
		record_recovery_stage scrape-begin "metrics=${input}"
		assert_recovery_same_daemon
		scrape_started=$(date +%s.%N)
		huatuo_bamai_metrics > "${input}" || fatal "IO recovery HTTP request failed: ${input}"
		scrape_ended=$(date +%s.%N)
		io_test_metrics_json "${input}" > "${input}.json" || fatal "cannot decode IO recovery metrics: ${input}"
		for index in 0 1; do assert_recovery_latency "${input}" "${operation}" "${index}"; done
		record_recovery_stage scrape-end "metrics=${input}"
	}

	assert_recovery_latency() {
		local metrics=$1 operation=$2 index=$3 status=0
		assert_iolatency_metrics "${disks[index]}" "${operation}" drain "${metrics}" \
			"${scrape_started}" "${scrape_ended}" "${RECOVERY_WORK}/latency-last-interval-${index}" \
			"${metrics}.json" 2> "${metrics}.latency-${index}.err" || status=$?
		if ((status != 0 && status != 3)); then
			cat "${metrics}.latency-${index}.err" >&2
			fatal "${recovery_context} disk=${disks[index]}: invalid IOLatency schema or interval; inspect ${metrics}"
		fi
		cp "${metrics}.assertion.json" "${metrics}.latency-${index}.json"
	}

	assert_recovery_empty() {
		local metrics=$1 operation=${2:-all}
		# Exposition was decoded by recovery_scrape. Check every owned/retired
		# disk and all three modules together so a scrape cannot consume old data
		# from a module other than the one whose fresh workload is being awaited.
		jq -e --arg first "${disks[0]}" --arg second "${disks[1]}" --arg retired "${old_disk:-}" \
			--arg operation "${operation}" '
		all(.[] | select(.labels.disk != null and
			(.labels.disk == $first or .labels.disk == $second or .labels.disk == $retired))
			| select(.name | test("^huatuo_bamai_(iocost|blk_throtl|iolatency)_"))
			| select($operation == "all" or .labels.operation == $operation); .value == 0)
		' "${metrics}.json" > /dev/null || fatal "discarded or idle IO statistics reappeared: ${metrics}"
	}

	assert_recovery_idle() {
		local metrics=$1 collector
		assert_recovery_empty "${metrics}"
		jq -e --argjson collectors "$(printf '%s\n' "${collectors[@]}" | jq -Rsc 'split("\n")[:-1]')" '
			. as $metrics | [$collectors[] | . as $collector |
				[$metrics[] | select(.name == "huatuo_bamai_scrape_collector_success"
					and .labels.collector == $collector) | .value] as $health |
				if $health == [0] or $health == [1] then {collector: $collector, health: $health}
				else error("missing or invalid recovery collector status: " + $collector) end]
		' "${metrics}.json" > "${metrics}.idle.json" || fatal "invalid collector status in idle window: ${metrics}"
		for collector in "${collectors[@]}"; do
			if jq -e --arg collector "${collector}" \
				'any(.[]; .collector == $collector and .health == [1])' "${metrics}.idle.json" > /dev/null; then
				log_info "${collector}: confirmed healthy idle window ${metrics}"
			else
				log_info "NOT_RUN ${collector} idle: success=0 cannot confirm quietness; inspect ${metrics}"
			fi
		done
	}

	recovery_workload() {
		local directory=$1 operation=${2:-write} limit=${3:-25} requests=${4:-4}
		local index output owner worker status=0 signal_status=0
		((limit <= 25)) || limit=25
		recovery_directory=${directory}
		mkdir -p "${directory}"
		record_recovery_stage workload-begin "operation=${operation} requests=${requests} budget=${limit}s directory=${directory}"
		recovery_owned || fatal "scsi_debug ownership changed before workload"
		# Record both children before allowing interruption to reclaim their disks.
		trap 'signal_status=130' INT
		trap 'signal_status=143' TERM
		for index in 0 1; do
			read -r owner < "/sys/block/${disks[index]}/device/vendor"
			[[ ${owner} == "${io_test_scsi_owner}" ]] || fatal "owned disk identity changed: ${disks[index]}"
			output="${directory}/${index}.json"
			cat "/sys/block/${disks[index]}/stat" > "${output}.diskstats"
			timeout --kill-after=2 "${limit}" python3 "${WAIT_HELPER}" workload --device "/dev/${disks[index]}" \
				--cgroup "${leaf}" --operation "${operation}" --requests "${requests}" --output "${output}" > "${output}.log" 2>&1 &
			workers+=("$!")
			record_recovery_stage worker-start "worker=$! device=${disks[index]}"
			printf 'worker=%s device=%s output=%s\n' "$!" "${disks[index]}" "${output}" >> "${RECOVERY_WORK}/resources.txt"
		done
		trap 'exit 130' INT
		trap 'exit 143' TERM
		((signal_status == 0)) || exit "${signal_status}"
		for worker in "${workers[@]}"; do wait "${worker}" || status=$?; done
		record_recovery_stage workload-end "exit=${status} workers=${workers[*]}"
		workers=()
		((status == 0)) || fatal "${recovery_context}: incomplete workload batch=${recovery_attempt} exit=${status} budget=${limit}s; last complete scrape=${recovery_metrics:-none}; inspect ${directory}"
		for index in 0 1; do cat "/sys/block/${disks[index]}/stat" >> "${directory}/${index}.json.diskstats"; done
	}

	assert_recovery_fresh_io() {
		local phase=$1 collector index round deadline attempt metrics summary before after remaining directory
		local batch_started batch_budget elapsed
		local baseline="" requests=4 operation=read confirmed
		[[ ${phase} != initial ]] || operation=write
		if [[ ${phase} == initial && ${#collectors[@]} -gt 1 ]]; then
			requests=8
			recovery_context="${phase}/unlimited/${operation}"
			baseline="${RECOVERY_WORK}/initial-baseline"
			record_recovery_stage controls-begin "baseline=unlimited"
			recovery_disable_controls || fatal "cannot clear controls before baseline IO"
			record_recovery_stage controls-end "baseline=unlimited"
			recovery_workload "${baseline}" "${operation}" 25 "${requests}"
			# This is an input/output reference, not a startup qualification.
			recovery_scrape "${baseline}/sample.metrics" "${operation}"
		fi
		# Qualify latency with unlimited IO so its Q observations do not depend
		# on the kernel's throttle path. Wait collectors use separate limited batches.
		for collector in "${collectors[@]}"; do
			recovery_context="${phase}/${collector}/${operation}"
			record_recovery_stage controls-begin "collector=${collector}"
			recovery_disable_controls || fatal "cannot clear controls before ${collector}/${phase}"
			for index in 0 1; do
				recovery_control "${index}" 1 "${collector}" || fatal "cannot enable ${collector} on ${disks[index]}"
			done
			record_recovery_stage controls-end "collector=${collector}"
			deadline=$((SECONDS + 30))
			attempt=0
			batch_budget=1
			while true; do
				remaining=$((deadline - SECONDS))
				((remaining >= batch_budget)) || fatal "${recovery_context}: qualification incomplete after ${attempt} batches; remaining=${remaining}s next_batch_reference=${batch_budget}s; inspect ${recovery_metrics} and failure.txt"
				attempt=$((attempt + 1))
				recovery_attempt=${attempt}
				batch_started=${SECONDS}
				directory="${RECOVERY_WORK}/${phase}-${collector}-fresh-${attempt}"
				recovery_workload "${directory}" "${operation}" "${remaining}" "${requests}"
				elapsed=$((SECONDS - batch_started + 1))
				metrics="${directory}/sample.metrics"
				recovery_scrape "${metrics}" "${operation}"
				[[ ${phase} == initial ]] || assert_recovery_empty "${metrics}" write
				confirmed=1
				for index in 0 1; do
					if [[ ${collector} != iolatency && -n ${baseline} ]]; then
						assert_io_wait_slowdown "${baseline}/${index}.json" "${directory}/${index}.json" \
							> "${directory}/${index}.timing.json" || fatal "${collector} slowdown was not independently established"
					fi
					if [[ ${collector} == iolatency ]]; then
						jq -e '.ready' "${metrics}.latency-${index}.json" > /dev/null || confirmed=0
					else
						summary="${directory}/${index}.${collector}.json"
						assert_io_wait_metrics --json --collector "${collector}" --disk "${disks[index]}" \
							--operation "${operation}" --container-groups 0 "${metrics}.json" > "${summary}" \
							|| fatal "invalid ${collector} output during ${phase} recovery"
						assert_io_wait_host_positive "${summary}" || confirmed=0
					fi
				done
				((confirmed == 0)) || break
				# Do not start another full batch when its observed runtime no
				# longer fits. A forced partial IO would hide the missing output.
				((elapsed <= batch_budget)) || batch_budget=${elapsed}
			done
			recovery_disable_controls || fatal "cannot restore controls after fresh IO"
			log_info "${collector}/${phase}: same daemon emitted fresh ${operation} ${collector} output on both owned disks"
		done
		recovery_context="${phase}/idle"
		for round in 1 2; do
			before=""
			for index in 0 1; do before+="$(< "/sys/block/${disks[index]}/stat")"; done
			sleep 0.2
			metrics="${RECOVERY_WORK}/${phase}-idle-${round}.metrics"
			recovery_scrape "${metrics}"
			after=""
			for index in 0 1; do after+="$(< "/sys/block/${disks[index]}/stat")"; done
			printf 'before=%s\nafter=%s\n' "${before}" "${after}" > "${metrics}.diskstats"
			[[ ${before} == "${after}" ]] || fatal "owned disks were active; cannot verify recovery idle output"
			assert_recovery_idle "${metrics}"
		done
	}

	recovery_pending_io() {
		local collector index
		for collector in "${collectors[@]}"; do
			recovery_context="pending/${collector}/write"
			record_recovery_stage controls-begin "collector=${collector}"
			recovery_disable_controls || fatal "cannot clear controls before pending ${collector} IO"
			for index in 0 1; do
				recovery_control "${index}" 1 "${collector}" || fatal "cannot enable pending ${collector} IO"
			done
			record_recovery_stage controls-end "collector=${collector}"
			recovery_workload "${RECOVERY_WORK}/${collector}-pending"
		done
		recovery_disable_controls || fatal "cannot clear controls after pending IO"
		# No scrape: completed writes must be discarded by the topology reset.
	}

	recovery_context=daemon-start
	record_recovery_stage begin
	io_test_start_bamai write_config_bamai_io_recovery --region dev --disable-storage --disable-kubelet --log-debug
	daemon_pid=$(< "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")
	record_recovery_stage end "daemon_pid=${daemon_pid}"
	printf 'daemon_pid=%s\n' "${daemon_pid}" >> "${RECOVERY_WORK}/resources.txt"
	# The long-lived daemon must not inherit device descriptors: those references
	# outlive the parent's close and can block SCSI disk reinitialization.
	for index in 0 1; do
		record_recovery_stage disk-lock-begin "disk=${disks[index]}"
		exec {fd}< "/dev/${disks[index]}"
		locks[index]=${fd}
		flock -x -w 10 "${fd}" || fatal "cannot lock owned disk ${disks[index]}"
		record_recovery_stage disk-lock-end "disk=${disks[index]} fd=${fd}"
	done
	# HTTP readiness can precede tracing attachment. Qualify initial output with
	# fresh IO just as after a topology change, without a fixed startup delay.
	assert_recovery_fresh_io initial
	recovery_pending_io
	recovery_context=scsi-delete
	old_disk=${disks[0]}
	printf 'deleted_disk=%s old_number=%s\n' "${old_disk}" "${numbers[0]}" >> "${RECOVERY_WORK}/resources.txt"
	recovery_disable_controls || fatal "cannot release controls before SCSI deletion"
	fd=${locks[0]}
	exec {fd}<&-
	unset 'locks[0]'
	recovery_host_owned || fatal "SCSI host ownership changed before deletion"
	record_recovery_stage begin "address=${scsi_address} disk=${old_disk}"
	recovery_device_status=0
	timeout --kill-after=1 5 bash -c 'printf "1\n" > "/sys/class/scsi_device/$1/device/delete"' _ "${scsi_address}" || recovery_device_status=$?
	record_recovery_stage end "exit=${recovery_device_status} address=${scsi_address}"
	((recovery_device_status == 0)) || exit "${recovery_device_status}"
	recovery_device_absent() {
		[[ ! -e /sys/block/${old_disk} && ! -e /sys/class/scsi_device/${scsi_address} ]]
	}
	wait_until 10 0.1 recovery_device_absent || fatal "owned SCSI device did not disappear after deletion"
	recovery_context=scsi-rescan
	recovery_host_owned || fatal "SCSI host ownership changed before rescan"
	record_recovery_stage begin "host=${scsi_host} scan=${scsi_scan}"
	recovery_device_status=0
	timeout --kill-after=1 5 bash -c 'printf "%s\n" "$2" > "/sys/class/scsi_host/$1/scan"' _ "${scsi_host}" "${scsi_scan}" || recovery_device_status=$?
	record_recovery_stage end "exit=${recovery_device_status} host=${scsi_host}"
	((recovery_device_status == 0)) || exit "${recovery_device_status}"
	rediscovered_disk_ready() {
		local path owner
		for path in /sys/class/scsi_device/"${scsi_address}"/device/block/*; do
			[[ -e ${path} && -b /dev/${path##*/} && -r ${path}/device/vendor && -w ${path}/queue/iostats ]] || continue
			read -r owner < "${path}/device/vendor" || continue
			[[ ${owner} == "${io_test_scsi_owner}" ]] || continue
			disks[0]=${path##*/}
			numbers[0]=$(< "${path}/dev")
			return 0
		done
		return 1
	}
	wait_until 10 0.1 rediscovered_disk_ready || fatal "owned SCSI disk did not return with complete identity"
	io_test_disks[0]=${disks[0]}
	io_test_enable_owned_disk_stats "${disks[0]}" || fatal "rediscovered SCSI disk identity or iostats is not ready"
	record_recovery_stage device-ready "disk=${disks[0]} number=${numbers[0]}"
	exec {fd}< "/dev/${disks[0]}"
	locks[0]=${fd}
	flock -x -w 10 "${fd}" || fatal "cannot lock rediscovered disk ${disks[0]}"
	printf 'rediscovered_disk=%s number=%s\n' "${disks[0]}" "${numbers[0]}" >> "${RECOVERY_WORK}/resources.txt"
	recovery_context=rescan-reset
	rm -f "${RECOVERY_WORK}"/latency-last-interval-*
	recovery_scrape "${RECOVERY_WORK}/rescan-reset.metrics"
	assert_recovery_empty "${RECOVERY_WORK}/rescan-reset.metrics"

	assert_recovery_fresh_io rescan
	log_info "IO recovery (${collectors[*]}): same daemon survived SCSI delete/rescan; fresh reads resumed without replaying old writes or idle statistics"
	exit 0
}

case ${TEST_IO_WAIT_RECOVERY_CASE:-all} in
all) io_test_run_cases "$0" TEST_IO_WAIT_RECOVERY_CASE recovery ;;
recovery) run_io_recovery ;;
*) fatal "TEST_IO_WAIT_RECOVERY_CASE must be all or recovery" ;;
esac
