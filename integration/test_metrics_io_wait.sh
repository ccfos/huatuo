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

# Check sustained waits, public aggregation and cgroup replacement.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_io.sh"
io_test_isolate_run_directory

run_wait_metrics() {
	set -euo pipefail
	source "${ROOT_DIR}/integration/lib.sh"
	source "${ROOT_DIR}/integration/config.sh"
	source "${ROOT_DIR}/integration/lib_io.sh"
	source "${ROOT_DIR}/integration/lib_io_wait.sh"
	CURL_TIMEOUT=(--connect-timeout 2 --max-time 3)
	WAIT_HUATUO_BAMAI_TIMEOUT=30
	require_commands python3 jq timeout
	[[ ${EUID} -eq 0 ]] || skip "IO wait metrics require root"
	readonly WAIT_WORK="${HUATUO_BAMAI_TEST_TMPDIR}/io-waits"
	readonly WAIT_HELPER="${ROOT_DIR}/integration/testdata/test_io_wait.py"
	mkdir -p "${WAIT_WORK}"
	cgroup_root=/sys/fs/cgroup
	control_prefix=io
	parent=""
	leaf=""
	worker=""
	delegated=0
	cost_enabled=0
	disk=""
	disk_lock=""
	device_number=""
	collector=""
	passed=0
	case ${TEST_IO_WAIT_COLLECTOR:-all} in
	all) collectors=(blk_throtl iocost) ;;
	blk_throtl | iocost) collectors=("${TEST_IO_WAIT_COLLECTOR}") ;;
	*) fatal "TEST_IO_WAIT_COLLECTOR must be all, blk_throtl or iocost" ;;
	esac

	wait_control() {
		local limited=$1 operation=$2 file value owner
		if [[ ${collector} == blk_throtl ]]; then
			if [[ ${control_prefix} == io ]]; then
				file="${leaf}/io.max"
				value="${device_number} ${operation:0:1}iops=$([[ ${limited} == 1 ]] && echo 8 || echo max)"
			else
				file="${leaf}/blkio.throttle.${operation}_iops_device"
				value="${device_number} $([[ ${limited} == 1 ]] && echo 8 || echo 0)"
			fi
			printf '%s\n' "${value}" > "${file}" || return 1
			cat "${file}" > "${WAIT_WORK}/${collector}-${operation}-control-${limited}.txt"
			if [[ ${limited} == 1 ]]; then
				if [[ ${control_prefix} == io ]]; then
					grep -Eq "^${device_number} .*${operation:0:1}iops=8([[:space:]]|$)" "${file}" || return 1
				else
					grep -Eq "^${device_number}[[:space:]]+8$" "${file}" || return 1
				fi
			fi
		else
			assert_io_test_disk_identity "${disk}" || fatal "IOCOST test disk identity changed"
			io_test_save_cost_settings "${cgroup_root}" "${control_prefix}" "${device_number}" || return 1
			if [[ ${limited} == 1 ]]; then
				printf '%s ctrl=user model=linear rbps=32768 rseqiops=8 rrandiops=8 wbps=32768 wseqiops=8 wrandiops=8\n' \
					"${device_number}" > "${cgroup_root}/${control_prefix}.cost.model" || return 1
				cost_enabled=1
				printf '%s enable=1 ctrl=user rpct=0 rlat=0 wpct=0 wlat=0 min=100 max=100\n' \
					"${device_number}" > "${cgroup_root}/${control_prefix}.cost.qos" || return 1
				grep -Eq "^${device_number} .*rbps=32768([[:space:]]|$)" \
					"${cgroup_root}/${control_prefix}.cost.model" || return 1
				grep -Eq "^${device_number} .*enable=1([[:space:]]|$)" \
					"${cgroup_root}/${control_prefix}.cost.qos" || return 1
			else
				printf '%s enable=0\n' "${device_number}" > "${cgroup_root}/${control_prefix}.cost.qos" || return 1
				cost_enabled=0
			fi
			cat "${cgroup_root}/${control_prefix}.cost.model" "${cgroup_root}/${control_prefix}.cost.qos" \
				> "${WAIT_WORK}/${collector}-${operation}-control-${limited}.txt"
		fi
	}

	cleanup_wait_metrics() {
		local status=$? clean=1
		trap - EXIT
		trap '' INT TERM
		set +e
		if ((cost_enabled)); then
			printf '%s enable=0\n' "${device_number}" > "${cgroup_root}/${control_prefix}.cost.qos" || clean=0
		fi
		if [[ -n ${leaf} && -d ${leaf} && ${collector} == blk_throtl ]]; then
			if [[ -w ${parent}/io.max ]]; then
				printf '%s riops=max\n' "${device_number}" > "${parent}/io.max" || clean=0
			fi
			wait_control 0 read || clean=0
			wait_control 0 write || clean=0
		fi
		if [[ -n ${worker} ]]; then
			stop_and_wait_by_pid "${worker}" 3 || true
			if kill -0 "${worker}" 2> /dev/null; then
				log_error "IO wait cleanup left process ${worker}; inspect resources.txt"
				clean=0
			fi
		fi
		huatuo_bamai_stop || clean=0
		if [[ -n ${disk_lock} ]]; then
			exec {disk_lock}<&-
		fi
		io_test_remove_cgroup "${leaf}" || clean=0
		io_test_remove_cgroup "${parent}" || clean=0
		if ((delegated && clean)); then
			printf '%s\n' -io > "${cgroup_root}/cgroup.subtree_control" || clean=0
		fi
		if ((clean)); then
			io_test_cleanup_disks || clean=0
		fi
		if ((clean == 0)); then
			log_error "IO wait cleanup incomplete; retain and inspect ${WAIT_WORK}/resources.txt"
			status=1
		fi
		exit "${status}"
	}
	trap cleanup_wait_metrics EXIT
	trap 'log_error "IO wait test interrupted; entering cleanup"; exit 130' INT
	trap 'log_error "IO wait test terminated or timed out; entering cleanup"; exit 143' TERM

	if [[ ! -f ${cgroup_root}/cgroup.controllers ]]; then
		cgroup_root=/sys/fs/cgroup/blkio
		control_prefix=blkio
		[[ -d ${cgroup_root} ]] || skip "blkio controller is unavailable"
	else
		grep -qw io "${cgroup_root}/cgroup.controllers" || skip "cgroup v2 IO controller is unavailable"
		if ! grep -qw io "${cgroup_root}/cgroup.subtree_control"; then
			delegated=1
			printf 'root_io_delegated=1 root=%s\n' "${cgroup_root}" >> "${WAIT_WORK}/resources.txt"
			printf '%s\n' +io > "${cgroup_root}/cgroup.subtree_control"
		fi
	fi
	read -r owner < /proc/sys/kernel/random/uuid
	parent="${cgroup_root}/huatuo-io-waits-${owner}"
	leaf="${parent}/load"
	printf 'parent=%s\nleaf=%s\nroot_io_delegated=%s\n' "${parent}" "${leaf}" "${delegated}" > "${WAIT_WORK}/resources.txt"
	mkdir "${parent}"
	if [[ ${control_prefix} == io ]]; then
		printf '%s\n' +io > "${parent}/cgroup.subtree_control"
	fi
	mkdir "${leaf}"
	io_test_prepare_disks
	trap 'log_error "IO wait test interrupted; entering cleanup"; exit 130' INT
	trap 'log_error "IO wait test terminated or timed out; entering cleanup"; exit 143' TERM
	disk=${io_test_disks[0]}
	device="/dev/${disk}"
	if ((io_test_disk_exclusive)); then
		require_commands flock
		# Keep udev's partition/filesystem probes out of the low-rate workload,
		# including change events caused by closing the baseline write handle.
		exec {disk_lock}< "${device}"
		flock -x -w 10 "${disk_lock}" || fatal "cannot lock owned IO test disk ${device}"
	fi
	read -r device_number < "/sys/block/${disk}/dev"
	[[ ${device_number} =~ ^[0-9]+:[0-9]+$ ]] || fatal "invalid public device number: ${device_number}"
	printf 'device=%s\nscsi_debug_owner=%s\n' "${device}" "${io_test_scsi_owner}" >> "${WAIT_WORK}/resources.txt"

	write_config_bamai_io_wait() {
		write_config_bamai_sched_tick | sed "s/\"${collector}\"/\"sched_tick\"/"
	}

	run_wait_workload() {
		local operation=$1 output=$2 requests=${3:-8}
		local -a progress=()
		(($# < 4)) || progress=(--progress "$4")
		timeout --kill-after=2 25 python3 "${WAIT_HELPER}" workload --device "${device}" \
			--cgroup "${leaf}" --operation "${operation}" --requests "${requests}" \
			--output "${output}" "${progress[@]}" \
			> "${output}.log" 2>&1 &
		worker=$!
		printf 'worker=%s output=%s\n' "${worker}" "${output}" >> "${WAIT_WORK}/resources.txt"
	}

	collect_wait_workload() {
		local operation=$1 directory=$2 requests=${3:-8} sample sample_path workload_status=0 before after
		mkdir -p "${directory}"
		before=$(< "/sys/block/${disk}/stat")
		huatuo_bamai_metrics > "${directory}/before.metrics"
		after=$(< "/sys/block/${disk}/stat")
		printf '%s\n%s\n' "${before}" "${after}" > "${directory}/before.diskstats"
		run_wait_workload "${operation}" "${directory}/limited.json" "${requests}"
		for ((sample = 0; sample < 260; sample++)); do
			printf -v sample_path '%s/sample-%03d.metrics' "${directory}" "${sample}"
			huatuo_bamai_metrics > "${sample_path}"
			[[ -f ${directory}/limited.json ]] && break
			kill -0 "${worker}" 2> /dev/null || break
			sleep 0.1
		done
		wait "${worker}" || workload_status=$?
		worker=""
		((workload_status == 0)) || fatal "limited ${collector}/${operation} workload failed (${workload_status})"
		huatuo_bamai_metrics > "${directory}/sample-final.metrics"
	}

	set_wait_hierarchy_limits() {
		local parent_iops=$1 leaf_iops=$2 group rate
		printf '%s riops=%s\n' "${device_number}" "${parent_iops}" > "${parent}/io.max" || return 1
		printf '%s riops=%s\n' "${device_number}" "${leaf_iops}" > "${leaf}/io.max" || return 1
		for group in "${parent}" "${leaf}"; do
			rate=${parent_iops}
			[[ ${group} == "${parent}" ]] || rate=${leaf_iops}
			[[ ${rate} == max ]] || grep -Eq "^${device_number} .*riops=${rate}([[:space:]]|$)" "${group}/io.max" || return 1
		done
	}

	run_wait_hierarchy() {
		local directory="${WAIT_WORK}/blk_throtl-hierarchy" phase status=0
		if [[ ${control_prefix} != io ]]; then
			log_info "NOT_RUN parent/child waits: cgroup v2 hierarchy is unavailable"
			return 0
		fi
		mkdir "${directory}"
		set_wait_hierarchy_limits max max || fatal "cannot establish the hierarchy baseline limits"
		run_wait_workload read "${directory}/baseline.json" 8
		wait "${worker}" || status=$?
		worker=""
		((status == 0)) || fatal "hierarchy baseline IO failed (${status})"
		for phase in parent child; do
			# Only the selected level may introduce throttle waits in this phase.
			if [[ ${phase} == parent ]]; then
				set_wait_hierarchy_limits 4 max || fatal "cannot establish parent-dominant IO limits"
			else
				set_wait_hierarchy_limits max 4 || fatal "cannot establish child-dominant IO limits"
			fi
			mkdir -p "${directory}/${phase}"
			cat "${parent}/io.max" "${leaf}/io.max" > "${directory}/${phase}/limits.txt"
			collect_wait_workload read "${directory}/${phase}" 8
			status=0
			assert_io_wait_slowdown "${directory}/baseline.json" "${directory}/${phase}/limited.json" \
				> "${directory}/${phase}/timing.json" || status=$?
			if ((status == 3)); then
				log_info "NOT_RUN ${phase}-dominant waits: external slowdown was not confirmed"
			else
				((status == 0)) || fatal "invalid ${phase}-dominant workload receipt"
				assert_io_wait_metrics --collector blk_throtl --disk "${disk}" --operation read \
					--positive-windows 1 --container-groups 0 --require-other \
					"${directory}/${phase}"/sample-*.metrics > "${directory}/${phase}/summary.json" \
					|| fatal "confirmed ${phase}-dominant IO lacks healthy waiting metrics"
				log_info "PASS ${phase}-dominant waits: confirmed slowdown and public Host/Other output"
			fi
			set_wait_hierarchy_limits max max || fatal "cannot restore hierarchy IO limits"
		done
	}

	run_wait_discard() {
		local directory="${WAIT_WORK}/iocost-discard" operation
		local -a before after
		if ! command -v blkdiscard > /dev/null \
			|| [[ ! -r /sys/block/${disk}/queue/discard_max_bytes ]] \
			|| (($(< "/sys/block/${disk}/queue/discard_max_bytes") == 0)); then
			log_info "NOT_RUN IOCOST discard: blkdiscard or device discard support is unavailable"
			return 0
		fi
		mkdir "${directory}"
		read -ra before < "/sys/block/${disk}/stat"
		wait_control 0 write || fatal "cannot disable IOCOST before discard"
		huatuo_bamai_metrics > "${directory}/before.metrics"
		timeout --kill-after=2s 15s bash -ec '
			printf "%s\n" "$$" > "$1/cgroup.procs"
			exec blkdiscard --offset 0 --length 4096 "$2"
		' io-discard "${leaf}" "${device}" > "${directory}/command.log" 2>&1 \
			|| fatal "discard failed on the owned IOCOST disk; inspect ${directory}/command.log"
		read -ra after < "/sys/block/${disk}/stat"
		printf '%s\n%s\n' "${before[*]}" "${after[*]}" > "${directory}/diskstats"
		((${#before[@]} >= 15 && ${#after[@]} >= 15 && after[11] > before[11])) \
			|| fatal "successful discard did not increase the owned disk discard count"
		huatuo_bamai_metrics > "${directory}/after.metrics"
		for operation in read write; do
			assert_io_wait_metrics --collector iocost --disk "${disk}" --operation "${operation}" \
				--container-groups 0 --idle "${directory}/after.metrics" \
				> "${directory}/${operation}-summary.json" \
				|| fatal "discard produced ${operation} waits or unhealthy IOCOST output"
		done
		log_info "IOCOST: independently confirmed discard does not produce read/write waits"
	}

	assert_replacement_wait_baseline() {
		local directory=$1 status=0
		local -a samples fields
		mapfile -t samples < "${directory}/before.diskstats"
		read -ra fields <<< "${samples[0]}"
		if ((io_test_disk_exclusive)) && [[ ${samples[0]} == "${samples[1]}" && ${fields[8]} == 0 ]]; then
			assert_io_wait_metrics --collector "${collector}" --disk "${disk}" \
				--operation "${operation}" --idle "${directory}/before.metrics" \
				> "${directory}/before-summary.json" || status=$?
			((status != 3)) || log_info "NOT_RUN W07 pre-load isolation: collector had no confirmed empty window"
			((status == 0 || status == 3)) || fatal "new cgroup inherited waits before its first IO"
			((status != 0)) || log_info "W07 ${collector}/${operation}: replacement baseline contains no old waits"
		else
			log_info "NOT_RUN W07 pre-load isolation: target disk was not independently quiet"
		fi
	}

	wait_workload_active() {
		local process
		[[ -s ${exit_dir}/progress.json && ! -e ${exit_dir}/limited.json ]] || return 1
		process=$(jq -e -r 'select(.completed > 0 and .completed < .requests) | .pid' \
			"${exit_dir}/progress.json") || return 1
		[[ ${process} =~ ^[1-9][0-9]*$ ]] || return 1
		grep -Fxq "${process}" "${leaf}/cgroup.procs" || return 1
		kill -0 "${process}" 2> /dev/null
	}

	wait_exit_disk_idle() {
		local before after
		local -a fields
		before=$(< "/sys/block/${disk}/stat")
		read -ra fields <<< "${before}"
		[[ ${fields[8]} == 0 ]] || return 1
		sleep 0.1
		after=$(< "/sys/block/${disk}/stat")
		[[ ${before} == "${after}" ]]
	}

	record_wait_retirement_snapshot() {
		local directory=$1 label=$2 started finished stat_line="" inflight_line="" stat_status=0 inflight_status=0
		started=$(date +%s.%N)
		IFS= read -r stat_line < "/sys/block/${disk}/stat" 2> /dev/null || stat_status=$?
		IFS= read -r inflight_line < "/sys/block/${disk}/inflight" 2> /dev/null || inflight_status=$?
		finished=$(date +%s.%N)
		jq -n --arg snapshot "${label}" --arg disk "${disk}" --arg started "${started}" --arg finished "${finished}" \
			--arg stat "${stat_line}" --arg inflight "${inflight_line}" \
			--argjson stat_status "${stat_status}" --argjson inflight_status "${inflight_status}" '
		{snapshot: $snapshot, disk: $disk, capture_started: $started, capture_finished: $finished,
		 stat_raw: $stat, stat: [$stat | scan("[0-9]+") | tonumber], stat_read_exit: $stat_status,
		 inflight_raw: $inflight, inflight: [$inflight | scan("[0-9]+") | tonumber], inflight_read_exit: $inflight_status}
	' > "${directory}/snapshot-${label}.json" || log_error "cannot save retirement snapshot ${label}: ${directory}"
	}

	report_wait_retirement() {
		local directory=$1 operation=$2 label file
		local -a snapshots=()
		log_error "W07 retirement observations: collector=${collector} disk=${disk} operation=${operation}"
		for label in A B C D; do
			file="${directory}/snapshot-${label}.json"
			[[ -s ${file} ]] || continue
			snapshots+=("${file}")
			jq -c . "${file}" >&2 || true
		done
		if ((${#snapshots[@]})); then
			# stat's ninth field is a gauge; report its endpoints instead of a delta.
			jq -s 'map({key: .snapshot, value: .}) | from_entries as $snapshots
			| [["first", "A", "B"], ["between", "B", "C"], ["next", "C", "D"]]
			| map(. as $window | $snapshots[.[1]] as $a | $snapshots[.[2]] as $b
				| if $a == null or $b == null or $a.stat_read_exit != 0 or $b.stat_read_exit != 0
					or ($a.stat | length) < 11 or ($a.stat | length) != ($b.stat | length)
				then {window: $window[0], stat_available: false}
				else {window: $window[0], stat_available: true,
					read_ios_delta: ($b.stat[0] - $a.stat[0]), write_ios_delta: ($b.stat[4] - $a.stat[4]),
					stat_inflight: [$a.stat[8], $b.stat[8]], inflight: [$a.inflight, $b.inflight],
					stat_counter_delta: [range(0; $a.stat | length) as $i
						| if $i == 8 then null else $b.stat[$i] - $a.stat[$i] end]} end)
		' "${snapshots[@]}" > "${directory}/window-deltas.json"
			jq -c '.[]' "${directory}/window-deltas.json" >&2 || true
		fi
		for file in "${directory}"/{workload-completed.txt,limited.json,limited.json.started,timing.json,first.metrics,next.metrics}; do
			[[ -s ${file} ]] || continue
			printf '%s:\n' "${file}" >&2
			case ${file} in
			*.metrics)
				awk -v collector="${collector}" -v disk="${disk}" -v operation="${operation}" '
				$1 ~ ("^huatuo_bamai_" collector "_") && index($1, "disk=\"" disk "\"") &&
					index($1, "operation=\"" operation "\"") {print}
				/^huatuo_bamai_scrape_collector_success\{/ && index($1, "collector=\"" collector "\"") {print}
			' "${file}" >&2
				;;
			*) cat "${file}" >&2 ;;
			esac
		done
	}

	retire_completed_wait_group() {
		local operation=$1 directory="${case_dir}/retired-tail" status=0
		mkdir "${directory}"
		wait_control 1 "${operation}" || fatal "cannot limit the retirement workload"
		huatuo_bamai_metrics > "${directory}/before.metrics"
		# Leave completed waits uncollected until their cgroup has been removed.
		run_wait_workload "${operation}" "${directory}/limited.json" 8
		wait "${worker}" || status=$?
		worker=""
		printf 'time=%s workload_exit=%s\n' "$(date +%s.%N)" "${status}" > "${directory}/workload-completed.txt"
		((status == 0)) || fatal "retirement workload failed (${status}); inspect ${directory}"
		assert_io_wait_slowdown "${case_dir}/baseline.json" "${directory}/limited.json" \
			> "${directory}/timing.json" \
			|| fatal "retirement workload did not establish a controlled slowdown; inspect ${directory}"
		io_test_remove_cgroup "${leaf}" || fatal "completed IO retained the owned cgroup ${leaf}"
		if ((io_test_disk_exclusive)); then
			wait_until 5 0.1 wait_exit_disk_idle || fatal "owned disk did not drain after completed cgroup exit"
		fi
		record_wait_retirement_snapshot "${directory}" A
		status=0
		huatuo_bamai_metrics > "${directory}/first.metrics" || status=$?
		record_wait_retirement_snapshot "${directory}" B
		if ((status != 0)); then
			report_wait_retirement "${directory}" "${operation}" || true
			return "${status}"
		fi
		assert_io_wait_metrics --collector "${collector}" --disk "${disk}" \
			--operation "${operation}" --positive-windows 1 --container-groups 0 --require-other \
			"${directory}/first.metrics" > "${directory}/first-summary.json" \
			|| {
				report_wait_retirement "${directory}" "${operation}" || true
				fatal "completed waits disappeared after cgroup removal; inspect ${directory}"
			}
		record_wait_retirement_snapshot "${directory}" C
		status=0
		huatuo_bamai_metrics > "${directory}/next.metrics" || status=$?
		record_wait_retirement_snapshot "${directory}" D
		if ((status != 0)); then
			report_wait_retirement "${directory}" "${operation}" || true
			return "${status}"
		fi
		assert_io_wait_metrics --collector "${collector}" --disk "${disk}" \
			--operation "${operation}" --container-groups 0 --idle "${directory}/next.metrics" \
			> "${directory}/next-summary.json" \
			|| {
				report_wait_retirement "${directory}" "${operation}" || true
				fatal "post-retirement interval repeated waits or was unhealthy; inspect ${directory}"
			}
		log_info "W07 ${collector}/${operation}: uncollected completed waits survived cgroup removal and did not repeat"
	}

	exit_active_wait_group() {
		local operation=$1 exit_dir="${case_dir}/active-exit" victim status=0
		mkdir -p "${exit_dir}"
		wait_control 1 "${operation}" || fatal "cannot establish controls for the active-exit workload"
		run_wait_workload "${operation}" "${exit_dir}/limited.json" 16 "${exit_dir}/progress.json"
		wait_until 8 0.1 wait_workload_active || fatal "cannot confirm an unfinished IO workload before cgroup exit"
		cp "${exit_dir}/progress.json" "${exit_dir}/before-kill.json"
		victim=$(< "${exit_dir}/limited.json.started")
		grep -Fxq "${victim}" "${leaf}/cgroup.procs" || fatal "active-exit process left its owned cgroup"
		[[ ! -e ${exit_dir}/limited.json ]] || fatal "active-exit workload finished before termination"
		kill -KILL "${victim}" || fatal "cannot terminate the owned active IO process ${victim}"
		# Release any already-submitted IO so cancellation cannot hold cleanup behind
		# the test's own limit. The termination request precedes this release.
		wait_control 0 "${operation}" || fatal "cannot release controls after terminating the workload"
		wait "${worker}" || status=$?
		worker=""
		((status == 137)) || fatal "active IO termination exited ${status}, expected SIGKILL (137)"
		io_test_remove_cgroup "${leaf}" || fatal "cannot retire the cgroup after active workload exit"
		if ((io_test_disk_exclusive)); then
			wait_until 5 0.1 wait_exit_disk_idle || fatal "owned disk did not drain after active cgroup exit"
		fi
		# The first scrape may contain the permitted exit tail. Only a following
		# interval is used to detect old waits leaking into the replacement group.
		huatuo_bamai_metrics > "${exit_dir}/retired-tail.metrics"
		mkdir "${leaf}"
		wait_control 1 "${operation}" || fatal "cannot limit the replacement cgroup"
		collect_wait_workload "${operation}" "${exit_dir}/replacement" 8
		assert_replacement_wait_baseline "${exit_dir}/replacement"
		assert_io_wait_metrics --collector "${collector}" --disk "${disk}" \
			--operation "${operation}" --positive-windows 1 --container-groups 0 --require-other \
			"${exit_dir}/replacement"/sample-*.metrics > "${exit_dir}/replacement/summary.json" \
			|| fatal "waiting output did not recover after active cgroup exit"
		wait_control 0 "${operation}" || fatal "cannot restore controls after active cgroup exit"
		log_info "W07 ${collector}/${operation}: unfinished workload exited, cgroup retired and replacement IO recovered"
	}

	for collector in "${collectors[@]}"; do
		if [[ ${collector} == blk_throtl && ! -w ${leaf}/io.max &&
			! -w ${leaf}/blkio.throttle.read_iops_device ]]; then
			log_info "SKIP blk_throtl: throttle controls are unavailable in the owned cgroup"
			continue
		fi
		if [[ ${collector} == iocost ]]; then
			if ((io_test_disk_exclusive == 0)) || [[ ! -w ${cgroup_root}/${control_prefix}.cost.model ||
				! -w ${cgroup_root}/${control_prefix}.cost.qos ||
				(-r /sys/kernel/realtime && $(< /sys/kernel/realtime) == 1) ]]; then
				log_info "SKIP IOCOST: requires a disposable owned disk, cost controller and non-RT kernel"
				continue
			fi
		fi
		io_test_start_bamai write_config_bamai_io_wait --region dev --disable-storage --disable-kubelet --log-debug
		# The HTTP listener can be ready before the IO probes. Completed small
		# batches and positive public output qualify startup before interval checks.
		wait_control 1 read
		deadline=$((SECONDS + 30))
		attempt=0
		while true; do
			attempt=$((attempt + 1))
			warmup_dir="${WAIT_WORK}/${collector}-warmup-${attempt}"
			collect_wait_workload read "${warmup_dir}" 8
			assert_io_wait_metrics --collector "${collector}" --disk "${disk}" --operation read \
				--container-groups 0 "${warmup_dir}"/sample-*.metrics > "${warmup_dir}/summary.json" \
				|| fatal "invalid ${collector} output during startup qualification"
			if assert_io_wait_host_positive "${warmup_dir}/summary.json"; then
				break
			fi
			((SECONDS < deadline)) || fatal "${collector} did not produce waits from completed IO within 30s"
		done
		wait_control 0 read
		if [[ ${collector} == iocost ]]; then
			run_wait_discard
		else
			run_wait_hierarchy
		fi
		operations=(read write)
		for operation in "${operations[@]}"; do
			case_dir="${WAIT_WORK}/${collector}-${operation}"
			mkdir -p "${case_dir}"
			wait_control 0 "${operation}"
			run_wait_workload "${operation}" "${case_dir}/baseline.json"
			workload_status=0
			wait "${worker}" || workload_status=$?
			worker=""
			((workload_status == 0)) || fatal "unlimited ${collector}/${operation} workload failed (${workload_status})"
			wait_control 1 "${operation}"
			collect_wait_workload "${operation}" "${case_dir}"
			assert_io_wait_metrics --collector "${collector}" --disk "${disk}" \
				--operation "${operation}" --container-groups 0 --idle "${case_dir}/before.metrics" \
				> "${case_dir}/unlimited-summary.json" \
				|| fatal "unlimited ${collector}/${operation} IO produced waits or unhealthy collector output"
			timing_status=0
			assert_io_wait_slowdown "${case_dir}/baseline.json" "${case_dir}/limited.json" \
				> "${case_dir}/timing.json" || timing_status=$?
			if ((timing_status == 3)); then
				log_info "SKIP ${collector}/${operation}: external control effect was not established; see ${case_dir}"
				wait_control 0 "${operation}"
				continue
			fi
			((timing_status == 0)) || fatal "cannot validate ${collector}/${operation} workload evidence"
			assert_io_wait_metrics --collector "${collector}" --disk "${disk}" \
				--operation "${operation}" --positive-windows 2 --container-groups 0 --require-other \
				"${case_dir}"/sample-*.metrics > "${case_dir}/summary.json" \
				|| fatal "confirmed sustained ${collector}/${operation} waits violate the public metric contract"
			wait_control 0 "${operation}"
			huatuo_bamai_metrics > "${case_dir}/drain.metrics"
			quiet_intervals=0
			for ((quiet = 1; quiet <= 10 && quiet_intervals < 2; quiet++)); do
				before=$(< "/sys/block/${disk}/stat")
				sleep 0.3
				huatuo_bamai_metrics > "${case_dir}/idle-${quiet}.metrics"
				after=$(< "/sys/block/${disk}/stat")
				[[ ${before} == "${after}" ]] || continue
				idle_status=0
				assert_io_wait_metrics --collector "${collector}" --disk "${disk}" \
					--operation "${operation}" --idle "${case_dir}/idle-${quiet}.metrics" \
					|| idle_status=$?
				((idle_status != 3)) || continue
				((idle_status == 0)) || fatal "${collector} repeated waits after workload completion"
				quiet_intervals=$((quiet_intervals + 1))
			done
			((quiet_intervals == 2)) || log_info "NOT_RUN ${collector} strict idle: two healthy, quiet disk intervals were not confirmed"
			# Read and write use separate cgroup lifecycle scenarios.
			if [[ ${operation} == read ]]; then
				# Recreate only the owned empty leaf; no internal object identity is read.
				retire_completed_wait_group "${operation}"
				mkdir "${leaf}"
				wait_control 1 "${operation}"
				collect_wait_workload "${operation}" "${case_dir}/recreated" 8
				assert_replacement_wait_baseline "${case_dir}/recreated"
				assert_io_wait_metrics --collector "${collector}" --disk "${disk}" \
					--operation "${operation}" --positive-windows 1 --container-groups 0 --require-other \
					"${case_dir}/recreated"/sample-*.metrics > "${case_dir}/recreated/summary.json" \
					|| fatal "${collector} did not resume after replacing the completed cgroup"
				wait_control 0 "${operation}"
				log_info "W07 ${collector}/${operation}: completed cgroup replaced and IO resumed"
			fi
			if [[ ${operation} == write ]]; then
				exit_active_wait_group "${operation}"
			fi
			passed=$((passed + 1))
			log_info "PASS ${collector}/${operation}: W01/W03 sustained waits and Host=Other"
		done
		huatuo_bamai_stop
		cp "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${WAIT_WORK}/${collector}.log"
	done
	((passed > 0)) || skip "neither IO waiting capability established a valid external workload"
	exit 0
}

case ${TEST_IO_WAIT_METRICS_CASE:-all} in
all) io_test_run_cases "$0" TEST_IO_WAIT_METRICS_CASE metrics ;;
metrics) run_wait_metrics ;;
*) fatal "TEST_IO_WAIT_METRICS_CASE must be all or metrics" ;;
esac
