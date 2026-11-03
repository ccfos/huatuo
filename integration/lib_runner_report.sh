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

set -euo pipefail

# Only the supervising runner writes the report; test processes provide reason files.
report_time() {
	date --iso-8601=seconds
}

report_clock() {
	local uptime unused
	read -r uptime unused < /proc/uptime
	printf '%s\n' "${uptime}"
}

report_update() {
	local filter=$1
	shift
	filter+=" | "' .execution_counts = reduce .test_executions[] as $test
		({total: (.test_executions | length), passed: 0, failed: 0, skipped: 0};
		 if $test.result == null then . else .[$test.result] += 1 end)'
	if ! jq "$@" "${filter}" "${RUNNER_OUTPUT_DIR}/test_run_results.json" > "${RUNNER_OUTPUT_DIR}/test_run_results.json.tmp"; then
		log_error "cannot serialize test report: ${RUNNER_OUTPUT_DIR}/test_run_results.json"
		return 1
	fi
	mv -- "${RUNNER_OUTPUT_DIR}/test_run_results.json.tmp" "${RUNNER_OUTPUT_DIR}/test_run_results.json"
}

report_init() {
	local suite=$1 repeats=$2
	shift 2
	command -v jq > /dev/null || {
		log_error "test reports require jq; install jq"
		return 1
	}
	mkdir -p "${RUNNER_OUTPUT_DIR}"
	RUNNER_STARTED_CLOCK=$(report_clock)
	export RUNNER_STARTED_CLOCK
	jq -n --arg suite "${suite}" --argjson repeats "${repeats}" --arg started "$(report_time)" \
		--args '{
		 schema_version: 1, suite: $suite, runs_per_test: $repeats,
		 status: "running", exit_code: null, reason: null,
		 started_at: $started, finished_at: null, duration_seconds: null,
		 execution_counts: {total: (($ARGS.positional | length) * $repeats), passed: 0, failed: 0, skipped: 0},
		 test_executions: [range(1; $repeats + 1) as $run | $ARGS.positional[] |
		 {test_name: (split("/") | last), run_index: $run, result: null, exit_code: null,
		 reason: null, started_at: null, finished_at: null, duration_seconds: null, logs: null}]
		}' "$@" > "${RUNNER_OUTPUT_DIR}/test_run_results.json.tmp"
	mv -- "${RUNNER_OUTPUT_DIR}/test_run_results.json.tmp" "${RUNNER_OUTPUT_DIR}/test_run_results.json"
}

report_execution_start() {
	local name=$1 run=$2 workspace=$3
	report_update '.test_executions |= map(if .test_name == $name and .run_index == $run then
		.started_at = $time | .logs = {stdout: ($dir + "/stdout.log"), stderr: ($dir + "/stderr.log")}
		else . end)' --arg name "${name}" --argjson run "${run}" --arg time "$(report_time)" --arg dir "${workspace##*/}"
}

report_execution_finish() {
	local name=$1 run=$2 result=$3 code=$4 reason=$5 started=$6
	report_update '.test_executions |= map(if .test_name == $name and .run_index == $run then
		.result = $result | .exit_code = $code | .reason = (if $reason == "" then null else $reason end) |
		.finished_at = $time | .duration_seconds = ((($now - $started) * 100 | round) / 100) else . end)' \
		--arg name "${name}" --argjson run "${run}" --arg result "${result}" --argjson code "${code}" \
		--arg reason "${reason}" --arg time "$(report_time)" --argjson now "$(report_clock)" --argjson started "${started}" || return 1
	local message
	message=$(jq -r --arg name "${name}" --argjson run "${run}" '
		.runs_per_test as $runs | .test_executions[] | select(.test_name == $name and .run_index == $run) |
		({passed: "PASS", failed: "FAIL", skipped: "SKIP"}[.result]) + ": " + .test_name +
		" (\(.run_index)/\($runs)) duration=\(.duration_seconds)s" +
		(if .result == "skipped" or .reason == null then "" else ": " + .reason end)
	' "${RUNNER_OUTPUT_DIR}/test_run_results.json") || return 1
	log_info "${message}"
	if [[ ${result} == skipped && -n ${reason} ]]; then
		log_info "SKIP: ${reason}"
	fi
}

report_finish() {
	local status=$1 code=$2 reason=$3
	report_update '.status = $status | .exit_code = $code |
		.reason = (if $reason == "" then null else $reason end) |
		.finished_at = $time | .duration_seconds = ((($now - $started) * 100 | round) / 100) |
		.test_executions |= map(if .result == null then .result = "skipped" |
		.reason = (if .started_at == null then "not started: " else "execution interrupted: " end) + $reason
		else . end)' \
		--arg status "${status}" --argjson code "${code}" --arg reason "${reason}" --arg time "$(report_time)" \
		--argjson now "$(report_clock)" --argjson started "${RUNNER_STARTED_CLOCK}"
}

report_summary() {
	local workspace
	# Print diagnostics after workers finish so parallel cases cannot interleave logs.
	while IFS= read -r workspace; do
		dump_text_files "${workspace}" || return 1
	done < <(jq -r '.test_executions[] | select(.result == "failed" and .logs != null) |
		$output + "/" + (.logs.stdout | rtrimstr("/stdout.log"))' \
		--arg output "${RUNNER_OUTPUT_DIR}" "${RUNNER_OUTPUT_DIR}/test_run_results.json")
	jq --arg output "${RUNNER_OUTPUT_DIR}" '
		def short_reason: if . == null then null else split("\n")[0] end;
		.suite as $suite |
		{status, output_dir: $output, results_path: ($output + "/test_run_results.json"),
		 runs_per_test, started_at, finished_at, duration_seconds, execution_counts,
		 skipped_tests: ([.test_executions[] | select(.result == "skipped") |
			$suite + "/" + .test_name] | unique),
		 failed_tests: [.test_executions[] | select(.result == "failed") |
			{name: ($suite + "/" + .test_name), run_index, reason: (.reason | short_reason)}]}
		+ (if .reason == null then {} else {reason: (.reason | short_reason)} end)
	' "${RUNNER_OUTPUT_DIR}/test_run_results.json"
}
