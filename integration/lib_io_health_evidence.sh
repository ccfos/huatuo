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

# Share owned evidence fixtures and command receipts across health scenarios.
set -euo pipefail

io_health_evidence_init() {
	evidence_protocol=$1
	evidence_tool=${evidence_protocol/scsi/smartctl}
	CURL_TIMEOUT=(--connect-timeout 2 --max-time 3)
	WAIT_HUATUO_BAMAI_TIMEOUT=30
	require_commands awk dd jq ps stat mdadm losetup truncate mount umount mountpoint findmnt python3 timeout udevadm flock readlink setsid
	require_readable /proc/mdstat /proc/sys/kernel/random/uuid
	[[ ${EUID} -eq 0 ]] || skip "IO health evidence requires root"
	readonly EVIDENCE_WORK="${HUATUO_BAMAI_TEST_TMPDIR}/evidence"
	readonly EVIDENCE_STORE="${HUATUO_BAMAI_TEST_TMPDIR}/events"
	readonly EVIDENCE_EVENT="${EVIDENCE_STORE}/io_health"
	readonly EVIDENCE_BAMAI="${HUATUO_BAMAI_BIN}"
	readonly EVIDENCE_PYTHON=$(command -v python3)
	mkdir -p "${EVIDENCE_WORK}" "${EVIDENCE_STORE}"
	io_health_md_init "${EVIDENCE_WORK}" htie
	evidence_store_source="huatuo-io-evidence-${md_label}"
	evidence_member=""
	evidence_target_disk=""
	evidence_target=""
	evidence_case_dir=""
	evidence_real_command=""
	if [[ ${evidence_protocol} == nvme ]]; then
		source "${ROOT_DIR}/integration/lib_io_health_nvme.sh"
	fi
}

