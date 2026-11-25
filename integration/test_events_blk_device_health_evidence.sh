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

# Verify SCSI command results, cooldown and event-store failures.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_io.sh"
io_test_isolate_run_directory
source "${ROOT_DIR}/integration/config.sh"
source "${ROOT_DIR}/integration/lib_blk_device_health.sh"
source "${ROOT_DIR}/integration/lib_blk_device_health_evidence.sh"

blk_device_health_evidence_init scsi
trap cleanup_evidence EXIT
trap 'log_error "IO evidence interrupted by INT"; exit 130' INT
trap 'log_error "IO evidence interrupted by TERM or execution timeout"; exit 143' TERM
blk_device_health_evidence_prepare

assert_evidence_case() {
	local mode=$1 status=$2 reason=${3:-} value
	start_case "${mode}"
	fault_member
	wait_until 20 0.2 blk_device_health_evidence_saved "${status}" || fatal "${mode}: confirmed member event lacks ${status} evidence result"
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

blk_device_health_real_scsi_evidence_saved() {
	blk_device_health_evidence_saved ok || blk_device_health_evidence_saved partial
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
	wait_until 20 0.2 blk_device_health_real_scsi_evidence_saved || fatal "H06 member event lacks real SCSI evidence; inspect smartctl output and events"
	assert_real_scsi_evidence_matches_command
	finish_case
	log_info "H06: real smartctl evidence matches its captured response on the owned SCSI disk"
}

test_evidence_cooldown() {
	local tool_pid faults_before ended now
	start_case gate
	fault_member
	wait_until 3 0.1 blk_device_health_evidence_command_started || fatal "gated evidence command did not start"
	blk_device_health_md_command mdadm "${md_device}" --remove "/dev/${evidence_member}" || fatal "cannot remove gated faulty member"
	wait_until 3 0.1 test ! -d "${md_sysfs}/md/dev-${evidence_member}" || fatal "member removal was not observed"
	# Confirm the second event reached Huatuo before allowing the first command to
	# finish; otherwise the cooldown could hide a broken in-progress guard.
	wait_until 10 0.2 blk_device_health_member_event_increased removed 0 || fatal "H08 suppressed the original member-removal event"
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
	wait_until 20 0.2 blk_device_health_evidence_saved ok || fatal "gated evidence did not finish and persist"
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
	wait_until 10 0.2 blk_device_health_member_event_increased faulty "${faults_before}" || fatal "H08 suppressed an original event during cooldown"
	now=$("${EVIDENCE_PYTHON}" -c 'import time; print(time.monotonic())')
	if ! awk -v now="${now}" -v ended="${ended}" 'BEGIN { exit !(now < ended + 60) }'; then
		log_info "NOT_RUN H08 cooldown: repeat event was observed after the cooldown expired"
		finish_case
		restore_member
		return
	fi
	assert_evidence_command_count 1 "H08 repeated evidence inside the post-completion cooldown"
	restore_member
	log_info "H08: original MD events survive evidence suppression while active and after command completion"
}

test_evidence_persistence() {
	# Fill only the private event filesystem. Removing the filler restores it;
	# no storage internals, host disk fullness, or file-opening strategy is assumed.
	start_case persistence
	# start_case already saved a controlled sync-action event. Keep membership
	# stable so this storage check does not depend on watcher teardown/recovery.
	local fill_status=0
	LC_ALL=C timeout --kill-after=2s 5s dd if=/dev/zero of="${EVIDENCE_STORE}/filler" bs=4096 status=none \
		2> "${evidence_case_dir}/enospc-fill.err" || fill_status=$?
	printf 'fill_exit=%s\n' "${fill_status}" >> "${evidence_case_dir}/enospc-progress.log"
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
	blk_device_health_persistence_failure_increased() {
		local value
		value=$(metric_value event_persistence_failures_total save_error) || return 1
		cp "${evidence_case_dir}/metrics.txt" "${evidence_case_dir}/enospc-metrics.out"
		printf 'save_error_baseline=%s save_error=%s event_size=%s sync_action=%s\n' \
			"${evidence_persistence_before}" "${value}" "$(stat -c %s "${EVIDENCE_EVENT}")" \
			"$(< "${md_sysfs}/md/sync_action")" >> "${evidence_case_dir}/enospc-progress.log"
		awk -v value="${value}" -v before="${evidence_persistence_before}" 'BEGIN { exit !(value > before) }'
	}
	# ENOSPC on a new file does not exhaust the existing log's allocated tail.
	# Reuse controlled non-fault events until an append needs a new allocation.
	evidence_allocation_size=$(stat -f -c %S "${EVIDENCE_STORE}")
	evidence_persistence_confirmed=0
	evidence_persistence_deadline=$((SECONDS + 30))
	printf '1\n' > "${md_sysfs}/md/sync_speed_min"
	printf '1\n' > "${md_sysfs}/md/sync_speed_max"
	blk_device_health_persistence_transition() {
		if blk_device_health_persistence_failure_increased || [[ $(stat -c %s "${EVIDENCE_EVENT}") -gt ${evidence_event_size} ]]; then
			return 0
		fi
		evidence_persistence_polls=$((evidence_persistence_polls + 1))
		if ((evidence_persistence_polls % 5 == 1)); then
			blk_device_health_md_toggle_sync_action || fatal "H10 cannot request a controlled MD transition"
		fi
		return 1
	}
	while true; do
		evidence_event_size=$(stat -c %s "${EVIDENCE_EVENT}")
		evidence_persistence_polls=0
		printf 'allocation_size=%s event_size=%s tail_bytes=%s\n' \
			"${evidence_allocation_size}" "${evidence_event_size}" "$(((evidence_allocation_size - evidence_event_size % evidence_allocation_size) % evidence_allocation_size))" \
			>> "${evidence_case_dir}/enospc-progress.log"
		wait_until 10 0.2 blk_device_health_persistence_transition \
			|| fatal "H10 controlled MD transitions produced neither a log append nor save_error; inspect enospc-progress.log and enospc-metrics.out"
		if blk_device_health_persistence_failure_increased; then
			evidence_persistence_confirmed=1
			break
		fi
		if ((SECONDS >= evidence_persistence_deadline)); then
			log_info "NOT_RUN H10: existing event log still accepted appends; ENOSPC did not reach its allocated tail; inspect enospc-progress.log"
			break
		fi
	done
	printf 'idle\n' > "${md_sysfs}/md/sync_action"
	printf 'system\n' > "${md_sysfs}/md/sync_speed_min"
	printf 'system\n' > "${md_sysfs}/md/sync_speed_max"
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
}

test_real_scsi_evidence
assert_evidence_case ok ok
assert_evidence_case partial partial parse_error
assert_evidence_case exec error exec_error
assert_evidence_case timeout timeout timeout
assert_evidence_case missing unsupported tool_unavailable
test_evidence_cooldown
test_evidence_persistence
