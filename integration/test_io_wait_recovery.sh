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

# Check same-daemon IO recovery after owned-device topology changes.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_io.sh"

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
	worker=""
	delegated=0
	disks=()
	numbers=()
	locks=()
	scsi_address=""
	bind_pending=0
	collectors=()

	recovery_owned() {
		local owner
		((io_test_scsi_owned)) || return 1
		read -r owner < /sys/module/scsi_debug/parameters/inq_vendor || return 1
		[[ ${owner} == "${io_test_scsi_owner}" ]]
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
				printf '%s riops=%s wiops=%s\n' "${numbers[index]}" "${value}" "${value}" > "${leaf}/io.max" || return 1
				cat "${leaf}/io.max" >> "${RECOVERY_WORK}/controls.txt"
			else
				for operation in read write; do
					printf '%s %s\n' "${numbers[index]}" "${enabled/1/8}" \
						> "${leaf}/blkio.throttle.${operation}_iops_device" || return 1
					cat "${leaf}/blkio.throttle.${operation}_iops_device" >> "${RECOVERY_WORK}/controls.txt"
				done
			fi
		else
			if ((enabled)); then
				printf '%s ctrl=user model=linear rbps=32768 rseqiops=8 rrandiops=8 wbps=32768 wseqiops=8 wrandiops=8\n' \
					"${numbers[index]}" > "${cgroup_root}/${control_prefix}.cost.model" || return 1
			fi
			printf '%s enable=%s ctrl=user rpct=0 rlat=0 wpct=0 wlat=0 min=100 max=100\n' \
				"${numbers[index]}" "${enabled}" > "${cgroup_root}/${control_prefix}.cost.qos" || return 1
			cat "${cgroup_root}/${control_prefix}.cost.model" "${cgroup_root}/${control_prefix}.cost.qos" \
				>> "${RECOVERY_WORK}/controls.txt"
		fi
	}

	recovery_disable_controls() {
		local index collector status=0
		for index in "${!disks[@]}"; do
			[[ -e /sys/block/${disks[index]} ]] || continue
			for collector in "${collectors[@]}"; do
				recovery_control "${index}" 0 "${collector}" || status=1
			done
		done
		return "${status}"
	}

	cleanup_io_recovery() {
		local status=$? clean=1 fd
		trap - EXIT
		trap '' INT TERM
		set +e
		recovery_disable_controls || clean=0
		[[ -z ${worker} ]] || stop_and_wait_by_pid "${worker}" 3
		if [[ -n ${worker} ]] && kill -0 "${worker}" 2> /dev/null; then
			log_error "IO recovery cleanup retained workload ${worker}"
			clean=0
		fi
		huatuo_bamai_stop || clean=0
		for fd in "${locks[@]}"; do exec {fd}<&-; done
		if ((bind_pending)) && [[ ! -e /sys/bus/scsi/drivers/sd/${scsi_address} ]]; then
			recovery_owned && timeout --kill-after=1 5 bash -c 'printf "%s" "$1" > /sys/bus/scsi/drivers/sd/bind' _ "${scsi_address}" || clean=0
		fi
		io_test_remove_cgroup "${leaf}" || clean=0
		io_test_remove_cgroup "${parent}" || clean=0
		if ((delegated && clean)); then
			printf '%s\n' -io > "${cgroup_root}/cgroup.subtree_control" || clean=0
		fi
		if ((clean)); then io_test_cleanup_disks || clean=0; fi
		if ((clean == 0)); then
			log_error "IO recovery cleanup incomplete; inspect ${RECOVERY_WORK}/resources.txt and io-disks.resources"
			status=1
		fi
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
	io_test_prepare_disks 3
	((io_test_scsi_owned && ${#io_test_disks[@]} == 3)) || skip "IO recovery requires three owned scsi_debug disks"
	[[ -w /sys/bus/scsi/drivers/sd/unbind && -w /sys/bus/scsi/drivers/sd/bind ]] \
		|| skip "SCSI driver rebind controls are unavailable"
	if [[ ${control_prefix} == io ]] && grep -qw io "${cgroup_root}/cgroup.controllers" \
		&& ! grep -qw io "${cgroup_root}/cgroup.subtree_control"; then
		delegated=1
		printf '%s\n' +io > "${cgroup_root}/cgroup.subtree_control"
	fi
	parent="${cgroup_root}/huatuo-io-recovery-$(< /proc/sys/kernel/random/uuid)"
	leaf="${parent}/load"
	printf 'parent=%s\nleaf=%s\nowner=%s\ndisks=%s\n' "${parent}" "${leaf}" \
		"${io_test_scsi_owner}" "${io_test_disks[*]}" > "${RECOVERY_WORK}/resources.txt"
	mkdir "${parent}"
	if [[ ${control_prefix} == io ]] && grep -qw io "${cgroup_root}/cgroup.subtree_control"; then
		printf '%s\n' +io > "${parent}/cgroup.subtree_control"
	fi
	mkdir "${leaf}"
	if [[ -w ${cgroup_root}/${control_prefix}.cost.model && -w ${cgroup_root}/${control_prefix}.cost.qos &&
		$(uname -m) == x86_64 && (! -r /sys/kernel/realtime || $(< /sys/kernel/realtime) != 1) ]]; then
		collectors+=(iocost)
	else
		log_info "NOT_RUN IOCOST recovery: writable cost controls and non-RT x86_64 are required"
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

	write_recovery_config() {
		local collector
		local -a substitutions=()
		write_sched_tick_config
		for collector in "${collectors[@]}"; do
			substitutions+=(-e "s/\"${collector}\"/\"sched_tick\"/")
		done
		sed "${substitutions[@]}" "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" > "${RECOVERY_WORK}/config.next"
		mv "${RECOVERY_WORK}/config.next" "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf"
	}

	assert_recovery_same_daemon() {
		local pid
		pid=$(< "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")
		[[ ${pid} == "${daemon_pid}" ]] && kill -0 "${pid}" 2> /dev/null \
			|| fatal "IO daemon exited or was replaced during recovery"
	}

	recovery_scrape() {
		local status=0
		assert_recovery_same_daemon
		scrape_started=$(date +%s.%N)
		huatuo_bamai_metrics > "$1" || fatal "IO recovery HTTP request failed: $1"
		scrape_ended=$(date +%s.%N)
		assert_iolatency_metrics "${2:-${disks[0]}}" "${3:-read}" drain "$1" "${scrape_started}" "${scrape_ended}" \
			"${RECOVERY_WORK}/latency-last-interval" || status=$?
		((status == 0 || status == 3)) || fatal "IOLatency recovery output violates the public contract: $1"
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
		local index=$1 output=$2 operation=${3:-write} limit=${4:-25} status=0 owner
		((limit <= 25)) || limit=25
		recovery_owned || fatal "scsi_debug ownership changed before workload"
		read -r owner < "/sys/block/${disks[index]}/device/vendor"
		[[ ${owner} == "${io_test_scsi_owner}" ]] || fatal "owned disk identity changed: ${disks[index]}"
		cat "/sys/block/${disks[index]}/stat" > "${output}.diskstats"
		timeout --kill-after=2 "${limit}" python3 "${WAIT_HELPER}" workload --device "/dev/${disks[index]}" \
			--cgroup "${leaf}" --operation "${operation}" --requests 8 --output "${output}" > "${output}.log" 2>&1 &
		worker=$!
		printf 'worker=%s device=%s output=%s\n' "${worker}" "${disks[index]}" "${output}" >> "${RECOVERY_WORK}/resources.txt"
		wait "${worker}" || status=$?
		worker=""
		((status == 0)) || fatal "IO recovery workload failed (${status}): ${output}"
		cat "/sys/block/${disks[index]}/stat" >> "${output}.diskstats"
	}

	assert_recovery_fresh_io() {
		local phase=$1 collector index round deadline attempt metrics summary before after remaining operation=read
		[[ ${phase} != initial ]] || operation=write
		# Fresh positive output qualifies recovery. Empty transitional scrapes alone
		# cannot establish it; post-change reads also expose any replayed old writes.
		for collector in "${collectors[@]}"; do
			recovery_disable_controls || fatal "cannot clear controls before ${collector}/${phase}"
			for index in 0 1; do
				recovery_control "${index}" 1 "${collector}" || fatal "cannot enable ${collector} on ${disks[index]}"
				deadline=$((SECONDS + 30))
				attempt=0
				while true; do
					attempt=$((attempt + 1))
					remaining=$((deadline - SECONDS))
					((remaining > 0)) || fatal "${collector} recovery deadline reached for ${disks[index]} after ${phase}"
					recovery_workload "${index}" "${RECOVERY_WORK}/${phase}-${collector}-fresh-${index}-${attempt}.json" \
						"${operation}" "${remaining}"
					metrics="${RECOVERY_WORK}/${phase}-${collector}-fresh-${index}-${attempt}.metrics"
					recovery_scrape "${metrics}" "${disks[index]}" "${operation}"
					[[ ${phase} == initial ]] || assert_recovery_empty "${metrics}" write
					if [[ ${collector} == iolatency ]]; then
						if jq -e '.ready' "${metrics}.assertion.json" > /dev/null; then break; fi
					else
						summary="${metrics}.${collector}.json"
						assert_io_wait_metrics --collector "${collector}" --disk "${disks[index]}" --operation "${operation}" \
							--container-groups 0 "${metrics}" > "${summary}" || fatal "invalid ${collector} output during ${phase} recovery"
						if assert_io_wait_host_positive "${summary}"; then break; fi
					fi
					((SECONDS < deadline)) || fatal "same daemon did not resume ${collector} output for ${disks[index]} after ${phase}"
				done
				recovery_control "${index}" 0 "${collector}" || fatal "cannot restore ${collector} after fresh IO"
			done
			log_info "${collector}/${phase}: same daemon emitted fresh ${operation} IO on both owned disks"
		done
		for round in 1 2; do
			before=""
			for index in 0 1; do before+="$(< "/sys/block/${disks[index]}/stat")"; done
			sleep 0.2
			recovery_scrape "${RECOVERY_WORK}/${phase}-idle-${round}.metrics"
			after=""
			for index in 0 1; do after+="$(< "/sys/block/${disks[index]}/stat")"; done
			printf 'before=%s\nafter=%s\n' "${before}" "${after}" > "${RECOVERY_WORK}/${phase}-idle-${round}.diskstats"
			[[ ${before} == "${after}" ]] || fatal "owned disks were active; cannot verify recovery idle output"
			assert_recovery_idle "${RECOVERY_WORK}/${phase}-idle-${round}.metrics"
		done
	}

	recovery_pending_io() {
		local phase=$1 index collector
		for collector in "${collectors[@]}"; do
			recovery_disable_controls || fatal "cannot clear controls before pending ${collector} IO"
			for index in 0 1; do
				recovery_control "${index}" 1 "${collector}" || fatal "cannot enable pending ${collector} IO"
				recovery_workload "${index}" "${RECOVERY_WORK}/${phase}-${collector}-pending-${index}.json"
			done
		done
		recovery_disable_controls || fatal "cannot clear controls after pending IO"
		# No scrape: completed writes must be discarded by the topology reset.
	}

	integration_huatuo_bamai_start write_recovery_config --region dev --disable-storage --disable-kubelet --log-debug
	daemon_pid=$(< "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")
	printf 'daemon_pid=%s\n' "${daemon_pid}" >> "${RECOVERY_WORK}/resources.txt"
	# The long-lived daemon must not inherit device descriptors: those references
	# outlive the parent's close and can block SCSI disk reinitialization.
	for index in 0 1; do
		exec {fd}< "/dev/${disks[index]}"
		locks[index]=${fd}
		flock -x -w 10 "${fd}" || fatal "cannot lock owned disk ${disks[index]}"
	done
	for collector in "${collectors[@]}"; do
		[[ ${collector} != iolatency ]] || continue
		recovery_disable_controls || fatal "cannot disable controls before ${collector} baseline"
		for index in 0 1; do
			recovery_workload "${index}" "${RECOVERY_WORK}/${collector}-baseline-${index}.json"
			recovery_control "${index}" 1 "${collector}"
			recovery_workload "${index}" "${RECOVERY_WORK}/${collector}-limited-${index}.json"
			assert_io_wait_slowdown "${RECOVERY_WORK}/${collector}-baseline-${index}.json" \
				"${RECOVERY_WORK}/${collector}-limited-${index}.json" > "${RECOVERY_WORK}/${collector}-timing-${index}.json" \
				|| fatal "${collector} slowdown was not independently established"
		done
	done
	# HTTP readiness can precede tracing attachment. Qualify initial output with
	# fresh IO just as after a topology change, without a fixed startup delay.
	assert_recovery_fresh_io initial
	recovery_pending_io topology
	extra=${io_test_disks[2]}
	recovery_owned || fatal "scsi_debug ownership changed before disk removal"
	[[ $(< "/sys/block/${extra}/device/vendor") == "${io_test_scsi_owner}" ]] || fatal "extra disk ownership changed"
	printf 'topology_removed=%s\n' "${extra}" >> "${RECOVERY_WORK}/resources.txt"
	timeout --kill-after=1 5 bash -c 'printf "1\n" > "$1"' _ "/sys/block/${extra}/device/delete"
	wait_until 10 0.1 test ! -e "/sys/block/${extra}" || fatal "owned topology disk was not removed"
	rm -f "${RECOVERY_WORK}/latency-last-interval"
	recovery_scrape "${RECOVERY_WORK}/topology-reset.metrics"
	assert_recovery_empty "${RECOVERY_WORK}/topology-reset.metrics"
	assert_recovery_fresh_io topology

	recovery_pending_io rebind
	old_disk=${disks[0]}
	scsi_address=$(basename "$(readlink -f "/sys/block/${old_disk}/device")")
	printf 'rebind_address=%s old_disk=%s old_number=%s\n' "${scsi_address}" "${old_disk}" "${numbers[0]}" >> "${RECOVERY_WORK}/resources.txt"
	recovery_disable_controls || fatal "cannot release controls before SCSI unbind"
	fd=${locks[0]}
	exec {fd}<&-
	unset 'locks[0]'
	bind_pending=1
	timeout --kill-after=1 5 bash -c 'printf "%s" "$1" > /sys/bus/scsi/drivers/sd/unbind' _ "${scsi_address}"
	wait_until 10 0.1 test ! -e "/sys/block/${old_disk}" || fatal "owned SCSI disk did not unbind"
	timeout --kill-after=1 5 bash -c 'printf "%s" "$1" > /sys/bus/scsi/drivers/sd/bind' _ "${scsi_address}"
	bind_pending=0
	rebound_disk_ready() {
		local path owner
		for path in /sys/class/scsi_device/"${scsi_address}"/device/block/*; do
			[[ -e ${path} && -b /dev/${path##*/} && -r ${path}/device/vendor ]] || continue
			read -r owner < "${path}/device/vendor" || continue
			[[ ${owner} == "${io_test_scsi_owner}" ]] || continue
			disks[0]=${path##*/}
			numbers[0]=$(< "${path}/dev")
			return 0
		done
		return 1
	}
	wait_until 10 0.1 rebound_disk_ready || fatal "owned SCSI disk did not return with complete identity"
	exec {fd}< "/dev/${disks[0]}"
	locks[0]=${fd}
	flock -x -w 10 "${fd}" || fatal "cannot lock rebound disk ${disks[0]}"
	printf 'rebound_disk=%s number=%s\n' "${disks[0]}" "${numbers[0]}" >> "${RECOVERY_WORK}/resources.txt"
	rm -f "${RECOVERY_WORK}/latency-last-interval"
	recovery_scrape "${RECOVERY_WORK}/rebind-reset.metrics"
	assert_recovery_empty "${RECOVERY_WORK}/rebind-reset.metrics"

	assert_recovery_fresh_io rebind
	log_info "IO recovery (${collectors[*]}): same daemon survived disk removal and SCSI rebind; fresh reads resumed without replaying old writes or idle statistics"
	exit 0
}

case ${TEST_IO_WAIT_RECOVERY_CASE:-all} in
all) io_test_run_cases "$0" TEST_IO_WAIT_RECOVERY_CASE recovery ;;
recovery) run_io_recovery ;;
*) fatal "TEST_IO_WAIT_RECOVERY_CASE must be all or recovery" ;;
esac