io_health_evidence_prepare() {
	io_test_prepare_storage
	evidence_loop_backing="${io_test_storage_dir}/member.img"
	printf 'store=%s\nsource=%s\nbacking=%s\n' \
		"${EVIDENCE_STORE}" "${evidence_store_source}" "${evidence_loop_backing}" >> "${EVIDENCE_WORK}/resources.txt"
	if [[ ${evidence_protocol} == nvme ]]; then
		nvme_fixture_prepare
		evidence_real_command=${nvme_real_command}
		evidence_target_disk=${nvme_namespace}
		evidence_target=${nvme_controller}
	else
		io_test_prepare_scsi_disks 1
		((io_test_scsi_owned)) || skip "IO health evidence requires an owned scsi_debug disk; existing disks will not be changed"
		((${#io_test_disks[@]} == 1)) || fatal "evidence fixture did not create exactly one owned SCSI disk"
		evidence_target_disk=${io_test_disks[0]}
		evidence_target=${evidence_target_disk}
	fi
	evidence_member=${evidence_target_disk}
	if [[ ${1:-whole} == partition ]]; then
		require_commands sfdisk
		partition_target
	fi
	# Keep the mirror independent of older scsi_debug hosts' shared RAM store.
	io_health_md_create_loop "${evidence_loop_backing}"
	evidence_loop_device=${io_health_loop_devices[0]}
	printf 'member=%s\nscsi_owner=%s\n' "${evidence_member}" "${io_test_scsi_owner}" >> "${EVIDENCE_WORK}/resources.txt"
	io_health_md_create --size=4096 "${evidence_loop_device}" "/dev/${evidence_member}"
	wait_until 10 0.2 io_health_md_members_ready || fatal "evidence array UUID and both member identities cannot be confirmed"
	timeout --kill-after=2s 10s mount -t tmpfs -o size=2M "${evidence_store_source}" "${EVIDENCE_STORE}"
}

scsi_is_owned() {
	local owner
	((io_test_scsi_owned)) || return 1
	read -r owner < /sys/module/scsi_debug/parameters/inq_vendor || return 1
	[[ ${owner} == "${io_test_scsi_owner}" ]]
}

target_is_owned() {
	local partition_number
	if [[ ${evidence_protocol} == nvme ]]; then
		nvme_fixture_is_owned || return 1
	else
		scsi_is_owned || return 1
		[[ ${evidence_target_disk} == "${io_test_disks[0]}" ]] || return 1
	fi
	if [[ ${evidence_member} != "${evidence_target_disk}" ]]; then
		read -r partition_number 2> /dev/null < "/sys/class/block/${evidence_member}/partition" || return 1
		[[ ${partition_number} == 1 ]] || return 1
		[[ $(basename "$(dirname "$(readlink -f "/sys/class/block/${evidence_member}")")") == "${evidence_target_disk}" ]] || return 1
	fi
}

io_health_partition_ready() {
	[[ -b /dev/${evidence_member} ]] && target_is_owned
}

partition_target() {
	local old_member=${evidence_member}
	local -a partition_command=(sfdisk --lock=yes)
	evidence_member=${evidence_target_disk}
	target_is_owned || fatal "refusing to partition an unowned evidence target"
	if compgen -G "/sys/class/block/${evidence_target_disk}/holders/*" > /dev/null; then
		fatal "owned target ${evidence_target_disk} still has block holders before partitioning"
	fi
	io_health_md_wait_udev || fatal "udev did not settle before partitioning ${evidence_target_disk}"
	# Older sfdisk lacks --lock; flock supplies the same exclusive device lock.
	sfdisk --help > "${EVIDENCE_WORK}/sfdisk-help.txt"
	if ! grep -q -- '--lock' "${EVIDENCE_WORK}/sfdisk-help.txt"; then
		require_commands flock
		partition_command=(flock --exclusive --timeout 10 "/dev/${evidence_target_disk}" sfdisk)
	fi
	printf 'label: dos\nstart=2048,size=65536,type=fd\n' \
		| timeout --kill-after=2s 15s "${partition_command[@]}" "/dev/${evidence_target_disk}" >> "${EVIDENCE_WORK}/commands.log" 2>&1
	io_health_md_wait_udev || fatal "udev did not settle after partitioning ${evidence_target_disk}"
	case ${evidence_target_disk} in
	*[0-9]) evidence_member="${evidence_target_disk}p1" ;;
	*) evidence_member="${evidence_target_disk}1" ;;
	esac
	# A reused device node can appear before the replacement's sysfs identity.
	if ! wait_until 10 0.1 io_health_partition_ready; then
		{
			printf 'member=%s target=%s selected=%s expected_owner=%s\n' \
				"${evidence_member}" "${evidence_target_disk}" "${io_test_disks[*]}" "${io_test_scsi_owner}"
			ls -l "/dev/${evidence_member}" "/sys/class/block/${evidence_member}" "/sys/class/block/${evidence_target_disk}" || true
			for attribute in "/sys/class/block/${evidence_member}/partition" \
				"/sys/class/block/${evidence_target_disk}/device/vendor" \
				"/sys/class/block/${evidence_target_disk}/diskseq" \
				/sys/module/scsi_debug/parameters/inq_vendor; do
				printf '%s=' "${attribute}"
				cat "${attribute}" || true
			done
			readlink -f "/sys/class/block/${evidence_member}" "/sys/class/block/${evidence_target_disk}" || true
		} > "${EVIDENCE_WORK}/${evidence_member}.readiness.txt" 2>&1
		fatal "owned partition ${evidence_member} did not become ready; inspect ${EVIDENCE_WORK}/${evidence_member}.readiness.txt"
	fi
	timeout --kill-after=2s 5s sfdisk --dump "/dev/${evidence_target_disk}" > "${EVIDENCE_WORK}/${evidence_target_disk}.partitions"
	printf 'partition=%s parent=%s previous_member=%s\n' "${evidence_member}" "${evidence_target_disk}" "${old_member}" >> "${EVIDENCE_WORK}/resources.txt"
}

io_health_md_target_is_owned() {
	[[ $1 == "${evidence_member}" ]] && target_is_owned
}

io_health_md_members_ready() {
	io_health_md_array_is_owned && [[ -d ${md_sysfs}/md/dev-${evidence_member} ]]
}

evidence_group_stopped() {
	! kill -0 -- "-$1" 2> /dev/null
}

evidence_group_is_owned() {
	local pid group
	while read -r pid group; do
		[[ ${group} == "$1" && -r /proc/${pid}/environ ]] || continue
		if tr '\0' '\n' < "/proc/${pid}/environ" \
			| grep -Fxq "HUATUO_IO_EVIDENCE_CASE=${evidence_case_dir}"; then
			return 0
		fi
	done < <(ps -eo pid=,pgid=)
	return 1
}

stop_evidence_helpers() {
	local pid group
	[[ -n ${evidence_case_dir} && -f ${evidence_case_dir}/commands.jsonl ]] || return 0
	while IFS= read -r group; do
		evidence_group_stopped "${group}" && continue
		evidence_group_is_owned "${group}" || {
			evidence_group_stopped "${group}" && continue
			log_error "cannot confirm evidence group ${group} ownership; retaining devices"
			return 1
		}
		kill -TERM -- "-${group}" 2> /dev/null || true
		if ! wait_until 2 0.1 evidence_group_stopped "${group}"; then
			kill -KILL -- "-${group}" 2> /dev/null || true
			wait_until 2 0.1 evidence_group_stopped "${group}" || return 1
		fi
	done < <(jq -r 'select(.phase == "start" and .pgid != null) | .pgid' "${evidence_case_dir}/commands.jsonl")
	while IFS= read -r pid; do
		[[ -r /proc/${pid}/cmdline ]] || continue
		# A recorded PID may already have exited; verify this private executable.
		if tr '\0' '\n' < "/proc/${pid}/cmdline" | grep -Fxq "${evidence_case_dir}/tools/${evidence_tool}"; then
			stop_by_pid "${pid}" 1
			wait_until 2 0.1 test ! -d "/proc/${pid}" || return 1
		fi
	done < <(jq -r 'select(.phase == "start") | .pid' "${evidence_case_dir}/commands.jsonl")
	return 0
}

cleanup_evidence() {
	local status=$? failed=0 source
	trap - EXIT
	trap '' INT TERM
	set +e
	io_health_md_stop_daemon || failed=1
	stop_evidence_helpers || failed=1
	if [[ -n ${evidence_case_dir} && -f ${EVIDENCE_EVENT} ]]; then
		cp "${EVIDENCE_EVENT}" "${evidence_case_dir}/events-at-exit.raw" || failed=1
	fi
	if ((failed == 0)); then
		io_health_md_cleanup "${evidence_member:+/dev/${evidence_member}}" || failed=1
	else
		failed=1
	fi
	if [[ ${evidence_protocol} == nvme ]] && ((failed == 0)); then
		nvme_fixture_cleanup || failed=1
	fi
	if ((failed == 0)); then io_test_cleanup_disks || failed=1; fi
	io_health_md_wait_udev || failed=1
	if mountpoint -q "${EVIDENCE_STORE}"; then
		source=$(findmnt -n -o SOURCE --target "${EVIDENCE_STORE}")
		if [[ ${source} != "${evidence_store_source}" ]] \
			|| ! timeout --kill-after=2s 10s umount "${EVIDENCE_STORE}"; then
			log_error "H10 cleanup: inspect mount source ${evidence_store_source} before unmounting ${EVIDENCE_STORE}"
			failed=1
		fi
	fi
	printf 'cleanup_failed=%s\nexecution_status=%s\n' "${failed}" "${status}" >> "${EVIDENCE_WORK}/resources.txt"
	if ((failed)); then
		log_error "IO evidence resources remain; see ${EVIDENCE_WORK}/resources.txt and io-disks.resources"
		status=1
	fi
	exit "${status}"
}

io_health_member_state_is() {
	[[ -r ${md_sysfs}/md/dev-${evidence_member}/state ]] \
		&& grep -Eq "(^|,)$1(,|$)" "${md_sysfs}/md/dev-${evidence_member}/state"
}

restore_member() {
	io_health_md_array_is_owned || fatal "array ownership changed before member recovery"
	target_is_owned || fatal "target ownership changed before member recovery"
	if io_health_member_state_is faulty; then
		io_health_md_command mdadm "${md_device}" --remove "/dev/${evidence_member}" || fatal "cannot remove owned faulty member"
		# MD can retire its sysfs member after the remove command returns.
		wait_until 3 0.1 test ! -d "${md_sysfs}/md/dev-${evidence_member}" \
			|| fatal "removed test member did not retire before recovery"
		io_health_md_wait_udev || fatal "udev did not settle after member removal"
	fi
	if [[ ! -d ${md_sysfs}/md/dev-${evidence_member} ]]; then
		io_health_md_command mdadm "${md_device}" --add "/dev/${evidence_member}" || fatal "cannot restore owned test member"
	fi
	if ! wait_until 20 0.2 io_health_member_state_is in_sync; then
		{
			for attribute in "${md_sysfs}"/md/{array_state,degraded,sync_action,sync_completed,dev-*/state}; do
				[[ -r ${attribute} ]] || continue
				printf '%s=' "${attribute}"
				cat "${attribute}"
			done
			cat /proc/mdstat
		} > "${EVIDENCE_WORK}/member-recovery.txt" 2>&1
		fatal "test member did not return to in_sync; inspect ${EVIDENCE_WORK}/member-recovery.txt"
	fi
	io_health_md_wait_udev || fatal "udev did not settle after member recovery"
	wait_until 20 0.2 io_health_md_array_is_healthy \
		|| fatal "recovered RAID1 is not healthy and writable; inspect ${EVIDENCE_WORK}/array-health.txt"
}

fault_member() {
	io_health_md_array_is_owned || fatal "array ownership changed before member fault"
	io_health_md_wait_udev || fatal "udev did not settle before member fault"
	io_health_md_array_is_healthy || fatal "RAID1 is not healthy and writable before fault; inspect ${EVIDENCE_WORK}/array-health.txt"
	io_health_member_state_is in_sync || fatal "test member is not in_sync before fault"
	io_health_md_command mdadm "${md_device}" --fail "/dev/${evidence_member}" || fatal "cannot fault owned SCSI member"
	wait_until 5 0.1 io_health_member_state_is faulty || fatal "public MD state did not confirm faulty member"
	cat "${md_sysfs}/md/dev-${evidence_member}/state" >> "${evidence_case_dir}/fault-states.txt"
}

finish_case() {
	huatuo_bamai_metrics > "${evidence_case_dir}/metrics.txt"
	io_health_md_stop_daemon || fatal "evidence daemon remained after shutdown"
	stop_evidence_helpers || fatal "evidence helper remained after daemon shutdown"
	[[ ! -f ${EVIDENCE_EVENT} ]] || cp "${EVIDENCE_EVENT}" "${evidence_case_dir}/events.json"
	cp "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${evidence_case_dir}/huatuo.log"
}

io_health_evidence_saved() {
	[[ -s ${EVIDENCE_EVENT} ]] || return 1
	jq -s -e --arg array "${md_device##*/}" --arg member "${evidence_member}" --arg status "$1" '
	any(.[]; .tracer_name == "io_health" and .tracer_type == "event"
		and .tracer_data.type == "md_member_state" and .tracer_data.array == $array
		and .tracer_data.member == $member
		and ((.tracer_data.new_state | split(",") | index("faulty")) != null)
		and .tracer_data.collection_status == $status)
' "${EVIDENCE_EVENT}" > /dev/null 2>&1
}

metric_value() {
	local metric=$1 reason=$2
	huatuo_bamai_metrics > "${evidence_case_dir}/metrics.txt" || return 1
	awk -v metric="${metric}" -v reason="${reason}" -v disk="${evidence_target}" '
	index($1, "huatuo_bamai_io_health_" metric "{") == 1 &&
	index($1, "reason=\"" reason "\"") &&
	(metric == "event_persistence_failures_total" || index($1, "disk=\"" disk "\"")) { value += $2 }
	END { print value + 0 }
' "${evidence_case_dir}/metrics.txt"
}

command_count() {
	jq -s --arg target "/dev/${evidence_target}" \
		'[.[] | select(.phase == "start" and any(.args[]; . == $target))] | length' "${evidence_case_dir}/commands.jsonl"
}

io_health_evidence_command_started() {
	[[ $(command_count) -gt 0 ]]
}

assert_evidence_command_count() {
	local expected=$1 message=$2 actual
	actual=$(command_count) || fatal "cannot count evidence commands: ${evidence_case_dir}/commands.jsonl"
	[[ ${actual} -eq ${expected} ]] || fatal "${message}: expected=${expected} actual=${actual}"
}

assert_evidence_command_completed() {
	local ended=$1 message=$2
	[[ ${ended} != null ]] || fatal "${message}"
}

member_event_count() {
	[[ -s ${EVIDENCE_EVENT} ]] || {
		printf '0\n'
		return
	}
	jq -s --arg member "${evidence_member}" --arg state "$1" --arg status "${2:-}" '
	[.[] | select(.tracer_data.type == "md_member_state" and .tracer_data.member == $member
		and ((.tracer_data.new_state | split(",") | index($state)) != null)
		and ($status == "" or .tracer_data.collection_status == $status))] | length
' "${EVIDENCE_EVENT}"
}

io_health_member_event_increased() {
	local value
	value=$(member_event_count "$1" "${3:-}") || return 1
	((value > $2))
}

io_health_evidence_command_record() {
	local phase=$1 status=$2 output=$3 now
	shift 3
	now=$(python3 -c 'import time; print(time.monotonic())') || return 1
	jq -cn --arg phase "${phase}" --argjson pid "$$" --argjson monotonic "${now}" \
		--argjson status "${status}" --arg output "${output}" --args '
		{phase: $phase, pid: $pid, args: $ARGS.positional, monotonic: $monotonic}
		+ (if $phase == "start" then {pgid: $pid} else {} end)
		+ (if $phase == "end" then {output: $output, returncode: $status} else {} end)
	' -- "$@" >> "${HUATUO_IO_EVIDENCE_CASE}/commands.jsonl"
}

# Called by a private runtime wrapper; execute the real utility and journal its
# response without interpreting the product's event or deciding test success.
io_health_evidence_record_command() {
	local tool=$1 directory=${HUATUO_IO_EVIDENCE_CASE} target executable value status=0 output
	local -a devices=()
	shift
	target=$(< "${directory}/target")
	executable=$(< "${directory}/real-command")
	io_health_evidence_command_record attempt null "" "$@" || return 1
	for value in "$@"; do
		[[ ${value} != /dev/* ]] || devices+=("${value}")
	done
	if ((${#devices[@]} != 1)) || [[ ${devices[0]} != "/dev/${target}" ]]; then
		printf 'unexpected %s device arguments: %s\n' "${tool}" "${devices[*]}" >&2
		return 1
	fi
	if [[ ${tool} == nvme ]]; then
		case ${1:-} in smart-log | error-log) ;; *) return 1 ;; esac
		[[ $(< "/sys/class/nvme/${target}/subsysnqn") == "$(< "${directory}/nqn")" ]] || return 1
	else
		read -r value < "/sys/class/block/${target}/device/vendor" || return 1
		[[ ${value} == "$(< "${directory}/scsi-owner")" ]] || return 1
	fi
	io_health_evidence_command_record start null "" "$@" || return 1
	output="$$-${1}.json"
	[[ ${output} != */* ]] || return 1
	timeout --foreground --kill-after=1s 8s "${executable}" "$@" \
		> "${directory}/${output}" 2> "${directory}/${output%.json}.err" || status=$?
	io_health_evidence_command_record end "${status}" "${output}" "$@" || return 1
	cat "${directory}/${output}"
	cat "${directory}/${output%.json}.err" >&2
	return "${status}"
}

start_case() {
	local mode=$1
	# The PID file is removed by shared shutdown. Finalize a preceding case
	# before replacing its event store or command journal.
	[[ ! -r ${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid ]] || finish_case
	restore_member
	evidence_case_dir="${EVIDENCE_WORK}/${mode}"
	mkdir -p "${evidence_case_dir}/tools"
	printf '%s\n' "${mode}" > "${evidence_case_dir}/mode"
	printf '%s\n' "${evidence_target}" > "${evidence_case_dir}/target"
	: > "${evidence_case_dir}/commands.jsonl"
	rm -f -- "${EVIDENCE_EVENT}"
	if [[ ${evidence_protocol} == nvme || ${mode} == real ]]; then
		{
			printf '#!/bin/bash\nset -euo pipefail\nexport PATH=%q\n' "${PATH}"
			printf 'exec timeout --foreground --kill-after=1s 10s setsid --wait /bin/bash -c %q _ %q %q "$@"\n' \
				'source "$1"; shift; io_health_evidence_record_command "$@"' \
				"${ROOT_DIR}/integration/lib_io_health_evidence.sh" "${evidence_tool}"
		} > "${evidence_case_dir}/tools/${evidence_tool}"
		printf '%s\n' "${evidence_real_command}" > "${evidence_case_dir}/real-command"
		if [[ ${evidence_protocol} == nvme ]]; then
			printf '%s\n' "${nvme_nqn}" > "${evidence_case_dir}/nqn"
		else
			printf '%s\n' "${io_test_scsi_owner}" > "${evidence_case_dir}/scsi-owner"
		fi
	elif [[ ${mode} != missing ]]; then
		{
			printf '#!%s\n' "${EVIDENCE_PYTHON}"
			tail -n +2 "${ROOT_DIR}/integration/testdata/tools/test_smartctl.py"
		} > "${evidence_case_dir}/tools/smartctl"
	fi
	[[ ${mode} == missing ]] || chmod +x "${evidence_case_dir}/tools/${evidence_tool}"
	# Restrict only the daemon's tool search; orchestration retains its PATH.
	printf '#!/bin/bash\nexport PATH=%q\nexport HUATUO_IO_EVIDENCE_CASE=%q\nexec %q "$@"\n' \
		"${evidence_case_dir}/tools" "${evidence_case_dir}" "${EVIDENCE_BAMAI}" > "${evidence_case_dir}/bamai"
	chmod +x "${evidence_case_dir}/bamai"
	HUATUO_BAMAI_BIN="${evidence_case_dir}/bamai"
	io_test_start_bamai write_config_bamai_io_health --region dev --disable-kubelet --log-debug
	assert_io_health_md_observer_ready "${EVIDENCE_EVENT}" || fatal "MD observer is not ready for evidence injection"
}
