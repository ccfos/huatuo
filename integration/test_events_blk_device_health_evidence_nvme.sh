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

# Compare persisted NVMe evidence with the real private controller responses.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_io.sh"
io_test_isolate_run_directory
source "${ROOT_DIR}/integration/config.sh"
source "${ROOT_DIR}/integration/lib_blk_device_health.sh"
source "${ROOT_DIR}/integration/lib_blk_device_health_evidence.sh"

blk_device_health_evidence_init nvme
trap cleanup_evidence EXIT
trap 'log_error "IO evidence interrupted by INT"; exit 130' INT
trap 'log_error "IO evidence interrupted by TERM or execution timeout"; exit 143' TERM
blk_device_health_evidence_prepare partition

blk_device_health_nvme_evidence_saved() {
	[[ -s ${EVIDENCE_EVENT} ]] || return 1
	jq -s -e --arg member "${evidence_member}" --arg array "${md_device##*/}" '
	any(.[]; .tracer_name == "blk_device_health" and .tracer_type == "event"
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
	wait_until 20 0.2 blk_device_health_nvme_evidence_saved || fatal "H06 NVMe member event lacks protocol evidence from the readable private target"
	assert_nvme_evidence_matches_commands

	finish_case
	log_info "H06/H09: real nvme-cli evidence matches its captured response; partition=${evidence_member}, owned controller=${evidence_target}, NQN=${nvme_nqn}"
}

test_nvme_evidence
