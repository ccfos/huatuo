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

# Verify partition normalization, retired results and replacement identities.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_io.sh"
io_test_isolate_run_directory
source "${ROOT_DIR}/integration/config.sh"
source "${ROOT_DIR}/integration/lib_io_health.sh"
source "${ROOT_DIR}/integration/lib_io_health_evidence.sh"

io_health_evidence_init scsi
trap cleanup_evidence EXIT
trap 'log_error "IO evidence interrupted by INT"; exit 130' INT
trap 'log_error "IO evidence interrupted by TERM or execution timeout"; exit 143' TERM
io_health_evidence_prepare partition

assert_evidence_target_normalized() {
	[[ ${evidence_member} != "${evidence_target}" ]] \
		|| fatal "H09 normalization requires a partition: member=${evidence_member} target=${evidence_target}"
	assert_evidence_command_count 1 "H09 evidence command did not normalize the member partition to its owned parent"
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
	wait_until 20 0.2 io_health_evidence_saved ok || fatal "H09 old identity did not establish successful evidence"
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
	wait_until 10 0.2 io_health_member_event_increased faulty "${faults_before}" || fatal "H09 old identity cooldown event is missing"
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
	wait_until 20 0.2 io_health_member_event_increased faulty "${evidence_before}" ok \
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
	wait_until 3 0.1 io_health_evidence_command_started || fatal "H09 gated evidence command did not start"
	tool_pid=$(jq -r --arg target "/dev/${old_target}" \
		'select(.phase == "start" and any(.args[]; . == $target)) | .pid' "${evidence_case_dir}/commands.jsonl")
	retire_old_identity
	kill -0 "${tool_pid}" 2> /dev/null \
		|| skip "H09 BLOCKED: device retirement exceeded the external evidence command's active window"
	touch "${evidence_case_dir}/release"
	wait_until 20 0.2 io_health_evidence_saved unsupported || fatal "H09 retired target did not produce an unsupported evidence result"
	assert_retired_identity_output "${old_member}"

	ended=$(jq -s --arg target "/dev/${old_target}" \
		'[.[] | select(.phase == "end" and any(.args[]; . == $target))][0].monotonic' "${evidence_case_dir}/commands.jsonl")
	assert_evidence_command_completed "${ended}" "H09 old command has no completion evidence"

	recreate_identity
	before=$(command_count)
	assert_io_health_md_observer_ready "${EVIDENCE_EVENT}" || fatal "replacement MD observer is not ready"
	assert_evidence_command_count "${before}" "H09 observer check unexpectedly collected member evidence"
	fault_member
	wait_until 20 0.2 io_health_evidence_saved ok || fatal "H09 replacement member lacks successful evidence; inspect saved events and command journal"
	assert_evidence_command_count "$((before + 1))" "H09 new identity did not execute its own evidence command"
	finish_case
	log_info "H09: old evidence rejected after retirement; distinct INQUIRY identity ${old_vendor}->${new_vendor} recovered (target ${old_target}->${evidence_target})"
}

test_identity
