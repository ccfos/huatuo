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
			((io_test_scsi_owned)) || fatal "IOCOST device control requires an owned disk"
			read -r owner < "/sys/block/${disk}/device/vendor"
			[[ ${owner} == "${io_test_scsi_owner}" ]] || fatal "IOCOST test disk ownership changed"
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
	io_test_prepare_disks 1
	trap 'log_error "IO wait test interrupted; entering cleanup"; exit 130' INT
	trap 'log_error "IO wait test terminated or timed out; entering cleanup"; exit 143' TERM
	((${#io_test_disks[@]})) || skip "IO wait metrics require an eligible block device"
	disk=${io_test_disks[0]}
	device="/dev/${disk}"
	if ((io_test_scsi_owned)); then
		require_commands flock
		# Keep udev's partition/filesystem probes out of the low-rate workload,
		# including change events caused by closing the baseline write handle.
		exec {disk_lock}< "${device}"
		flock -x -w 10 "${disk_lock}" || fatal "cannot lock owned IO test disk ${device}"
	fi
	read -r device_number < "/sys/block/${disk}/dev"
	[[ ${device_number} =~ ^[0-9]+:[0-9]+$ ]] || fatal "invalid public device number: ${device_number}"
	printf 'device=%s\nscsi_debug_owner=%s\n' "${device}" "${io_test_scsi_owner}" >> "${WAIT_WORK}/resources.txt"

	write_wait_config() {
		write_sched_tick_config
		sed "s/\"${collector}\"/\"sched_tick\"/" "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" \
			> "${WAIT_WORK}/config.next"
		mv "${WAIT_WORK}/config.next" "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf"
	}

	run_wait_workload() {
		local operation=$1 output=$2 requests=${3:-16}
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
		local operation=$1 directory=$2 requests=${3:-16} sample sample_path workload_status=0 before after
		mkdir -p "${directory}"
		before=$(< "/sys/block/${disk}/stat")
		huatuo_bamai_metrics > "${directory}/before.metrics"
		after=$(< "/sys/block/${disk}/stat")
		printf '%s\n%s\n' "${before}" "${after}" > "${directory}/before.diskstats"
		run_wait_workload "${operation}" "${directory}/limited.json" "${requests}"
		for ((sample = 0; sample < 52; sample++)); do
			printf -v sample_path '%s/sample-%03d.metrics' "${directory}" "${sample}"
			huatuo_bamai_metrics > "${sample_path}"
			[[ -f ${directory}/limited.json ]] && break
			kill -0 "${worker}" 2> /dev/null || break
			sleep 0.5
		done
		wait "${worker}" || workload_status=$?
		worker=""
		((workload_status == 0)) || fatal "limited ${collector}/${operation} workload failed (${workload_status})"
		huatuo_bamai_metrics > "${directory}/sample-final.metrics"
	}

	assert_replacement_wait_baseline() {
		local directory=$1 status=0
		local -a samples fields
		mapfile -t samples < "${directory}/before.diskstats"
		read -ra fields <<< "${samples[0]}"
		if ((io_test_scsi_owned)) && [[ ${samples[0]} == "${samples[1]}" && ${fields[8]} == 0 ]]; then
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

	exit_active_wait_group() {
		local operation=$1 exit_dir="${case_dir}/active-exit" victim status=0
		mkdir -p "${exit_dir}"
		wait_control 1 "${operation}" || fatal "cannot establish controls for the active-exit workload"
		run_wait_workload "${operation}" "${exit_dir}/limited.json" 128 "${exit_dir}/progress.json"
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
		if ((io_test_scsi_owned)); then
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
			if ((io_test_scsi_owned == 0)) || [[ $(uname -m) != x86_64 ||
			! -w ${cgroup_root}/${control_prefix}.cost.model || ! -w ${cgroup_root}/${control_prefix}.cost.qos ||
			(-r /sys/kernel/realtime && $(< /sys/kernel/realtime) == 1) ]]; then
				log_info "SKIP IOCOST: requires a disposable owned disk, cost controller and non-RT x86_64"
				continue
			fi
		fi
		integration_huatuo_bamai_start write_wait_config --region dev --disable-storage --disable-kubelet --log-debug
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
		operations=(read)
		((io_test_scsi_owned == 0)) || operations+=(write)
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
			if ((io_test_scsi_owned)); then
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
			else
				log_info "NOT_RUN ${collector} strict idle: existing disk is not exclusive"
			fi
			# Both directions check metrics; lifecycle combinations use one direction
			# each. Read-only fixtures retain both checks with the read workload.
			if [[ ${operation} == read ]]; then
				# Recreate only the owned empty leaf; no internal object identity is read.
				io_test_remove_cgroup "${leaf}" || fatal "completed IO retained the owned cgroup ${leaf}"
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
			if [[ ${operation} == write ]] || ((io_test_scsi_owned == 0)); then
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
