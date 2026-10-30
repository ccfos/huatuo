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

# Check SCSI/NVMe evidence and target identities in isolated cases.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_io.sh"
io_test_isolate_run_directory
source "${ROOT_DIR}/integration/lib_io_health.sh"

test_health_evidence() {
	set -euo pipefail

	source "${ROOT_DIR}/integration/lib.sh"
	source "${ROOT_DIR}/integration/lib_io.sh"
	source "${ROOT_DIR}/integration/config.sh"
	evidence_mode=${TEST_IO_HEALTH_EVIDENCE_MODE:-scsi}
	case ${evidence_mode} in
	scsi | identity | nvme) ;;
	*) fatal "TEST_IO_HEALTH_EVIDENCE_MODE must be scsi, identity or nvme" ;;
	esac
	evidence_protocol=scsi
	[[ ${evidence_mode} != nvme ]] || evidence_protocol=nvme
	evidence_tool=${evidence_protocol/scsi/smartctl}
	CURL_TIMEOUT=(--connect-timeout 2 --max-time 3)
	WAIT_HUATUO_BAMAI_TIMEOUT=30
	require_commands awk dd jq ps stat mdadm losetup truncate mount umount mountpoint findmnt python3 timeout
	require_readable /proc/mdstat /proc/sys/kernel/random/uuid
	[[ ${EUID} -eq 0 ]] || skip "H06-H08/H10 require root"

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
	evidence_loop_device=""
	evidence_loop_backing="${EVIDENCE_WORK}/member.img"
	evidence_case_dir=""
	evidence_real_command=""
	evidence_freeze_queue=""
	evidence_freeze_requests_original=""
	evidence_freeze_requests_pending=0
	if [[ ${evidence_protocol} == nvme ]]; then
		source "${ROOT_DIR}/integration/lib_io_health_nvme.sh"
	fi

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

	assert_partition_is_ready() {
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
		if ! wait_until 10 0.1 assert_partition_is_ready; then
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

	assert_array_has_both_members() {
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

	restore_freeze_queue() {
		((evidence_freeze_requests_pending)) || return 0
		if ! target_is_owned || [[ ${evidence_freeze_queue} != /sys/block/${evidence_target_disk}/queue/nr_requests ]] \
			|| ! timeout --kill-after=2s 5s bash -c 'printf "%s\n" "$2" > "$1"' _ \
				"${evidence_freeze_queue}" "${evidence_freeze_requests_original}" \
			|| [[ $(< "${evidence_freeze_queue}") != "${evidence_freeze_requests_original}" ]]; then
			log_error "H05 cannot restore owned queue ${evidence_freeze_queue} to ${evidence_freeze_requests_original}; retaining its devices"
			return 1
		fi
		evidence_freeze_requests_pending=0
		printf 'freeze_restored=%s\n' "${evidence_freeze_requests_original}" >> "${EVIDENCE_WORK}/resources.txt"
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
		if ((failed == 0)) && restore_freeze_queue; then
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
	trap cleanup_evidence EXIT
	trap 'log_error "IO evidence interrupted by INT"; exit 130' INT
	trap 'log_error "IO evidence interrupted by TERM or execution timeout"; exit 143' TERM

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
		((io_test_scsi_owned)) || skip "H06-H08/H10 BLOCKED: a test-owned scsi_debug disk is required; existing disks will not be changed"
		((${#io_test_disks[@]} == 1)) || fatal "evidence fixture did not create exactly one owned SCSI disk"
		evidence_target_disk=${io_test_disks[0]}
		evidence_target=${evidence_target_disk}
	fi
	evidence_member=${evidence_target_disk}
	if [[ ${evidence_mode} != scsi ]]; then
		require_commands sfdisk
		partition_target
	fi
	# Older scsi_debug hosts share one RAM store across their disks. A private
	# loop backing keeps the mirror's other member physically independent.
	io_health_md_create_loop "${evidence_loop_backing}"
	evidence_loop_device=${io_health_loop_devices[0]}
	printf 'member=%s\nscsi_owner=%s\n' "${evidence_member}" "${io_test_scsi_owner}" >> "${EVIDENCE_WORK}/resources.txt"
	io_health_md_create --size=4096 "${evidence_loop_device}" "/dev/${evidence_member}"
	wait_until 10 0.2 assert_array_has_both_members || fatal "evidence array UUID and both member identities cannot be confirmed"
	timeout --kill-after=2s 10s mount -t tmpfs -o size=2M "${evidence_store_source}" "${EVIDENCE_STORE}"

	assert_member_has_state() {
		[[ -r ${md_sysfs}/md/dev-${evidence_member}/state ]] \
			&& grep -Eq "(^|,)$1(,|$)" "${md_sysfs}/md/dev-${evidence_member}/state"
	}

	restore_member() {
		io_health_md_array_is_owned || fatal "array ownership changed before member recovery"
		target_is_owned || fatal "target ownership changed before member recovery"
		if assert_member_has_state faulty; then
			io_health_md_command mdadm "${md_device}" --remove "/dev/${evidence_member}" || fatal "cannot remove owned faulty member"
			# MD can retire its sysfs member after the remove command returns.
			wait_until 3 0.1 test ! -d "${md_sysfs}/md/dev-${evidence_member}" \
				|| fatal "removed test member did not retire before recovery"
			io_health_md_wait_udev || fatal "udev did not settle after member removal"
		fi
		if [[ ! -d ${md_sysfs}/md/dev-${evidence_member} ]]; then
			io_health_md_command mdadm "${md_device}" --add "/dev/${evidence_member}" || fatal "cannot restore owned test member"
		fi
		if ! wait_until 20 0.2 assert_member_has_state in_sync; then
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
		wait_until 20 0.2 assert_io_health_md_array_healthy \
			|| fatal "recovered RAID1 is not healthy and writable; inspect ${EVIDENCE_WORK}/array-health.txt"
	}

	fault_member() {
		io_health_md_array_is_owned || fatal "array ownership changed before member fault"
		io_health_md_wait_udev || fatal "udev did not settle before member fault"
		assert_io_health_md_array_healthy || fatal "RAID1 is not healthy and writable before fault; inspect ${EVIDENCE_WORK}/array-health.txt"
		assert_member_has_state in_sync || fatal "test member is not in_sync before fault"
		io_health_md_command mdadm "${md_device}" --fail "/dev/${evidence_member}" || fatal "cannot fault owned SCSI member"
		wait_until 5 0.1 assert_member_has_state faulty || fatal "public MD state did not confirm faulty member"
		cat "${md_sysfs}/md/dev-${evidence_member}/state" >> "${evidence_case_dir}/fault-states.txt"
	}

	start_case() {
		local mode=$1
		restore_member
		evidence_case_dir="${EVIDENCE_WORK}/${mode}"
		mkdir -p "${evidence_case_dir}/tools"
		printf '%s\n' "${mode}" > "${evidence_case_dir}/mode"
		printf '%s\n' "${evidence_target}" > "${evidence_case_dir}/target"
		: > "${evidence_case_dir}/commands.jsonl"
		rm -f -- "${EVIDENCE_EVENT}"
		if [[ ${mode} != missing ]]; then
			{
				printf '#!%s\n' "${EVIDENCE_PYTHON}"
				cat "${ROOT_DIR}/integration/testdata/tools/test_io_health_tool.py"
			} > "${evidence_case_dir}/tools/${evidence_tool}"
			chmod +x "${evidence_case_dir}/tools/${evidence_tool}"
		fi
		if [[ ${evidence_protocol} == nvme || ${mode} == real ]]; then
			printf '%s\n' "${evidence_real_command}" > "${evidence_case_dir}/real-command"
			command -v timeout > "${evidence_case_dir}/timeout-command"
		fi
		if [[ ${evidence_protocol} == nvme ]]; then
			printf '%s\n' "${nvme_nqn}" > "${evidence_case_dir}/nqn"
		elif [[ ${mode} == real ]]; then
			printf '%s\n' "${io_test_scsi_owner}" > "${evidence_case_dir}/scsi-owner"
		fi
		# Restrict only the daemon's tool search. Test helpers keep their normal PATH.
		printf '#!/bin/bash\nexport PATH=%q\nexport HUATUO_IO_EVIDENCE_CASE=%q\nexec %q "$@"\n' \
			"${evidence_case_dir}/tools" "${evidence_case_dir}" "${EVIDENCE_BAMAI}" > "${evidence_case_dir}/bamai"
		chmod +x "${evidence_case_dir}/bamai"
		HUATUO_BAMAI_BIN="${evidence_case_dir}/bamai"
		io_test_start_bamai write_config_bamai_io_health --region dev --disable-kubelet --log-debug
		assert_io_health_md_observer_ready "${EVIDENCE_EVENT}" || fatal "MD observer is not ready for evidence injection"
	}

	finish_case() {
		huatuo_bamai_metrics > "${evidence_case_dir}/metrics.txt"
		io_health_md_stop_daemon || fatal "evidence daemon remained after shutdown"
		stop_evidence_helpers || fatal "evidence helper remained after daemon shutdown"
		[[ ! -f ${EVIDENCE_EVENT} ]] || cp "${EVIDENCE_EVENT}" "${evidence_case_dir}/events.json"
		cp "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${evidence_case_dir}/huatuo.log"
	}

	assert_evidence_saved() {
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

	assert_evidence_command_started() {
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

	assert_evidence_target_normalized() {
		[[ ${evidence_member} != "${evidence_target}" ]] \
			|| fatal "H09 normalization requires a partition: member=${evidence_member} target=${evidence_target}"
		assert_evidence_command_count 1 "H09 evidence command did not normalize the member partition to its owned parent"
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

	assert_member_event_increased() {
		local value
		value=$(member_event_count "$1" "${3:-}") || return 1
		((value > $2))
	}

	assert_freeze_counter() {
		local phase=$1 input="${evidence_case_dir}/freeze-${1}.metrics"
		huatuo_bamai_metrics > "${input}" || fatal "H05 cannot scrape freeze Counter during ${phase}"
		io_test_metrics_json "${input}" > "${input}.json" || fatal "H05 invalid metric exposition during ${phase}"
		freeze_count=$(jq -er --arg disk "${evidence_target_disk}" '
			[.[] | select(.name == "huatuo_bamai_io_health_blkdisk_freeze")] as $samples
			| if all($samples[];
				.type == "counter" and .type_count == 1 and .label_count == 3
				and (.labels | keys) == ["disk", "host", "region"]
				and (.labels.disk | length > 0) and .labels.host == "huatuo-dev" and .labels.region == "dev"
				and (.raw_value | test("^[+]?[0-9]+([.][0-9]*)?([eE][+-]?[0-9]+)?$"))
				and (.value | type == "number") and (.value | isinfinite | not)
				and .value >= 0 and .value == (.value | floor))
				and all($samples | group_by(.labels)[]; length == 1)
			then [$samples[] | select(.labels.disk == $disk) | .value] | .[0] // 0
			else error("invalid freeze Counter type, labels or count") end
		' "${input}.json") || fatal "H05 invalid freeze Counter during ${phase}; inspect ${input}"
		if (($# == 2)); then
			awk -v value="${freeze_count}" -v before="$2" 'BEGIN { exit !(value > before) }'
		fi
	}

	assert_freeze_restored_and_idle() {
		local changed=$1 restored attempt
		# Restoration can freeze again; compare later scrapes with its cumulative value.
		assert_freeze_counter restored
		restored=${freeze_count}
		awk -v value="${restored}" -v before="${changed}" 'BEGIN { exit !(value >= before) }' \
			|| fatal "H05 freeze Counter regressed after restoring nr_requests"
		for attempt in 1 2; do
			sleep 0.2
			assert_freeze_counter "idle-${attempt}"
			[[ ${freeze_count} == "${restored}" ]] || fatal "H05 idle scrape changed the cumulative freeze Counter"
		done
	}

	freeze_counter_ready() {
		freeze_attempt=$((freeze_attempt + 1))
		if assert_freeze_counter "changed-${freeze_attempt}" "${freeze_before}"; then return 0; fi
		target_is_owned || fatal "H05 target ownership changed during startup qualification"
		timeout --kill-after=2s 5s bash -c 'printf "%s\n" "$2" > "$1"' _ "${evidence_freeze_queue}" "${freeze_next}" \
			|| fatal "H05 owned queue change failed during startup qualification"
		[[ $(< "${evidence_freeze_queue}") == "${freeze_next}" ]] || fatal "H05 queue readback differs during startup qualification"
		if [[ ${freeze_next} == "${freeze_changed}" ]]; then freeze_next=${evidence_freeze_requests_original}; else freeze_next=${freeze_changed}; fi
		return 1
	}

	test_freeze_counter() {
		local freeze_changed freeze_before freeze_attempt=0 freeze_next
		evidence_freeze_queue="/sys/block/${evidence_target_disk}/queue/nr_requests"
		target_is_owned || fatal "H05 refusing queue change after target ownership changed"
		if [[ ! -r ${evidence_freeze_queue} || ! -w ${evidence_freeze_queue} ]]; then
			log_info "NOT_RUN H05: owned disk has no writable nr_requests control"
			return
		fi
		if ! read -r evidence_freeze_requests_original < "${evidence_freeze_queue}"; then
			log_info "NOT_RUN H05: owned queue nr_requests cannot be read"
			return
		fi
		if [[ ! ${evidence_freeze_requests_original} =~ ^[0-9]+$ ]] || ((evidence_freeze_requests_original < 8)); then
			log_info "NOT_RUN H05: nr_requests=${evidence_freeze_requests_original} has no safe smaller test value"
			return
		fi
		freeze_changed=$((evidence_freeze_requests_original / 2))
		assert_freeze_counter baseline
		freeze_before=${freeze_count}
		printf 'freeze_queue=%s original=%s requested=%s\n' \
			"${evidence_freeze_queue}" "${evidence_freeze_requests_original}" "${freeze_changed}" >> "${EVIDENCE_WORK}/resources.txt"
		# A timed-out write may have changed the queue before its status arrives.
		evidence_freeze_requests_pending=1
		target_is_owned || fatal "H05 target ownership changed before queue update"
		if ! timeout --kill-after=2s 5s bash -c 'printf "%s\n" "$2" > "$1"' _ "${evidence_freeze_queue}" "${freeze_changed}" \
			|| [[ $(< "${evidence_freeze_queue}") != "${freeze_changed}" ]]; then
			restore_freeze_queue || fatal "H05 queue restoration failed after rejected change"
			log_info "NOT_RUN H05: owned queue did not accept a different nr_requests value"
			return
		fi
		# MD readiness does not imply that the block probes have attached. Keep
		# supplying real queue changes until the public Counter proves readiness.
		freeze_next=${evidence_freeze_requests_original}
		wait_until 30 0.2 freeze_counter_ready \
			|| fatal "H05 confirmed queue changes did not increase the freeze Counter within 30s"
		freeze_changed=${freeze_count}
		restore_freeze_queue || fatal "H05 queue restoration failed after freeze check"
		assert_freeze_restored_and_idle "${freeze_changed}"
		log_info "H05: owned queue change increases freeze Counter and idle scrapes preserve it"
	}

	assert_evidence_case() {
		local mode=$1 status=$2 reason=${3:-} value
		start_case "${mode}"
		[[ ${mode} != ok ]] || test_freeze_counter
		fault_member
		wait_until 20 0.2 assert_evidence_saved "${status}" || fatal "${mode}: confirmed member event lacks ${status} evidence result"
		if [[ ${mode} == missing ]]; then
			assert_evidence_command_count 0 "missing tool unexpectedly executed a command"
		else
			assert_evidence_command_count 1 "${mode}: expected one recorded evidence command"
		fi
		if [[ -n ${reason} ]]; then
			value=$(metric_value collection_errors_total "${reason}")
			[[ ${value} == 1 ]] || fatal "${mode}: ${reason} count=${value}, expected one deduplicated attempt"
		fi
		case ${mode} in
		ok)
			jq -s -e --arg member "${evidence_member}" '
			[.[] | select(.tracer_data.member == $member and .tracer_data.collection_status == "ok")
				| .tracer_data.scsi] as $evidence
			| ($evidence | length > 0) and all($evidence[];
				.smart_passed == false and .grown_defect_count == 0
				and .information_exception.asc == 0 and .information_exception.ascq == 0
				and .temperature.current_celsius == 0 and .temperature.trip_celsius == 0
				and .read.delayed_corrections == 0 and .read.reread_rewrite_corrections == 0
				and .read.uncorrected_errors == 0 and .read.gigabytes_processed == 0
				and .pending_defects.count == 0 and .pending_defects.sample_lbas == [])
		' "${EVIDENCE_EVENT}" > /dev/null || fatal "${mode}: SCSI evidence lost false/zero/empty values"
			;;
		partial)
			jq -s -e --arg member "${evidence_member}" '
			any(.[]; .tracer_data.member == $member and .tracer_data.collection_status == "partial"
				and .tracer_data.scsi.smart_passed == false)
		' "${EVIDENCE_EVENT}" > /dev/null || fatal "partial evidence discarded the valid SMART result"
			;;
		esac
		finish_case
		log_info "H06/H07: ${mode} evidence status=${status}, reason=${reason:-none}"
	}

	record_old_identity() {
		old_target=${evidence_target}
		old_member=${evidence_member}
		old_owner=${io_test_scsi_owner}
		read -r old_vendor < "/sys/class/block/${evidence_target_disk}/device/vendor"
		printf 'old_disk=%s old_member=%s old_vendor=%s old_owner=%s\n' \
			"${old_target}" "${old_member}" "${old_vendor}" "${old_owner}" > "${evidence_case_dir}/identity.txt"
		if [[ -r /sys/class/block/${evidence_target_disk}/diskseq ]]; then
			printf 'old_diskseq=%s\n' "$(< "/sys/class/block/${evidence_target_disk}/diskseq")" >> "${evidence_case_dir}/identity.txt"
		fi
	}

	retire_old_identity() {
		io_health_md_command mdadm "${md_device}" --remove "/dev/${evidence_member}" || fatal "cannot remove old identity from the test array"
		wait_until 3 0.1 test ! -d "${md_sysfs}/md/dev-${evidence_member}" || fatal "old MD member did not retire"
		if compgen -G "/sys/class/block/${evidence_member}/holders/*" > /dev/null \
			|| compgen -G "/sys/class/block/${evidence_target_disk}/holders/*" > /dev/null; then
			fatal "H09 old owned target still has holders before replacement"
		fi
		# Removing the complete owned RAM device, then creating it with a new
		# INQUIRY vendor marker, establishes a new identity without relying on a
		# driver's rebind preserving or changing an implementation-private token.
		io_test_cleanup_scsi_disks || fatal "cannot retire the old owned SCSI identity"
		io_health_md_wait_udev || fatal "udev did not settle after retiring the old SCSI identity"
		[[ ! -e /sys/class/block/${old_target} && ! -e /sys/module/scsi_debug ]] \
			|| fatal "H09 old device/module disappearance was not confirmed"
	}

	recreate_identity() {
		io_test_prepare_scsi_disks 1
		((io_test_scsi_owned)) || skip "H09 BLOCKED: replacement owned SCSI device could not be created"
		evidence_target_disk=${io_test_disks[0]}
		evidence_target=${evidence_target_disk}
		evidence_member=${evidence_target_disk}
		read -r new_vendor < "/sys/class/block/${evidence_target_disk}/device/vendor"
		[[ ${io_test_scsi_owner} != "${old_owner}" && ${new_vendor} != "${old_vendor}" &&
			${new_vendor} == "${io_test_scsi_owner}" ]] || fatal "replacement device lacks an independently distinct INQUIRY identity"
		printf 'new_disk=%s new_vendor=%s new_owner=%s\n' "${evidence_target_disk}" "${new_vendor}" "${io_test_scsi_owner}" \
			>> "${evidence_case_dir}/identity.txt"
		if [[ -r /sys/class/block/${evidence_target_disk}/diskseq ]]; then
			printf 'new_diskseq=%s\n' "$(< "/sys/class/block/${evidence_target_disk}/diskseq")" >> "${evidence_case_dir}/identity.txt"
		fi
		partition_target
		printf '%s\n' "${evidence_target}" > "${evidence_case_dir}/target"
		printf 'ok\n' > "${evidence_case_dir}/mode"
		restore_member
	}

	test_identity_cooldown() {
		local old_target old_member old_vendor old_owner new_vendor ended now before faults_before evidence_before new_started
		start_case identity-cooldown
		printf 'ok\n' > "${evidence_case_dir}/mode"
		record_old_identity
		fault_member
		wait_until 20 0.2 assert_evidence_saved ok || fatal "H09 old identity did not establish successful evidence"
		assert_evidence_target_normalized
		log_info "H09: member=${evidence_member} retains its partition identity; smartctl target=${evidence_target}"
		ended=$(jq -s '[.[] | select(.phase == "end")][-1].monotonic' "${evidence_case_dir}/commands.jsonl")
		assert_evidence_command_completed "${ended}" "H09 successful old command has no completion record"
		before=$(command_count)
		assert_eq "${before}" 1 || fatal "H09 expected one successful old-identity evidence command"
		restore_member
		now=$("${EVIDENCE_PYTHON}" -c 'import time; print(time.monotonic())')
		if ! awk -v now="${now}" -v ended="${ended}" 'BEGIN { exit !(now < ended + 60) }'; then
			log_info "NOT_RUN H09 old cooldown: member recovery exceeded the completed command cooldown"
			finish_case
			return
		fi
		faults_before=$(member_event_count faulty)
		fault_member
		wait_until 10 0.2 assert_member_event_increased faulty "${faults_before}" || fatal "H09 old identity cooldown event is missing"
		now=$("${EVIDENCE_PYTHON}" -c 'import time; print(time.monotonic())')
		if ! awk -v now="${now}" -v ended="${ended}" 'BEGIN { exit !(now < ended + 60) }'; then
			log_info "NOT_RUN H09 old cooldown: repeat event was observed after the cooldown expired"
			finish_case
			return
		fi
		assert_evidence_command_count "${before}" "H09 old identity did not suppress a repeated evidence command"
		retire_old_identity
		recreate_identity
		if [[ ${evidence_target} != "${old_target}" ]]; then
			log_info "NOT_RUN H09 same-name cooldown: replacement name ${evidence_target} differs from ${old_target}"
			finish_case
			return
		fi
		assert_io_health_md_observer_ready "${EVIDENCE_EVENT}" || fatal "replacement MD observer is not ready"
		now=$("${EVIDENCE_PYTHON}" -c 'import time; print(time.monotonic())')
		if ! awk -v now="${now}" -v ended="${ended}" 'BEGIN { exit !(now < ended + 60) }'; then
			log_info "NOT_RUN H09 same-name cooldown: replacement setup exceeded the established old cooldown"
			finish_case
			return
		fi
		before=$(command_count)
		evidence_before=$(member_event_count faulty ok)
		fault_member
		wait_until 20 0.2 assert_member_event_increased faulty "${evidence_before}" ok \
			|| fatal "H09 same-name new identity inherited the old cooldown or lacks new successful evidence"
		assert_evidence_command_count "$((before + 1))" "H09 new identity did not execute one evidence command"
		new_started=$(jq -s '[.[] | select(.phase == "start")][-1].monotonic' "${evidence_case_dir}/commands.jsonl")
		if ! awk -v started="${new_started}" -v ended="${ended}" 'BEGIN { exit !(started < ended + 60) }'; then
			log_info "NOT_RUN H09 same-name cooldown: new evidence started after the old cooldown expired"
			finish_case
			return
		fi
		finish_case
		log_info "H09: established old cooldown suppressed its repeat, while the same-name replacement collected inside 60s"
	}

	assert_retired_identity_output() {
		local old_member=$1
		[[ $(metric_value collection_errors_total target_changed) == 1 ]] \
			|| fatal "H09 retired target did not report one target_changed attempt"
		jq -s -e --arg member "${old_member}" '
		any(.[]; .tracer_data.member == $member and .tracer_data.collection_status == "unsupported"
			and (.tracer_data | has("scsi") | not) and (.tracer_data | has("nvme") | not))
	' "${EVIDENCE_EVENT}" > /dev/null || fatal "H09 attached stale protocol evidence after target retirement"
	}

	test_identity() {
		local old_target old_member old_vendor old_owner tool_pid ended new_vendor before
		test_identity_cooldown

		start_case gate
		record_old_identity
		fault_member
		wait_until 3 0.1 assert_evidence_command_started || fatal "H09 gated evidence command did not start"
		tool_pid=$(jq -r --arg target "/dev/${old_target}" \
			'select(.phase == "start" and any(.args[]; . == $target)) | .pid' "${evidence_case_dir}/commands.jsonl")
		retire_old_identity
		kill -0 "${tool_pid}" 2> /dev/null \
			|| skip "H09 BLOCKED: device retirement exceeded the external evidence command's active window"
		touch "${evidence_case_dir}/release"
		wait_until 20 0.2 assert_evidence_saved unsupported || fatal "H09 retired target did not produce an unsupported evidence result"
		assert_retired_identity_output "${old_member}"

		ended=$(jq -s --arg target "/dev/${old_target}" \
			'[.[] | select(.phase == "end" and any(.args[]; . == $target))][0].monotonic' "${evidence_case_dir}/commands.jsonl")
		assert_evidence_command_completed "${ended}" "H09 old command has no completion evidence"

		recreate_identity
		before=$(command_count)
		assert_io_health_md_observer_ready "${EVIDENCE_EVENT}" || fatal "replacement MD observer is not ready"
		assert_evidence_command_count "${before}" "H09 observer check unexpectedly collected member evidence"
		fault_member
		wait_until 20 0.2 assert_evidence_saved ok || fatal "H09 replacement member lacks successful evidence; inspect saved events and command journal"
		assert_evidence_command_count "$((before + 1))" "H09 new identity did not execute its own evidence command"
		finish_case
		log_info "H09: old evidence rejected after retirement; distinct INQUIRY identity ${old_vendor}->${new_vendor} recovered (target ${old_target}->${evidence_target})"
	}

	assert_nvme_evidence_saved() {
		[[ -s ${EVIDENCE_EVENT} ]] || return 1
		jq -s -e --arg member "${evidence_member}" --arg array "${md_device##*/}" '
		any(.[]; .tracer_name == "io_health" and .tracer_type == "event"
			and .tracer_data.type == "md_member_state" and .tracer_data.member == $member and .tracer_data.array == $array
			and (.tracer_data.collection_status == "ok" or .tracer_data.collection_status == "partial")
			and (.tracer_data.nvme | type == "object"))
	' "${EVIDENCE_EVENT}" > /dev/null 2>&1
	}

	assert_nvme_evidence_matches_commands() {
		local smart_output error_output
		jq -s -e --arg target "/dev/${evidence_target}" '
		[.[] | select(.phase == "attempt")] as $attempts
		| all($attempts[];
			(.args[0] == "smart-log" or .args[0] == "error-log")
			and ([.args[] | select(startswith("/dev/"))] == [$target]))
		and all(["smart-log", "error-log"][];
			. as $command | any($attempts[]; .args[0] == $command))
	' "${evidence_case_dir}/commands.jsonl" > /dev/null || fatal "H06 NVMe requires SMART and error-log attempts on the private controller"
		smart_output=$(jq -rs '[.[] | select(.phase == "end" and .args[0] == "smart-log" and .returncode == 0)][0].output // empty' \
			"${evidence_case_dir}/commands.jsonl")
		[[ -n ${smart_output} && ${smart_output} != */* ]] || fatal "H06 NVMe real SMART command output was not recorded"
		jq -s -e --slurpfile source "${evidence_case_dir}/${smart_output}" --arg member "${evidence_member}" '
		def nvme_uint:
			(if type == "object" then .value else . end)
			| (if type == "string" and test("^0[xX][0-9a-fA-F]+$") then
				ascii_downcase | .[2:] | explode
				| reduce .[] as $digit (0; . * 16 + ($digit - (if $digit <= 57 then 48 else 87 end)))
			else tonumber end)
			| if . >= 0 and . == floor then . else error("invalid unsigned NVMe value") end;
		($source[0].critical_warning | nvme_uint) as $warning
		| ($warning <= 255) and any(.[]; .tracer_data.member == $member
			and .tracer_data.nvme.critical_warning == $warning
			and .tracer_data.nvme.media_errors_total == ($source[0].media_errors | tonumber)
			and (.tracer_data | has("scsi") | not))
	' "${EVIDENCE_EVENT}" > /dev/null || fatal "H06 NVMe evidence differs from the actual nvme-cli SMART response"
		error_output=$(jq -rs '[.[] | select(.phase == "end" and .args[0] == "error-log" and .returncode == 0)][0].output // empty' \
			"${evidence_case_dir}/commands.jsonl")
		if [[ -n ${error_output} && ${error_output} != */* ]]; then
			jq -s -e --arg member "${evidence_member}" '
			any(.[]; .tracer_data.member == $member
				and (.tracer_data.nvme.error_log | type == "array" and length <= 8))
		' "${EVIDENCE_EVENT}" > /dev/null || fatal "H06 NVMe error-log output is missing or exceeds eight entries"
			if jq -e '(.errors | type == "array") and all(.errors[]; (.error_count | tonumber) == 0)' \
				"${evidence_case_dir}/${error_output}" > /dev/null; then
				jq -s -e --arg member "${evidence_member}" \
					'any(.[]; .tracer_data.member == $member and .tracer_data.nvme.error_log == [])' \
					"${EVIDENCE_EVENT}" > /dev/null || fatal "H06 NVMe did not preserve a known empty error log"
			fi
		fi
	}

	test_nvme_evidence() {
		start_case real
		fault_member
		wait_until 20 0.2 assert_nvme_evidence_saved || fatal "H06 NVMe member event lacks protocol evidence from the readable private target"
		assert_nvme_evidence_matches_commands

		finish_case
		log_info "H06/H09: real nvme-cli evidence matches its captured response; partition=${evidence_member}, owned controller=${evidence_target}, NQN=${nvme_nqn}"
	}

	case ${evidence_mode} in
	identity)
		test_identity
		exit 0
		;;
	nvme)
		test_nvme_evidence
		exit 0
		;;
	esac

	assert_real_scsi_evidence_saved() {
		assert_evidence_saved ok || assert_evidence_saved partial
	}

	assert_real_scsi_evidence_matches_command() {
		local output
		assert_evidence_command_count 1 "H06 real SCSI evidence must execute once"
		output=$(jq -rs '[.[] | select(.phase == "end")][0].output // empty' "${evidence_case_dir}/commands.jsonl")
		[[ -n ${output} && ${output} != */* ]] || fatal "H06 real smartctl output was not recorded"
		jq -e '(.smart_status.passed | type) == "boolean"
			or (.scsi_grown_defect_list | type) == "number"
			or (.temperature.current | type) == "number"' "${evidence_case_dir}/${output}" > /dev/null \
			|| fatal "H06 qualified SCSI target lost its supported SMART fields"
		jq -s -e --slurpfile source "${evidence_case_dir}/${output}" --arg member "${evidence_member}" '
		$source[0] as $raw | any(.[]; .tracer_data.member == $member
			and (.tracer_data.collection_status == "ok" or .tracer_data.collection_status == "partial")
			and (.tracer_data | has("nvme") | not)
			and (.tracer_data.scsi | type == "object")
			and (if ($raw.smart_status.passed | type) == "boolean"
				then .tracer_data.scsi.smart_passed == $raw.smart_status.passed else true end)
			and (if ($raw.scsi_grown_defect_list | type) == "number"
				then .tracer_data.scsi.grown_defect_count == $raw.scsi_grown_defect_list else true end)
			and (if ($raw.temperature.current | type) == "number"
				then .tracer_data.scsi.temperature.current_celsius == $raw.temperature.current else true end))
	' "${EVIDENCE_EVENT}" > /dev/null || fatal "H06 SCSI evidence differs from the actual smartctl response"
	}

	test_real_scsi_evidence() {
		local status=0
		if ! evidence_real_command=$(command -v smartctl); then
			log_info "NOT_RUN H06 real SCSI evidence: smartctl is unavailable"
			return
		fi
		target_is_owned || fatal "H06 SCSI ownership changed before real smartctl qualification"
		timeout --kill-after=1s 8s "${evidence_real_command}" -x -j "/dev/${evidence_target}" \
			> "${EVIDENCE_WORK}/smartctl-baseline.json" 2> "${EVIDENCE_WORK}/smartctl-baseline.err" || status=$?
		printf 'smartctl_baseline_exit=%s\n' "${status}" >> "${EVIDENCE_WORK}/resources.txt"
		((status != 124 && status != 137)) || fatal "H06 real smartctl qualification did not finish (exit ${status})"
		jq -e 'type == "object"' "${EVIDENCE_WORK}/smartctl-baseline.json" > /dev/null \
			|| fatal "H06 real smartctl qualification did not return JSON (exit ${status})"
		if ! jq -e '(.smart_status.passed | type) == "boolean"
			or (.scsi_grown_defect_list | type) == "number"
			or (.temperature.current | type) == "number"' "${EVIDENCE_WORK}/smartctl-baseline.json" > /dev/null; then
			log_info "NOT_RUN H06 real SCSI evidence: owned scsi_debug target provides no supported SMART fields (exit ${status})"
			cat "${EVIDENCE_WORK}/smartctl-baseline.json" "${EVIDENCE_WORK}/smartctl-baseline.err"
			return
		fi
		start_case real
		fault_member
		wait_until 20 0.2 assert_real_scsi_evidence_saved || fatal "H06 member event lacks real SCSI evidence; inspect smartctl output and events"
		assert_real_scsi_evidence_matches_command
		finish_case
		log_info "H06: real smartctl evidence matches its captured response on the owned SCSI disk"
	}

	test_real_scsi_evidence
	assert_evidence_case ok ok
	assert_evidence_case partial partial parse_error
	assert_evidence_case exec error exec_error
	assert_evidence_case timeout timeout timeout
	assert_evidence_case missing unsupported tool_unavailable

	evidence_persistence_baseline_ready=0
	test_evidence_cooldown() {
		local tool_pid faults_before ended now
		start_case gate
		fault_member
		wait_until 3 0.1 assert_evidence_command_started || fatal "gated evidence command did not start"
		io_health_md_command mdadm "${md_device}" --remove "/dev/${evidence_member}" || fatal "cannot remove gated faulty member"
		wait_until 3 0.1 test ! -d "${md_sysfs}/md/dev-${evidence_member}" || fatal "member removal was not observed"
		# Confirm the second event reached Huatuo before allowing the first command to
		# finish; otherwise the cooldown could hide a broken in-progress guard.
		wait_until 10 0.2 assert_member_event_increased removed 0 || fatal "H08 suppressed the original member-removal event"
		assert_evidence_command_count 1 "H08 duplicated evidence while the first command was active"
		tool_pid=$(jq -r --arg target "/dev/${evidence_member}" \
			'select(.phase == "start" and .args[-1] == $target) | .pid' "${evidence_case_dir}/commands.jsonl")
		if ! kill -0 "${tool_pid}" 2> /dev/null; then
			log_info "NOT_RUN H08: member removal exceeded the evidence command active window"
			finish_case
			restore_member
			return
		fi
		touch "${evidence_case_dir}/release"
		wait_until 20 0.2 assert_evidence_saved ok || fatal "gated evidence did not finish and persist"
		ended=$(jq -s '[.[] | select(.phase == "end")][-1].monotonic' "${evidence_case_dir}/commands.jsonl")
		assert_evidence_command_completed "${ended}" "H08 gated command has no completion record"
		restore_member
		now=$("${EVIDENCE_PYTHON}" -c 'import time; print(time.monotonic())')
		if ! awk -v now="${now}" -v ended="${ended}" 'BEGIN { exit !(now < ended + 60) }'; then
			log_info "NOT_RUN H08 cooldown: member recovery exceeded the completed command cooldown"
			finish_case
			return
		fi
		faults_before=$(member_event_count faulty)
		fault_member
		wait_until 10 0.2 assert_member_event_increased faulty "${faults_before}" || fatal "H08 suppressed an original event during cooldown"
		now=$("${EVIDENCE_PYTHON}" -c 'import time; print(time.monotonic())')
		if ! awk -v now="${now}" -v ended="${ended}" 'BEGIN { exit !(now < ended + 60) }'; then
			log_info "NOT_RUN H08 cooldown: repeat event was observed after the cooldown expired"
			finish_case
			restore_member
			return
		fi
		assert_evidence_command_count 1 "H08 repeated evidence inside the post-completion cooldown"
		restore_member
		evidence_persistence_baseline_ready=1
		log_info "H08: original MD events survive evidence suppression while active and after command completion"
	}

	test_evidence_cooldown

	# Fill only the private event filesystem. Removing the filler restores it;
	# no storage internals, host disk fullness, or file-opening strategy is assumed.
	# H08 leaves a saved successful event and an in_sync member. Its local skip
	# closes the daemon, so H10 still initializes independently on that path.
	if ((evidence_persistence_baseline_ready == 0)); then
		start_case persistence
		fault_member
		wait_until 20 0.2 assert_evidence_saved ok || fatal "H10 baseline event was not saved"
		restore_member
	fi
	"${EVIDENCE_PYTHON}" - "${EVIDENCE_STORE}" << 'PY'
import errno
from pathlib import Path
import sys

directory = Path(sys.argv[1])
with (directory / "filler").open("wb", buffering=0) as output:
    try:
        while True:
            output.write(b"x" * 4096)
    except OSError as error:
        if error.errno != errno.ENOSPC:
            raise

PY
	assert_event_store_enospc() {
		if LC_ALL=C timeout --kill-after=2s 5s dd if=/dev/zero of="${EVIDENCE_STORE}/probe" bs=1 count=1 conv=fsync status=none \
			2> "${evidence_case_dir}/enospc-probe.err"; then
			fatal "private event filesystem still accepts new data"
		fi
		grep -Fq 'No space left on device' "${evidence_case_dir}/enospc-probe.err" \
			|| fatal "private event filesystem probe failed without ENOSPC; inspect enospc-probe.err"
	}
	assert_event_store_enospc
	evidence_persistence_before=$(metric_value event_persistence_failures_total save_error)
	assert_persistence_failed() {
		local value
		value=$(metric_value event_persistence_failures_total save_error) || return 1
		awk -v value="${value}" -v before="${evidence_persistence_before}" 'BEGIN { exit !(value > before) }'
	}
	# ENOSPC on a new file does not exhaust the existing log's allocated tail.
	# Wait for each real fault to append data or report failure before retrying.
	evidence_allocation_size=$(stat -f -c %S "${EVIDENCE_STORE}")
	evidence_persistence_confirmed=0
	evidence_persistence_deadline=$((SECONDS + 30))
	assert_persistence_progress() {
		assert_persistence_failed || [[ $(stat -c %s "${EVIDENCE_EVENT}") -gt ${evidence_event_size} ]]
	}
	while true; do
		evidence_event_size=$(stat -c %s "${EVIDENCE_EVENT}")
		printf 'allocation_size=%s event_size=%s tail_bytes=%s\n' \
			"${evidence_allocation_size}" "${evidence_event_size}" "$(((evidence_allocation_size - evidence_event_size % evidence_allocation_size) % evidence_allocation_size))" \
			>> "${evidence_case_dir}/enospc-progress.txt"
		fault_member
		wait_until 10 0.2 assert_persistence_progress \
			|| fatal "H10 fault produced neither a log append nor save_error; inspect enospc-progress.txt"
		if assert_persistence_failed; then
			evidence_persistence_confirmed=1
			cp "${evidence_case_dir}/metrics.txt" "${evidence_case_dir}/enospc-metrics.txt"
			break
		fi
		restore_member
		if ((SECONDS >= evidence_persistence_deadline)); then
			log_info "NOT_RUN H10: existing event log still accepted appends; ENOSPC did not reach its allocated tail; inspect enospc-progress.txt"
			break
		fi
	done
	evidence_failed_count=$(metric_value event_persistence_failures_total save_error)
	rm -f -- "${EVIDENCE_STORE}/filler" "${EVIDENCE_STORE}/probe"
	assert_persistence_counter_restored() {
		local after
		after=$(metric_value event_persistence_failures_total save_error)
		awk -v before="${evidence_failed_count}" -v after="${after}" 'BEGIN { exit !(after >= before) }' \
			|| fatal "H10 save_error Counter regressed after removing the external storage fault"
	}
	assert_persistence_counter_restored
	finish_case
	if ((evidence_persistence_confirmed)); then
		log_info "H10: real event-store ENOSPC increments save_error and /metrics remains available; MD has no raw event Counter"
	fi
	exit 0
}

case ${TEST_IO_HEALTH_EVIDENCE_MODE:-all} in
all) io_test_run_cases "$0" TEST_IO_HEALTH_EVIDENCE_MODE scsi identity nvme ;;
scsi | identity | nvme) test_health_evidence ;;
*) fatal "TEST_IO_HEALTH_EVIDENCE_MODE must be all, scsi, identity or nvme" ;;
esac
