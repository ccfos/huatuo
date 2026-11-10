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

# Exercise both suite entry points with fixture cases and no external services.
set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

readonly RUNNER_FIXTURE="${HUATUO_BAMAI_TEST_TMPDIR}/runner"
require_commands unshare mount findmnt readlink flock ss stat lsns timeout setsid python3
readonly RUNNER_UNSHARE=$(command -v unshare)
runner_parent_uts=$(readlink /proc/self/ns/uts)
runner_parent_mount=$(readlink /proc/self/ns/mnt)
export runner_parent_uts runner_parent_mount

assert_runner_summary() {
	local log=$1
	# Diagnostic files can contain JSON before or after the summary.
	awk '
		/^\{/ {block = ""; collecting = 1}
		collecting {block = block $0 ORS}
		/^\}/ && collecting {
			if (block ~ /"output_dir":/ && block ~ /"results_path":/) last = block
			collecting = 0
		}
		END {printf "%s", last}
	' "${log}" | jq -se '
		if length == 1 and (.[0].results_path | type == "string") then .[0]
		else error("expected a final JSON summary with a results path") end
	' || fatal "invalid runner summary; see ${log}"
}

assert_runner_result() {
	local suite=$1 label=$2 expected_status=$3 expected_summary=$4 status=0
	shift 4
	local invoked_suite=$suite
	if [[ ${1:-} == --suite ]]; then invoked_suite=${2:-}; fi
	local log="${HUATUO_BAMAI_TEST_TMPDIR}/${suite}-${label}.log" summary
	local -a signal_reset_launcher
	runner_build_signal_reset_launcher signal_reset_launcher
	# PID namespace teardown also reclaims workers that created new process groups.
	# Keep the reset after unshare --fork and before Bash: ignored signals inherited at
	# shell startup cannot be restored by its traps. The fixture launcher needs
	# its own reset even when production workers reset theirs, or cancellation
	# may time out instead of exercising the runner's signal handling.
	HUATUO_TEST_CGROUP_MODE=stale INTEGRATION_JOBS=1 timeout --kill-after=2 "${runner_fixture_timeout:-30}" \
		"${RUNNER_UNSHARE}" --mount --pid --fork --kill-child=KILL --mount-proc \
		"${signal_reset_launcher[@]}" \
		bash -c 'export runner_parent_pid=$(readlink /proc/self/ns/pid); cd /; exec bash "$@"' \
		huatuo-runner-fixture "${RUNNER_FIXTURE}/${suite}/run.sh" "$@" > "${log}" 2>&1 || status=$?
	if [[ ${status} == 124 || ${status} == 137 ]]; then
		cat "${log}"
		fatal "${suite}/${label}: runner timed out after ${runner_fixture_timeout:-30} seconds; fixture processes were terminated"
	fi
	if [[ ${status} != "${expected_status}" ]]; then
		cat "${log}"
		fatal "${suite}/${label}: exit=${status}, expected exit=${expected_status}; see ${log}"
	fi
	if [[ -n ${expected_summary} ]]; then
		summary=$(assert_runner_summary "${log}")
		jq -e --argjson expected "${expected_summary}" '.execution_counts == $expected' <<< "${summary}" > /dev/null \
			|| fatal "${suite}/${label}: expected counts=${expected_summary}, actual=$(jq -c .execution_counts <<< "${summary}"); see ${log}"
		local report
		report=$(jq -r .results_path <<< "${summary}")
		[[ $(grep -Fc "\"results_path\": \"${report}\"" "${log}") == 1 ]] || fatal "${suite}/${label}: summary was printed more than once"
		if [[ ${invoked_suite} == integration && ${expected_status} == 0 ]] \
			&& jq -e '.execution_counts.total == .execution_counts.passed' <<< "${summary}" > /dev/null; then
			local summary_start
			summary_start=$(grep -n '^{' "${log}" | tail -n 1)
			[[ $(tail -n +"${summary_start%%:*}" "${log}") == "${summary}" ]] \
				|| fatal "${suite}/${label}: output follows the successful JSON summary; see ${log}"
			[[ ! -e ${report%/*} ]] || fatal "${suite}/${label}: successful integration directory was retained: ${report%/*}"
			return 0
		fi
		[[ -d ${report%/*} ]] || fatal "${suite}/${label}: diagnostic directory was removed: ${report%/*}"
		jq -e --slurpfile report "${report}" '
			. as $summary | $report[0] | .suite as $suite |
			$summary.execution_counts == .execution_counts and
			$summary.status == (if .status == "completed" then "passed" elif .status == "aborted" then "failed" else .status end) and
			$summary.runs_per_test == .runs_per_test and
			$summary.started_at == .started_at and $summary.finished_at == .finished_at and
			$summary.duration_seconds == .duration_seconds and
			$summary.results_path == ($summary.output_dir + "/test_run_results.json") and
			($summary | has("suite") or has("exit_code") or has("report") or has("summary") or has("reason") | not) and
			$summary.skipped_tests == ([.test_executions[] | select(.result == "skipped") | $suite + "/" + .test_name] | unique) and
			([$summary.failed_tests[] | {name, run_index, reason}] ==
			 [.test_executions[] | select(.result == "failed") | {name: ($suite + "/" + .test_name), run_index, reason: (.reason | if . == null then null else split("\n")[0] end)}])
		' <<< "${summary}" > /dev/null || fatal "${suite}/${label}: summary disagrees with report: ${report}"
		jq -e --argjson code "${expected_status}" '
			.exit_code == $code and .status != "running" and
			(.execution_counts.total == (.test_executions | length)) and
			(.execution_counts.total == (.execution_counts.passed + .execution_counts.failed + .execution_counts.skipped)) and
			(all(.test_executions[]; .result != null)) and
			([.test_executions[] | [.test_name, .run_index]] | length == (unique | length))
		' "${report}" > /dev/null || fatal "${suite}/${label}: invalid execution report: ${report}"
		while IFS= read -r file; do
			[[ -f ${report%/*}/${file} ]] || fatal "${suite}/${label}: missing retained log: ${file}"
		done < <(jq -r '.test_executions[] | select(.logs != null) | .logs[]' "${report}")
		if [[ ${label} == repeated_skip || ${label} == mixed ]]; then
			grep -Eq "^\\[[^]]+\\]\\[${suite^^}\\] SKIP: test_skip.sh \\([0-9]+/[0-9]+\\) duration=[0-9.]+s$" "${log}" \
				|| fatal "skip summary was not logged separately"
			grep -Eq "^\\[[^]]+\\]\\[${suite^^}\\] SKIP: command is not installed: huatuo-runner-missing-command$" "${log}" \
				|| fatal "skip reason was not logged separately"
		fi
	else
		[[ -z $(sed -n '/^{/,/^}/p' "${log}") ]] || fatal "${suite}/${label}: unexpected JSON summary; see ${log}"
	fi
	if grep -q 'PASS: test_skip.sh' "${log}"; then
		fatal "${suite}/${label}: skipped case reported as passed"
	fi
}

runner_prepare_fixture() {
	mkdir -p "${RUNNER_FIXTURE}"/{integration,e2e,_output}
	cp "${ROOT_DIR}/integration/"{run.sh,env.sh,lib.sh,lib_cgroup.sh,lib_runner.sh,lib_runner_report.sh,runner_worker.sh} "${RUNNER_FIXTURE}/integration/"
	cp "${ROOT_DIR}/e2e/"{run.sh,lib.sh} "${RUNNER_FIXTURE}/e2e/"
	mkdir -p "${RUNNER_FIXTURE}/_output/conf"
	printf '[HTTPServer]\n' > "${RUNNER_FIXTURE}/_output/conf/huatuo-bamai.conf"
	# Fixture cases have their own declarations; production entries name absent files.
	printf '\nrunner_test_namespaces=()\n' >> "${RUNNER_FIXTURE}/integration/lib_runner.sh"
	cat >> "${RUNNER_FIXTURE}/integration/lib_runner.sh" << 'METADATA'
for table in runner_test_groups runner_shared_storage_tests; do
 declare -n fixture_entries=$table
 for key in "${!fixture_entries[@]}"; do
  [[ -f $ROOT_DIR/$key ]] || unset 'fixture_entries[$key]'
 done
 unset -n fixture_entries
done
METADATA
	# Keep result classification and workspace cleanup real; replace service I/O.
	cat >> "${RUNNER_FIXTURE}/integration/lib.sh" << 'EOF'
ss() {
 readlink /proc/self/ns/net > "${HUATUO_BAMAI_TEST_TMPDIR}/port-namespace"
 command ss "$@"
}
huatuo_bamai_start() {
    : > "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log"
    if [[ ${RUNNER_LOG_FAIL:-0} == 1 ]]; then
        printf 'level=error fixture failure\n' > "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log"
    fi
    readlink /proc/self/ns/mnt > "${HUATUO_BAMAI_TEST_TMPDIR}/service-namespace"
    [[ ${RUNNER_START_FAIL:-0} == 0 ]] || fatal "fixture startup failure"
}
huatuo_bamai_stop() {
 local workspace=${1:-$HUATUO_BAMAI_TEST_TMPDIR}
 if [[ -f $workspace/service-namespace ]]; then
  [[ $(readlink /proc/self/ns/mnt) == "$(< "$workspace/service-namespace")" ]] || return 1
 fi
 [[ ${RUNNER_STOP_FAIL:-0} == 0 ]]
}
huatuo_apiserver_stop() { return 0; }
EOF

}

assert_runner_suites() {
	for runner_suite in integration e2e; do
		rm -f "${RUNNER_FIXTURE}/skip-cleaned"
		cat > "${RUNNER_FIXTURE}/${runner_suite}/test_pass.sh" << 'EOF'
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
require_commands bash "${ROOT_DIR}/integration/run.sh"
require_build_output
assert_runner_case() {
source "${ROOT_DIR}/integration/lib_cgroup.sh"
assert_eq "${HUATUO_TEST_CGROUP_MODE}" "$(cgroup_mode)" "runner refreshes and exports cgroup mode"
[[ $(hostname) == huatuo-dev ]] || fatal "runner hostname was not isolated"
[[ $(readlink /proc/self/ns/uts) != "${runner_parent_uts}" ]] || fatal "runner reused the parent UTS namespace"
[[ $(readlink /proc/self/ns/mnt) != "${runner_parent_mount}" ]] || fatal "runner reused the parent mount namespace"
[[ $(findmnt -n -o PROPAGATION /) == private ]] || fatal "runner root mount is not private"
[[ ! -e "${HUATUO_BAMAI_TEST_TMPDIR}/case-marker" ]] || fatal "runner reused a case workspace"
touch "${HUATUO_BAMAI_TEST_TMPDIR}/case-marker"
if [[ ${TEST_LOG_TAG} == E2E ]]; then
    [[ -f "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" ]] || fatal "e2e baseline daemon was not started"
else
    [[ ! -e "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" ]] || fatal "integration started an unsolicited daemon"
fi
}
assert_runner_case
EOF
		cat > "${RUNNER_FIXTURE}/${runner_suite}/test_skip.sh" << 'EOF'
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
trap 'touch "${ROOT_DIR}/skip-cleaned"' EXIT
require_commands huatuo-runner-missing-command
EOF
		assert_runner_result "${runner_suite}" mixed 0 '{"total":2,"passed":1,"skipped":1,"failed":0}'
		[[ -f "${RUNNER_FIXTURE}/skip-cleaned" ]] || fatal "${runner_suite}: skip bypassed EXIT cleanup"
		assert_runner_result "${runner_suite}" repeated_pass 0 '{"total":2,"passed":2,"skipped":0,"failed":0}' test_pass.sh 2
		assert_runner_result "${runner_suite}" repeated_skip 0 '{"total":2,"passed":0,"skipped":2,"failed":0}' test_skip.sh 2
		RUNNER_STOP_FAIL=1 assert_runner_result "${runner_suite}" cleanup_failure 1 '{"total":1,"passed":0,"skipped":0,"failed":1}' test_skip.sh

		cat > "${RUNNER_FIXTURE}/${runner_suite}/test_zz_failure.sh" << 'EOF'
set -euo pipefail
false
EOF
		cat > "${RUNNER_FIXTURE}/${runner_suite}/test_zzz_not_run.sh" << 'EOF'
touch "${ROOT_DIR}/unexpected-run"
EOF
		assert_runner_result "${runner_suite}" failure 1 '{"total":4,"passed":1,"skipped":2,"failed":1}'
		[[ ! -e "${RUNNER_FIXTURE}/unexpected-run" ]] || fatal "${runner_suite}: continued after failure"
	done
	RUNNER_START_FAIL=1 assert_runner_result e2e startup_failure 1 '{"total":1,"passed":0,"skipped":0,"failed":1}' test_pass.sh
	local report
	report=$(assert_runner_summary "${HUATUO_BAMAI_TEST_TMPDIR}/e2e-startup_failure.log" | jq -r .results_path)
	jq -e '.test_executions[0] | .exit_code == null and .result == "failed" and .reason == "fixture startup failure"' "${report}" > /dev/null || fatal "startup failure lost its reason or fabricated a process exit code"
	report=$(assert_runner_summary "${HUATUO_BAMAI_TEST_TMPDIR}/integration-cleanup_failure.log" | jq -r .results_path)
	jq -e '.test_executions[0] | .exit_code == 77 and .result == "failed" and (.reason | contains("failed to stop huatuo-bamai"))' "${report}" > /dev/null || fatal "cleanup failure overwrote the process exit code"
	RUNNER_LOG_FAIL=1 assert_runner_result e2e log_failure 1 '{"total":1,"passed":0,"skipped":0,"failed":1}' test_skip.sh
	# Inject a missing network prerequisite while retaining real E2E log checks.
	printf '\nrunner_test_namespaces=(["e2e/test_pass.sh"]="mount net")\n' >> "${RUNNER_FIXTURE}/integration/lib_runner.sh"
	grep -Fq "require_commands ip" "${RUNNER_FIXTURE}/integration/runner_worker.sh" || fatal "missing network prerequisite injection point"
	sed -i 's/require_commands ip/require_commands huatuo-runner-missing-command/' "${RUNNER_FIXTURE}/integration/runner_worker.sh"
	assert_runner_result e2e preparation_skip 0 '{"total":1,"passed":0,"skipped":1,"failed":0}' test_pass.sh
	report=$(assert_runner_summary "${HUATUO_BAMAI_TEST_TMPDIR}/e2e-preparation_skip.log" | jq -r .results_path)
	jq -e '.test_executions[0] | .result == "skipped" and .exit_code == null and .reason == "command is not installed: huatuo-runner-missing-command"' "$report" > /dev/null || fatal "E2E preparation skip lost its reason or fabricated a process exit code"
	RUNNER_STOP_FAIL=1 assert_runner_result e2e preparation_cleanup_failure 1 '{"total":1,"passed":0,"skipped":0,"failed":1}' test_pass.sh
	cp "${ROOT_DIR}/integration/runner_worker.sh" "${RUNNER_FIXTURE}/integration/runner_worker.sh"
	printf '\nrunner_test_namespaces=()\n' >> "${RUNNER_FIXTURE}/integration/lib_runner.sh"
	mv "${RUNNER_FIXTURE}/_output" "${RUNNER_FIXTURE}/saved-output"
	assert_runner_result integration missing_output 1 '{"total":1,"passed":0,"skipped":1,"failed":0}' test_pass.sh
	mv "${RUNNER_FIXTURE}/saved-output" "${RUNNER_FIXTURE}/_output"
	mkdir -p "${RUNNER_FIXTURE}/bin"
	printf '#!/bin/sh\nexit 23\n' > "${RUNNER_FIXTURE}/bin/unshare"
	chmod +x "${RUNNER_FIXTURE}/bin/unshare"
	PATH="${RUNNER_FIXTURE}/bin:${PATH}" assert_runner_result integration namespace_failure 23 '{"total":1,"passed":0,"skipped":1,"failed":0}' test_pass.sh
	assert_runner_result integration explicit_suite 0 '{"total":1,"passed":1,"skipped":0,"failed":0}' --suite integration test_pass.sh
	cp "${RUNNER_FIXTURE}/e2e/test_pass.sh" "${RUNNER_FIXTURE}/e2e/test_e2e_only.sh"
	assert_runner_result integration e2e_suite 0 '{"total":2,"passed":2,"skipped":0,"failed":0}' --suite e2e test_e2e_only.sh 2
	assert_runner_result integration missing_suite 2 '' --suite
	assert_runner_result integration invalid_suite 2 '' --suite unknown
	assert_runner_result integration missing_jobs 2 '' -j
	assert_runner_result integration invalid_jobs 2 '' -j 0
	assert_runner_result integration invalid_jobs_large 2 '' -j 9
	assert_runner_result integration e2e_parallel 2 '' --suite e2e -j 2
	assert_runner_result integration invalid_repeat 2 '' --suite e2e test_pass.sh 0
	assert_runner_result integration invalid_path 2 '' --suite e2e ../e2e/test_pass.sh
	assert_runner_result e2e invalid_repeat 2 '' test_pass.sh 0
	assert_runner_result integration repeat_limit 2 '' test_pass.sh 1001
	assert_runner_result integration repeat_overflow 2 '' test_pass.sh 99999999999999999999
	PATH="${RUNNER_FIXTURE}/bin:${PATH}" assert_runner_result integration repeat_boundary 23 '{"total":1000,"passed":0,"skipped":1000,"failed":0}' test_pass.sh 1000
	local index
	for ((index = 1; index <= 11; index++)); do
		printf 'exit 0\n' > "${RUNNER_FIXTURE}/integration/test_limit_${index}.sh"
	done
	assert_runner_result integration execution_limit 2 '' '' 1000
	for ((index = 1; index <= 11; index++)); do
		rm "${RUNNER_FIXTURE}/integration/test_limit_${index}.sh"
	done

}

assert_runner_prerequisites() {
	# A path containing spaces must be checked as one executable, not split into words.
	runner_tool="${RUNNER_FIXTURE}/test executable"
	printf '#!/usr/bin/env bash\nexit 0\n' > "${runner_tool}"
	chmod +x "${runner_tool}"
	require_commands "${runner_tool}"
	chmod -x "${runner_tool}"
	runner_status=0
	(require_commands "${runner_tool}") > "${HUATUO_BAMAI_TEST_TMPDIR}/nonexecutable.log" 2>&1 || runner_status=$?
	[[ ${runner_status} == 77 ]] || fatal "nonexecutable path did not skip: exit=${runner_status}"
	runner_status=0
	(require_commands "${RUNNER_FIXTURE}") > "${HUATUO_BAMAI_TEST_TMPDIR}/directory.log" 2>&1 || runner_status=$?
	[[ ${runner_status} == 77 ]] || fatal "directory accepted as an executable: exit=${runner_status}"
	runner_status=0
	(ROOT_DIR="${RUNNER_FIXTURE}/missing" require_build_output) > "${HUATUO_BAMAI_TEST_TMPDIR}/missing-output.log" 2>&1 || runner_status=$?
	[[ ${runner_status} == 1 ]] || fatal "missing build output did not fail: exit=${runner_status}"

}

assert_runner_parallel_isolation() {
	# Parallel cases have independent PID files, toolstream paths, and HTTP ports.
	rm -f "${RUNNER_FIXTURE}/integration/"test_*.sh
	runner_socket_id=""
	if [[ -S /var/run/docker.sock ]]; then runner_socket_id=$(stat -Lc '%d:%i' /var/run/docker.sock); fi
	runner_nested_mount="" runner_nested_id=""
	runner_runtime_root=$(readlink -f /var/run)
	while IFS= read -r runner_mount; do
		[[ ${runner_mount} == "${runner_runtime_root}/"* ]] || continue
		runner_nested_mount=${runner_mount#"${runner_runtime_root}"}
		runner_nested_id=$(stat -Lc '%d:%i' "${runner_mount}")
		break
	done < <(findmnt -rn -o TARGET)
	export runner_socket_id runner_nested_mount runner_nested_id
	cat > "${RUNNER_FIXTURE}/integration/test_metrics.sh" << 'EOF'
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
name=${0##*/}
exec 8>/var/run/huatuo-bamai.pid
exec 9>/var/run/huatuo-apiserver.pid
assert_runner_isolation() {
flock -n 8 || fatal "bamai PID lock was shared between workers"
flock -n 9 || fatal "apiserver PID lock was shared between workers"
for file in huatuo-bamai.pid huatuo-apiserver.pid huatuo-toolstream.sock; do
 printf '%s' "$name" > "/var/run/$file"
done
if [[ -n $runner_socket_id ]]; then
 [[ $(stat -Lc '%d:%i' /var/run/docker.sock) == "$runner_socket_id" ]] || fatal "host Docker socket was hidden"
fi
if [[ -n $runner_nested_mount ]]; then
 [[ $(stat -Lc '%d:%i' "/var/run$runner_nested_mount") == "$runner_nested_id" ]] || fatal "nested runtime mount was hidden"
fi
printf '%s\n' "$HUATUO_BAMAI_ADDR" > "$ROOT_DIR/$name.address"
touch "$ROOT_DIR/$name.started"
peer=test_metrics.sh
[[ $name != test_metrics.sh ]] || peer=test_node_config_api.sh
wait_until 5 0.05 test -f "$ROOT_DIR/$peer.started"
for file in huatuo-bamai.pid huatuo-apiserver.pid huatuo-toolstream.sock; do
 [[ $(< "/var/run/$file") == "$name" ]] || fatal "worker overwrote another case runtime file"
done
}
assert_runner_isolation
EOF
	cp "${RUNNER_FIXTURE}/integration/"test_{metrics,node_config_api}.sh
	assert_runner_result integration parallel_isolation 0 '{"total":2,"passed":2,"skipped":0,"failed":0}' -j 2
	[[ $(cat "${RUNNER_FIXTURE}/"*.address | sort -u | wc -l) == 2 ]] || fatal "parallel workers shared a daemon port"

}

assert_runner_storage_lifecycle() {
	# Keep the earlier minimal fixtures independent of storage helpers.
	rm -f "${RUNNER_FIXTURE}/integration/"test_*.sh
	cp "${ROOT_DIR}/integration/lib_storage.sh" "${RUNNER_FIXTURE}/integration/"
	cat >> "${RUNNER_FIXTURE}/integration/lib_storage.sh" << 'EOF'
docker() {
    case $1 in
    run) printf 'start\n' >> "${ROOT_DIR}/storage-lifecycle"; printf 'fixture-es\n';;
    port) printf '127.0.0.1:19200\n';;
    rm) printf 'stop\n' >> "${ROOT_DIR}/storage-lifecycle";;
    logs) printf 'fixture storage diagnostics\n';;
    image) return 0;;
    *) return 1;;
    esac
}
storage_ready() { return 0; }
EOF
	cat > "${RUNNER_FIXTURE}/integration/test_apiserver_continuous_profiling_api.sh" << 'EOF'
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_storage.sh"
trap 'status=$?; storage_stop || status=1; exit "$status"' EXIT
storage_start
printf '%s\n' "$(storage_test_index profiles)" >> "${ROOT_DIR}/storage-indices"
case ${runner_storage_mode} in
skip) skip "fixture skip after storage startup";;
failure) fatal "fixture failure after storage startup";;
esac
EOF
	cp "${RUNNER_FIXTURE}/integration/"test_apiserver_continuous_profiling_{api,control}.sh
	for runner_mode in success skip failure; do
		export runner_storage_mode=${runner_mode}
		: > "${RUNNER_FIXTURE}/storage-lifecycle"
		: > "${RUNNER_FIXTURE}/storage-indices"
		case ${runner_mode} in
		success) assert_runner_result integration shared_storage 0 '{"total":2,"passed":2,"skipped":0,"failed":0}' ;;
		skip) assert_runner_result integration shared_storage_skip 0 '{"total":2,"passed":0,"skipped":2,"failed":0}' ;;
		failure) assert_runner_result integration shared_storage_failure 1 '{"total":2,"passed":0,"skipped":1,"failed":1}' ;;
		esac
		[[ $(grep -c '^start$' "${RUNNER_FIXTURE}/storage-lifecycle") == 1 ]] || fatal "${runner_mode}: shared storage did not start once"
		[[ $(grep -c '^stop$' "${RUNNER_FIXTURE}/storage-lifecycle") == 1 ]] || fatal "${runner_mode}: shared storage did not stop once"
		if [[ ${runner_mode} == success ]]; then
			[[ $(sort -u "${RUNNER_FIXTURE}/storage-indices" | wc -l) == 2 ]] || fatal "shared storage reused an index"
		fi
	done
	export runner_storage_mode=success
	: > "${RUNNER_FIXTURE}/storage-lifecycle"
	assert_runner_result integration repeated_storage 0 '{"total":2,"passed":2,"skipped":0,"failed":0}' test_apiserver_continuous_profiling_api.sh 2
	[[ $(grep -c '^start$' "${RUNNER_FIXTURE}/storage-lifecycle") == 2 ]] || fatal "repeated storage reused the previous round"
	[[ $(grep -c '^stop$' "${RUNNER_FIXTURE}/storage-lifecycle") == 2 ]] || fatal "repeated storage cleanup did not run"
	unset runner_storage_mode

}

assert_runner_reports() {
	cat > "${RUNNER_FIXTURE}/integration/test_report.sh" << 'EOF'
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
printf 'stdout marker\n'
printf 'stderr marker\n' >&2
case ${REPORT_MODE} in
skip) (fatal 'expected child failure') || true; skip $'missing "runtime"\n诊断路径: C:\\tmp';;
fail)
 printf '%66000s' '' | tr ' ' x > "$HUATUO_BAMAI_TEST_TMPDIR/large.log"
 printf '\nend marker\n' >> "$HUATUO_BAMAI_TEST_TMPDIR/large.log"
 printf '{\n  "results_path": "diagnostic-only"\n}\n' > "$HUATUO_BAMAI_TEST_TMPDIR/response.json"
 printf '[1, {"nested": true}]\n' > "$HUATUO_BAMAI_TEST_TMPDIR/array.json"
 printf 'false\n' > "$HUATUO_BAMAI_TEST_TMPDIR/false.json"
 printf 'null\n' > "$HUATUO_BAMAI_TEST_TMPDIR/null.json"
 printf '{invalid\n' > "$HUATUO_BAMAI_TEST_TMPDIR/invalid.json"
 cp "$HUATUO_BAMAI_TEST_TMPDIR/large.log" "$HUATUO_BAMAI_TEST_TMPDIR/large.json"
 printf 'source marker\n' > "$HUATUO_BAMAI_TEST_TMPDIR/test_case.sh"
 printf 'output marker\n' > "$HUATUO_BAMAI_TEST_TMPDIR/tool.out"
 printf 'binary\0marker\n' > "$HUATUO_BAMAI_TEST_TMPDIR/binary.log"
 printf 'config marker\n' > "$HUATUO_BAMAI_TEST_TMPDIR/bamai.conf"
 printf 'tool error marker\n' > "$HUATUO_BAMAI_TEST_TMPDIR/tool.err"
 fatal $'fixture failed: "runtime"\n诊断路径: C:\\tmp';;
namespace) run_test_step unshare --pid --fork --mount-proc bash -c 'source "$ROOT_DIR/integration/lib.sh"; skip "namespace dependency missing"';;
step_skip) run_test_step bash -euc 'source "$ROOT_DIR/integration/lib.sh"; skip "step dependency missing"';;
step_fail) run_test_step bash -euc 'source "$ROOT_DIR/integration/lib.sh"; fatal "step failed"';;
step_code) run_test_step bash -c 'exit 42';;
command_failure) false;;
function_failure) fail_command() { false; }; fail_command;;
pipeline_failure) false | cat;;
caught_success) (fatal 'caught') || true; set +e; false; set -e;;
caught_child) bash -c 'source "$ROOT_DIR/integration/lib.sh"; fatal "expected child error"' || true; false;;
caught_subshell) (fatal 'expected subshell error') || true; false;;
cancel | cancel_failure)
 (fatal 'expected child failure') || true
 [[ $REPORT_MODE != cancel_failure ]] || trap 'exit 42' TERM
 sleep 30 &
 child=$!
 trap 'kill "$child" 2>/dev/null || true; wait "$child" 2>/dev/null || true' EXIT
 touch "${ROOT_DIR}/report-ready"
 wait "$child";;
esac
EOF
	local mode report stdout stderr
	for mode in pass skip fail caught_success; do
		local code=0 counts='{"total":1,"passed":1,"skipped":0,"failed":0}'
		case ${mode} in
		skip) counts='{"total":1,"passed":0,"skipped":1,"failed":0}' ;;
		fail) code=1 counts='{"total":1,"passed":0,"skipped":0,"failed":1}' ;;
		esac
		REPORT_MODE=${mode} assert_runner_result integration "report-${mode}" "${code}" "${counts}" test_report.sh
		report=$(assert_runner_summary "${HUATUO_BAMAI_TEST_TMPDIR}/integration-report-${mode}.log" | jq -r .results_path)
		if [[ ${mode} == pass || ${mode} == caught_success ]]; then
			[[ ! -e ${report%/*} ]] || fatal "${mode}: successful integration directory was retained"
			continue
		fi
		stdout=${report%/*}/$(jq -r '.test_executions[0].logs.stdout' "${report}")
		stderr=${report%/*}/$(jq -r '.test_executions[0].logs.stderr' "${report}")
		grep -Fxq 'stdout marker' "${stdout}" || fatal "stdout was not retained"
		grep -Fxq 'stderr marker' "${stderr}" || fatal "stderr was not retained"
		! grep -Fq 'stderr marker' "${stdout}" || fatal "stderr leaked into stdout"
		jq -e 'all(.test_executions[]; .started_at != null and .finished_at != null and .duration_seconds >= 0)' "${report}" > /dev/null || fatal "execution timestamps missing"
		if [[ ${mode} == fail ]]; then
			local log="${HUATUO_BAMAI_TEST_TMPDIR}/integration-report-${mode}.log"
			assert_runner_summary "${log}" | jq -e '
				[.failed_tests[] | {name, run_index, reason}] ==
				[{name: "integration/test_report.sh", run_index: 1,
				 reason: "fixture failed: \"runtime\""}] and
				(.failed_tests[0].logs | any(endswith("/execution.log"))) and
				(.failed_tests[0].logs | any(endswith("/response.json"))) and
				(.failed_tests[0].logs | any(endswith("/large.log"))) and
				(.failed_tests[0].logs | all(test("/(reason\\.[^/]*|test_case.sh|binary.log|test_run_results.json)$") | not))
			' > /dev/null || fatal "failure summary lost its identity or diagnostic paths"
			local marker
			grep -Fq 'Failed: integration/test_report.sh (run 1/1)' "${log}" || fatal "failure heading missing"
			grep -Eq '^--- [^ ]+/large.log \(64KB\) ---$' "${log}" || fatal "truncated log heading missing"
			for marker in 'stdout marker' 'stderr marker' 'fixture failed: "runtime"' '诊断路径: C:\tmp' \
				'end marker' '"results_path": "diagnostic-only"' 'exit_code=1' 'output marker' 'config marker' 'tool error marker'; do
				grep -Fq "$marker" "${log}" || fatal "failure diagnostics omitted $marker"
			done
			! grep -Fq -- '----- FILE (' "$log" || fatal "obsolete raw file diagnostics were printed"

		fi
		if [[ ${mode} == skip ]]; then
			jq -e --arg reason $'missing "runtime"\n诊断路径: C:\\tmp' '.test_executions[0] | .reason == $reason and .exit_code == 77' "${report}" > /dev/null || fatal "skip reason was not preserved"
		fi
	done
	REPORT_MODE=namespace assert_runner_result integration report-namespace 0 '{"total":1,"passed":0,"skipped":1,"failed":0}' test_report.sh
	report=$(assert_runner_summary "${HUATUO_BAMAI_TEST_TMPDIR}/integration-report-namespace.log" | jq -r .results_path)
	jq -e '.test_executions[0] | .reason == "namespace dependency missing" and .exit_code == 77' "${report}" > /dev/null || fatal "PID namespace lost the skip reason"
	for mode in step_skip step_fail step_code caught_child caught_subshell command_failure function_failure pipeline_failure; do
		local code=1 counts='{"total":1,"passed":0,"skipped":0,"failed":1}' expected_reason=''
		case ${mode} in
		step_skip) code=0 counts='{"total":1,"passed":0,"skipped":1,"failed":0}' expected_reason='step dependency missing' ;;
		step_fail) expected_reason='step failed' ;;
		step_code) expected_reason='step command bash exited with code 42' ;;
		esac
		REPORT_MODE=${mode} assert_runner_result integration "report-${mode}" "${code}" "${counts}" test_report.sh
		report=$(assert_runner_summary "${HUATUO_BAMAI_TEST_TMPDIR}/integration-report-${mode}.log" | jq -r .results_path)
		case $mode in
		caught_child | caught_subshell | command_failure | function_failure | pipeline_failure)
			local command=false
			[[ $mode != pipeline_failure ]] || command=cat
			jq -e --arg command "$command" '.test_executions[0] | .exit_code == 1 and
				(.reason | test("test_report.sh:[0-9]+: command failed")) and
				(.reason | endswith("(exit 1): " + $command))' "$report" > /dev/null \
				|| fatal "${mode}: missing failing command location or captured a handled error"
			;;
		*) jq -e --arg reason "${expected_reason}" '.test_executions[0].reason == $reason' "${report}" > /dev/null || fatal "${mode}: reason crossed an unhandled execution boundary" ;;
		esac
		if [[ ${mode} == step_code ]]; then
			jq -e '.test_executions[0].exit_code == 42' "${report}" > /dev/null || fatal "step overwrote the command exit code"
		fi
	done
	local signal pid status log
	for signal in INT TERM; do
		for mode in cancel cancel_failure; do
			rm -f "${RUNNER_FIXTURE}/report-ready"
			log="${HUATUO_BAMAI_TEST_TMPDIR}/entry-${signal}.log"
			set -m
			REPORT_MODE=${mode} bash "${RUNNER_FIXTURE}/integration/run.sh" test_report.sh 2 > "${log}" 2>&1 &
			pid=$!
			set +m
			wait_until 5 0.05 test -f "${RUNNER_FIXTURE}/report-ready" || fatal "cancellation fixture not ready"
			kill -s "${signal}" "${pid}"
			wait_until 10 0.05 process_exited "${pid}" || {
				stop_by_process_group "${pid}" 1
				fatal "entry cancellation did not finish"
			}
			status=0
			wait "${pid}" || status=$?
			local expected=143
			[[ ${signal} != INT ]] || expected=130
			[[ ${status} == "${expected}" ]] || fatal "entry cancellation exit=${status}, expected=${expected}"
			report=$(assert_runner_summary "${log}" | jq -r .results_path)
			local skipped=2 failed=0
			[[ ${mode} != cancel_failure ]] || {
				skipped=1
				failed=1
			}
			jq -e --argjson code "${expected}" --argjson skipped "${skipped}" --argjson failed "${failed}" '
			.status == "interrupted" and .exit_code == $code and .execution_counts.skipped == $skipped and .execution_counts.failed == $failed and
			.test_executions[0].started_at != null and .test_executions[0].exit_code != null and
			.test_executions[1].started_at == null and .test_executions[1].exit_code == null and .test_executions[1].logs == null
		' "${report}" > /dev/null || fatal "entry cancellation report is inconsistent"
		done
	done
}

assert_runner_launch_signal() {
	local phase signal report expected
	cp "${RUNNER_FIXTURE}/integration/run.sh" "${RUNNER_FIXTURE}/run.saved"
	cat > "${RUNNER_FIXTURE}/bin/unshare" << 'EOF'
#!/usr/bin/env bash
trap 'exit 143' TERM
trap 'exit 130' INT
sleep 1 &
child=$!
trap 'kill "$child" 2>/dev/null || true; wait "$child" 2>/dev/null || true' EXIT
wait "$child"
touch "$ROOT_DIR/launch-completed"
EOF
	chmod +x "${RUNNER_FIXTURE}/bin/unshare"
	for phase in before_launch before_registration; do
		cp "${RUNNER_FIXTURE}/run.saved" "${RUNNER_FIXTURE}/integration/run.sh"
		if [[ ${phase} == before_launch ]]; then
			grep -Fxq 'set -m' "${RUNNER_FIXTURE}/integration/run.sh" || fatal "missing launch injection point"
			sed -i '/^set -m$/i kill -s "$REPORT_SIGNAL" "$$"' "${RUNNER_FIXTURE}/integration/run.sh"
		else
			grep -Fxq 'supervisor_pid=$!' "${RUNNER_FIXTURE}/integration/run.sh" || fatal "missing PID registration injection point"
			sed -i '/^supervisor_pid=\$!$/i kill -s "$REPORT_SIGNAL" "$$"' "${RUNNER_FIXTURE}/integration/run.sh"
		fi
		for signal in INT TERM; do
			rm -f "${RUNNER_FIXTURE}/launch-completed"
			expected=143
			[[ ${signal} != INT ]] || expected=130
			REPORT_SIGNAL=${signal} PATH="${RUNNER_FIXTURE}/bin:${PATH}" \
				assert_runner_result integration "launch-${phase}-${signal}" "${expected}" '{"total":1,"passed":0,"skipped":1,"failed":0}' test_report.sh
			[[ ! -e ${RUNNER_FIXTURE}/launch-completed ]] || fatal "${phase}/${signal}: child completed after cancellation"
			report=$(assert_runner_summary "${HUATUO_BAMAI_TEST_TMPDIR}/integration-launch-${phase}-${signal}.log" | jq -r .results_path)
			jq -e '.status == "interrupted" and .test_executions[0].started_at == null' "${report}" > /dev/null || fatal "${phase}/${signal}: invalid cancellation report"
		done
	done
	mv "${RUNNER_FIXTURE}/run.saved" "${RUNNER_FIXTURE}/integration/run.sh"
	rm "${RUNNER_FIXTURE}/bin/unshare"
}

assert_runner_late_signal() {
	local signal report expected
	for signal in INT TERM; do
		cat > "${RUNNER_FIXTURE}/bin/unshare" << 'EOF'
#!/usr/bin/env bash
set -eu
/usr/bin/unshare "$@"
[[ $1 != --uts ]] || kill -s "${REPORT_SIGNAL}" "${PPID}"
EOF
		chmod +x "${RUNNER_FIXTURE}/bin/unshare"
		expected=143
		[[ ${signal} != INT ]] || expected=130
		REPORT_MODE=pass REPORT_SIGNAL=${signal} PATH="${RUNNER_FIXTURE}/bin:${PATH}" \
			assert_runner_result integration "late-${signal}" "${expected}" '{"total":1,"passed":1,"skipped":0,"failed":0}' test_report.sh
		report=$(assert_runner_summary "${HUATUO_BAMAI_TEST_TMPDIR}/integration-late-${signal}.log" | jq -r .results_path)
		jq -e '.status == "interrupted" and .test_executions[0].result == "passed" and .test_executions[0].exit_code == 0' "${report}" > /dev/null || fatal "late signal changed the completed test result"
	done
	# Inject after EXIT cleanup has begun, both while reading and writing JSON.
	rm -f "${RUNNER_FIXTURE}/bin/unshare"
	local phase counts
	grep -Fq 'local code=$?' "${RUNNER_FIXTURE}/integration/run.sh" || fatal "missing entry cleanup injection point"
	sed -i '/local code=\$?/a\ printf "%s" "$BASHPID" > "$ROOT_DIR/entry-pid"' "${RUNNER_FIXTURE}/integration/run.sh"
	for phase in read write; do
		if [[ ${phase} == write ]]; then mv "${RUNNER_FIXTURE}/_output" "${RUNNER_FIXTURE}/saved-output"; fi
		for signal in INT TERM; do
			rm -f "${RUNNER_FIXTURE}/signal-sent"
			cat > "${RUNNER_FIXTURE}/bin/jq" << 'EOF'
#!/usr/bin/env bash
set -eu
if [[ ! -e $ROOT_DIR/signal-sent ]] && {
 [[ $REPORT_PHASE == read && $1 == -r && $2 == .status ]] ||
 [[ $REPORT_PHASE == write && $* == *'.status = $status'* ]];
}; then
 touch "$ROOT_DIR/signal-sent"
 kill -s "$REPORT_SIGNAL" "$(< "$ROOT_DIR/entry-pid")"
fi
exec /usr/bin/jq "$@"
EOF
			chmod +x "${RUNNER_FIXTURE}/bin/jq"
			expected=143
			[[ ${signal} != INT ]] || expected=130
			counts='{"total":1,"passed":1,"skipped":0,"failed":0}'
			[[ ${phase} != write ]] || counts='{"total":1,"passed":0,"skipped":1,"failed":0}'
			REPORT_MODE=pass REPORT_PHASE=${phase} REPORT_SIGNAL=${signal} PATH="${RUNNER_FIXTURE}/bin:${PATH}" \
				assert_runner_result integration "final-${phase}-${signal}" "${expected}" "${counts}" test_report.sh
			report=$(assert_runner_summary "${HUATUO_BAMAI_TEST_TMPDIR}/integration-final-${phase}-${signal}.log" | jq -r .results_path)
			jq -e --arg reason "runner received SIG${signal}" '.status == "interrupted" and .reason == $reason' "${report}" > /dev/null || fatal "${phase}/${signal}: finalization lost signal state"
		done
		if [[ ${phase} == write ]]; then mv "${RUNNER_FIXTURE}/saved-output" "${RUNNER_FIXTURE}/_output"; fi
	done
	rm -f "${RUNNER_FIXTURE}/bin/jq"
	sed -i '/printf "%s" "$BASHPID" > "$ROOT_DIR\/entry-pid"/d' "${RUNNER_FIXTURE}/integration/run.sh"

}

assert_runner_report_read_failure() (
	trap 'rm -f "${RUNNER_FIXTURE}/bin/jq"' EXIT
	cat > "${RUNNER_FIXTURE}/bin/jq" << 'EOF'
#!/usr/bin/env bash
set -eu
# Damage the completed results only when the supervisor starts reading them.
if [[ $1 == -r && $2 == .status ]]; then
 printf '%s' "$3" > "$ROOT_DIR/read-failure-path"
 if [[ $REPORT_MODE == missing ]]; then
  rm "$3"
 else
  printf 'invalid\n' > "$3"
 fi
fi
if [[ -f $ROOT_DIR/read-failure-path ]]; then
 printf 'read\n' >> "$ROOT_DIR/read-failure-attempts"
fi
exec /usr/bin/jq "$@"
EOF
	chmod +x "${RUNNER_FIXTURE}/bin/jq"
	local mode log report attempts
	for mode in missing invalid; do
		rm -f "${RUNNER_FIXTURE}/read-failure-path" "${RUNNER_FIXTURE}/read-failure-attempts"
		REPORT_MODE=${mode} PATH="${RUNNER_FIXTURE}/bin:${PATH}" \
			assert_runner_result integration "read-failure-${mode}" 1 '' test_pass.sh
		log="${HUATUO_BAMAI_TEST_TMPDIR}/integration-read-failure-${mode}.log"
		report=$(< "${RUNNER_FIXTURE}/read-failure-path")
		attempts=$(wc -l < "${RUNNER_FIXTURE}/read-failure-attempts")
		[[ ${attempts} == 1 ]] || fatal "${mode}: results read attempts=${attempts}, expected=1; see ${log}"
		grep -Fq "cannot read test results: ${report}" "${log}" \
			|| fatal "${mode}: results read failure omitted the path: ${report}; see ${log}"
	done
)

assert_runner_report_write_failure() (
	local mode=${1:-scheduling}
	local fixture="${RUNNER_FIXTURE}/report-failure-${mode}" pid="" status=0 case_pid child_pid report
	mkdir -p "${fixture}/integration" "${fixture}/_output"
	cp "${RUNNER_FIXTURE}/integration/"{run.sh,lib.sh,lib_cgroup.sh,lib_runner.sh,lib_runner_report.sh,runner_worker.sh} "${fixture}/integration/"
	cp "${RUNNER_FIXTURE}/integration/env.sh" "${fixture}/integration/env.sh"
	# Keep real scheduling and report I/O, while observing resource teardown.
	cat >> "${fixture}/integration/lib.sh" << 'EOF'
huatuo_bamai_stop() {
 if [[ $1 == */test_metrics.* ]]; then touch "$ROOT_DIR/first-cleaned"; fi
}
huatuo_apiserver_stop() { return 0; }
EOF
	cat >> "${fixture}/integration/lib_runner.sh" << 'EOF'
runner_stop_storage() { touch "$ROOT_DIR/storage-stopped"; }
EOF
	cat > "${fixture}/integration/test_metrics.sh" << 'EOF'
set -euo pipefail
source "$ROOT_DIR/integration/lib.sh"
wait_until 5 0.05 test -f "$ROOT_DIR/second-ready"
printf '%s' "$RUNNER_OUTPUT_DIR/test_run_results.json" > "$ROOT_DIR/report-path"
mkdir "$RUNNER_OUTPUT_DIR/test_run_results.json.tmp"
EOF
	cat > "${fixture}/integration/test_node_config_api.sh" << 'EOF'
set -euo pipefail
source "$ROOT_DIR/integration/lib.sh"
printf '%s' "$BASHPID" > "$ROOT_DIR/case-pid"
sleep 30 &
child=$!
printf '%s' "$child" > "$ROOT_DIR/child-pid"
trap 'kill "$child" 2>/dev/null || true; wait "$child" 2>/dev/null || true; touch "$ROOT_DIR/second-cleaned"' EXIT
touch "$ROOT_DIR/second-ready"
wait "$child"
EOF
	local jobs=2
	if [[ ${mode} == cleanup ]]; then
		jobs=3
		# Mark actual EXIT cleanup entry before the second case breaks report I/O.
		grep -Fxq 'runner_cleanup() {' "${fixture}/integration/lib_runner.sh" || fatal "missing runner cleanup injection point"
		sed -i '/^runner_cleanup() {/a\ touch "$ROOT_DIR/cleanup-entered"' "${fixture}/integration/lib_runner.sh"
		cat > "${fixture}/integration/test_metrics.sh" << 'EOF'
set -euo pipefail
source "$ROOT_DIR/integration/lib.sh"
wait_until 5 0.05 test -f "$ROOT_DIR/second-ready"
fatal "initial test failure"
EOF
		cat > "${fixture}/integration/test_node_cloud_events_api.sh" << 'EOF'
set -euo pipefail
source "$ROOT_DIR/integration/lib.sh"
wait_until 5 0.05 test -f "$ROOT_DIR/cleanup-entered"
printf '%s' "$RUNNER_OUTPUT_DIR/test_run_results.json" > "$ROOT_DIR/report-path"
mkdir "$RUNNER_OUTPUT_DIR/test_run_results.json.tmp"
EOF
	fi

	trap '
		if [[ -n ${pid} ]]; then stop_by_process_group "${pid}" 1 || true; fi
		if [[ -f ${fixture}/case-pid ]]; then stop_by_process_group "$(< "${fixture}/case-pid")" 1 || true; fi
	' EXIT
	set -m
	bash "${fixture}/integration/run.sh" -j "${jobs}" > "${fixture}/runner.log" 2>&1 &
	pid=$!
	set +m
	wait_until 10 0.05 process_exited "${pid}" || fatal "report failure prevented runner shutdown"
	wait "${pid}" || status=$?
	pid=""
	[[ ${status} == 1 ]] || fatal "report write failure exit=${status}, expected 1"
	case_pid=$(< "${fixture}/case-pid")
	child_pid=$(< "${fixture}/child-pid")
	! kill -0 "${case_pid}" 2> /dev/null || fatal "report failure left a test running"
	! kill -0 "${child_pid}" 2> /dev/null || fatal "report failure left a workload running"
	[[ -f ${fixture}/second-cleaned && -f ${fixture}/storage-stopped ]] || fatal "report failure bypassed resource cleanup"
	report=$(< "${fixture}/report-path")
	jq -e '.status == "running" and .exit_code == null and any(.test_executions[]; .result == null)' "${report}" > /dev/null || fatal "report failure fabricated final results"
	grep -Fq 'incomplete report:' "${fixture}/runner.log" || fatal "report failure did not identify the partial report"
)

assert_runner_inherited_environment() (
	local fixture=${RUNNER_FIXTURE} suite run log report previous=""
	cat > "${fixture}/integration/test_probe.sh" << 'EOF'
set -euo pipefail
source "$ROOT_DIR/integration/lib.sh"
assert_probe_workspace() {
 [[ $HUATUO_BAMAI_TEST_TMPDIR == "$RUNNER_OUTPUT_DIR"/test_probe.* ]] || fatal "case workspace is outside its invocation directory: $HUATUO_BAMAI_TEST_TMPDIR"
}
assert_probe_workspace
EOF
	cp "${fixture}/integration/test_probe.sh" "${fixture}/e2e/"
	bash -euc '
	 unset __HUATUO_ENV_SH_LOADED RUNNER_OUTPUT_DIR
	 source "$1/integration/env.sh"
	 printf "%s" "$HUATUO_BAMAI_TEST_TMPDIR" > "$1/preloaded-workspace"
	 for suite in integration e2e; do
	  for run in 1 2; do
	   bash "$1/$suite/run.sh" test_probe.sh > "$1/$suite-$run.log" 2>&1
	  done
	 done
	' inherited-env "${fixture}" || fatal "runner failed with a preloaded environment; see ${fixture}"
	local -a reports=()
	for suite in integration e2e; do
		for run in 1 2; do
			log="${fixture}/${suite}-${run}.log"

			report=$(assert_runner_summary "${log}" | jq -r .results_path)
			for previous in "${reports[@]}"; do [[ ${report} != "${previous}" ]] || fatal "invocations reused report ${report}"; done
			reports+=("${report}")
			if [[ ${suite} == integration ]]; then
				[[ ! -e ${report%/*} ]] || fatal "${suite}/${run}: successful integration directory was retained: ${report%/*}"
			else
				jq -e --arg suite "${suite}" '.suite == $suite and .status == "completed" and .exit_code == 0 and .execution_counts == {total:1,passed:1,failed:0,skipped:0} and .test_executions[0].result == "passed"' "${report}" > /dev/null || fatal "${suite}/${run}: report disagrees with execution: ${report}"
			fi
		done
	done
	[[ ! -e $(< "${fixture}/preloaded-workspace")/test_run_results.json ]] || fatal "runner wrote a report into the preloaded workspace"
)

assert_runner_namespaces() {
	runner_prepare_fixture
	rm -f "$RUNNER_FIXTURE/integration/"test_*.sh "$RUNNER_FIXTURE/e2e/"test_*.sh
	local profile suite report
	export runner_parent_net=$(readlink /proc/self/ns/net)
	export runner_parent_pid=$(readlink /proc/self/ns/pid)
	for suite in integration e2e; do
		cat > "$RUNNER_FIXTURE/$suite/test_metrics.sh" << 'EOF'
set -euo pipefail
source "$ROOT_DIR/integration/lib.sh"
assert_case_namespaces() {
 printf "%s\n" "$HUATUO_BAMAI_ADDR" >> "$ROOT_DIR/execution-addresses"
 [[ $(< "$HUATUO_BAMAI_TEST_TMPDIR/port-namespace") == "$(readlink /proc/self/ns/net)" ]] || fatal "port allocation ran outside the execution namespace"
 [[ $HUATUO_BAMAI_ADDR == "$HUATUO_BAMAI_DEFAULT_ADDR" ]] || fatal "execution address was not inherited"
 [[ /var/run -ef $HUATUO_BAMAI_TEST_TMPDIR/run ]] || fatal "case does not own /var/run"
 [[ $(readlink /proc/self/ns/mnt) != "$runner_parent_mount" ]] || fatal "case shares parent mount namespace"
 if [[ $NAMESPACE_PROFILE == net ]]; then
  [[ $(readlink /proc/self/ns/net) != "$runner_parent_net" ]] || fatal "network namespace was not isolated"
  ip -j link show lo | jq -e '.[0].flags | index("UP") != null' >/dev/null || fatal "loopback is down"
  for proxy_var in http_proxy https_proxy all_proxy ftp_proxy HTTP_PROXY HTTPS_PROXY ALL_PROXY FTP_PROXY no_proxy NO_PROXY; do
   [[ ! -v $proxy_var ]] || fatal "network namespace inherited $proxy_var"
  done
 else
  [[ $(readlink /proc/self/ns/net) == "$runner_parent_net" ]] || fatal "host network visibility was lost"
  [[ ${http_proxy:-} == http://127.0.0.1:9 ]] || fatal "non-network case lost its proxy environment"
 fi
 if [[ $NAMESPACE_PROFILE == pid ]]; then
  [[ $(readlink /proc/self/ns/pid) != "$runner_parent_pid" ]] || fatal "PID namespace was not isolated"
  [[ $(readlink /proc/1/ns/pid) == "$(readlink /proc/self/ns/pid)" ]] || fatal "procfs does not match PID namespace"
 else
  [[ $(readlink /proc/self/ns/pid) == "$runner_parent_pid" ]] || fatal "host process visibility was lost"
 fi
 if [[ $TEST_LOG_TAG == E2E ]]; then
  [[ $(< "$HUATUO_BAMAI_TEST_TMPDIR/service-namespace") == "$(readlink /proc/self/ns/mnt)" ]] || fatal "E2E preparation used a different namespace"
 fi
}
assert_case_namespaces
EOF
		cp "$RUNNER_FIXTURE/$suite/test_metrics.sh" "$RUNNER_FIXTURE/$suite/test_node_config_api.sh"
	done
	for profile in mount net pid; do
		local namespaces=mount
		[[ $profile == mount ]] || namespaces="mount $profile"
		printf '\nrunner_test_namespaces=(["integration/test_metrics.sh"]="%s" ["integration/test_node_config_api.sh"]="%s" ["e2e/test_metrics.sh"]="%s")\n' "$namespaces" "$namespaces" "$namespaces" >> "$RUNNER_FIXTURE/integration/lib_runner.sh"
		for suite in integration e2e; do
			http_proxy=http://127.0.0.1:9 HTTPS_PROXY=http://127.0.0.1:9 ALL_PROXY=http://127.0.0.1:9 \
				NAMESPACE_PROFILE=$profile assert_runner_result "$suite" "namespace-$profile" 0 '{"total":2,"passed":2,"skipped":0,"failed":0}' test_metrics.sh 2
		done
		http_proxy=http://127.0.0.1:9 NAMESPACE_PROFILE=$profile assert_runner_result integration "namespace-parallel-$profile" 0 '{"total":2,"passed":2,"skipped":0,"failed":0}' -j 2
		: > "$RUNNER_FIXTURE/execution-addresses"
		http_proxy=http://127.0.0.1:9 NAMESPACE_PROFILE=$profile assert_runner_result integration "namespace-repeat-$profile" 0 '{"total":2,"passed":2,"skipped":0,"failed":0}' -j 2 test_metrics.sh 2
		[[ $(sort -u "$RUNNER_FIXTURE/execution-addresses" | wc -l) == 2 ]] || fatal "repeated executions reused a slot port"

	done
	# Exercise all metadata registries, not just namespace declarations.
	local table
	for table in runner_test_groups runner_shared_storage_tests; do
		printf '\n%s["integration/test_metrics.sh"]=invalid\n' "$table" >> "$RUNNER_FIXTURE/integration/lib_runner.sh"
		assert_runner_result integration "invalid-$table" 1 '{"total":1,"passed":0,"skipped":1,"failed":0}' test_metrics.sh
		printf '\nunset "%s[integration/test_metrics.sh]"\n' "$table" >> "$RUNNER_FIXTURE/integration/lib_runner.sh"
		printf '\n%s["integration/test_absent.sh"]=1\n' "$table" >> "$RUNNER_FIXTURE/integration/lib_runner.sh"
		assert_runner_result integration "unknown-$table" 1 '{"total":1,"passed":0,"skipped":1,"failed":0}' test_metrics.sh
		printf '\nunset "%s[integration/test_absent.sh]"\n' "$table" >> "$RUNNER_FIXTURE/integration/lib_runner.sh"
	done
	# Invalid declarations fail before any case starts.
	printf '\nrunner_test_namespaces=(["integration/test_metrics.sh"]="mount invalid")\n' >> "$RUNNER_FIXTURE/integration/lib_runner.sh"
	assert_runner_result integration invalid_namespaces 1 '{"total":1,"passed":0,"skipped":1,"failed":0}' test_metrics.sh
	printf '\nrunner_test_namespaces=(["integration/test_absent.sh"]="mount")\n' >> "$RUNNER_FIXTURE/integration/lib_runner.sh"
	assert_runner_result integration unknown_namespace_case 1 '{"total":1,"passed":0,"skipped":1,"failed":0}' test_metrics.sh
	printf '\nrunner_test_namespaces=()\n' >> "$RUNNER_FIXTURE/integration/lib_runner.sh"
	cat > "$RUNNER_FIXTURE/bin/unshare" << 'EOF'
#!/usr/bin/env bash
if [[ $1 == --mount ]]; then exit 23; fi
exec /usr/bin/unshare "$@"
EOF
	chmod +x "$RUNNER_FIXTURE/bin/unshare"
	PATH="$RUNNER_FIXTURE/bin:$PATH" assert_runner_result integration case_namespace_failure 1 '{"total":1,"passed":0,"skipped":0,"failed":1}' test_metrics.sh
	report=$(assert_runner_summary "$HUATUO_BAMAI_TEST_TMPDIR/integration-case_namespace_failure.log" | jq -r .results_path)
	jq -e '.test_executions[0] | .result == "failed" and .exit_code == null' "$report" > /dev/null || fatal "namespace creation failure fabricated a test exit code"
	rm -f "$RUNNER_FIXTURE/bin/unshare"
	# Exercise raw exit codes, reason ownership and INT/TERM in real net/PID contexts.
	touch "$RUNNER_FIXTURE/integration/test_report.sh"
	for profile in net pid; do
		printf '\nrunner_test_namespaces=(["integration/test_report.sh"]="mount %s")\n' "$profile" >> "$RUNNER_FIXTURE/integration/lib_runner.sh"
		assert_runner_reports
	done
}

assert_runner_pid_cancellation() (
	local pid="" status=0 report namespace
	trap 'if [[ -n $pid ]]; then stop_by_process_group "$pid" 1 || true; fi' EXIT
	cat > "$RUNNER_FIXTURE/integration/test_report.sh" << 'EOF'
set -euo pipefail
source "$ROOT_DIR/integration/lib.sh"
trap '' TERM
readlink /proc/self/ns/pid > "$ROOT_DIR/forced-namespace"
sleep 30 &
wait "$!"
EOF
	printf '\nrunner_test_namespaces=(["integration/test_report.sh"]="mount pid")\n' >> "$RUNNER_FIXTURE/integration/lib_runner.sh"
	rm -f "$RUNNER_FIXTURE/forced-namespace"
	set -m
	bash "$RUNNER_FIXTURE/integration/run.sh" test_report.sh > "$RUNNER_FIXTURE/forced.log" 2>&1 &
	pid=$!
	set +m
	wait_until 5 0.05 test -s "$RUNNER_FIXTURE/forced-namespace" || fatal "PID cancellation fixture did not start"
	namespace=$(< "$RUNNER_FIXTURE/forced-namespace")
	namespace=${namespace#*[}
	namespace=${namespace%]}
	kill -TERM "$pid"
	wait_until 10 0.05 process_exited "$pid" || fatal "PID cancellation exceeded the grace period"
	wait "$pid" || status=$?
	pid=""
	[[ $status == 143 ]] || fatal "forced PID cancellation exit=$status, expected=143"
	! lsns -t pid -n -o NS | awk -v ns="$namespace" '$1 == ns {found=1} END {exit !found}' || fatal "cancelled PID namespace still has live processes: $namespace"
	report=$(assert_runner_summary "$RUNNER_FIXTURE/forced.log" | jq -r .results_path)
	jq -e '.status == "interrupted" and .exit_code == 143 and .test_executions[0].result == "skipped" and .test_executions[0].exit_code == null' "$report" > /dev/null || fatal "forced termination fabricated a case exit code"
)

assert_runner_execution_results() {
	runner_prepare_fixture
	rm -f "$RUNNER_FIXTURE/integration/"test_*.sh
	touch "$RUNNER_FIXTURE/integration/test_metrics.sh" "$RUNNER_FIXTURE/integration/test_node_config_api.sh"
	# Replace the process boundary, keeping scheduling, result parsing and reporting real.
	cat > "$RUNNER_FIXTURE/integration/runner_worker.sh" << 'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [[ ${3##*/} == test_node_config_api.sh ]]; then
 printf 'exit_code=0\ncleanup_status=0\ncleanup_reason:\n' > "$HUATUO_BAMAI_TEST_TMPDIR/execution.log"
 touch "$ROOT_DIR/peer-finished"
 exit 0
fi
case $RESULT_MODE in
 missing) ;;
 invalid) printf 'invalid\n' > "$HUATUO_BAMAI_TEST_TMPDIR/execution.log" ;;
 multiple) printf 'exit_code=0\nexit_code=0\ncleanup_status=0\ncleanup_reason:\n' > "$HUATUO_BAMAI_TEST_TMPDIR/execution.log" ;;
 fields) printf 'exit_code=0\n' > "$HUATUO_BAMAI_TEST_TMPDIR/execution.log" ;;
 type) printf 'exit_code="0"\ncleanup_status=0\ncleanup_reason:\n' > "$HUATUO_BAMAI_TEST_TMPDIR/execution.log" ;;
 range) printf 'exit_code=256\ncleanup_status=0\ncleanup_reason:\n' > "$HUATUO_BAMAI_TEST_TMPDIR/execution.log" ;;
 cleanup_status) printf 'exit_code=0\ncleanup_status=2\ncleanup_reason:\n' > "$HUATUO_BAMAI_TEST_TMPDIR/execution.log" ;;
 cleanup_empty) printf 'exit_code=0\ncleanup_status=1\ncleanup_reason:\n' > "$HUATUO_BAMAI_TEST_TMPDIR/execution.log" ;;
 header) printf 'exit_code=0\ncleanup_status=0\nreason:\n' > "$HUATUO_BAMAI_TEST_TMPDIR/execution.log" ;;
 cleanup_text) printf '%s\n' 'exit_code=0' 'cleanup_status=1' 'cleanup_reason:' 'cleanup_status=0' '引号 "test" C:\tmp' '$(touch "$ROOT_DIR/unexpected-command")' > "$HUATUO_BAMAI_TEST_TMPDIR/execution.log" ;;
 cleanup) printf 'exit_code=0\ncleanup_status=1\ncleanup_reason:\nfirst line\nsecond line\n' > "$HUATUO_BAMAI_TEST_TMPDIR/execution.log" ;;
esac
EOF
	local mode report
	for mode in missing invalid fields type range multiple cleanup_status cleanup_empty header cleanup_text cleanup; do
		rm -f "$RUNNER_FIXTURE/peer-finished"
		RESULT_MODE=$mode assert_runner_result integration "result-$mode" 1 '{"total":2,"passed":1,"skipped":0,"failed":1}' -j 2
		[[ -f $RUNNER_FIXTURE/peer-finished ]] || fatal "result error prevented peer completion: $mode"
		report=$(assert_runner_summary "$HUATUO_BAMAI_TEST_TMPDIR/integration-result-$mode.log" | jq -r .results_path)
		if [[ $mode == cleanup_text ]]; then
			local execution_log
			execution_log=${report%/*}/$(jq -r '.test_executions[] | select(.test_name == "test_metrics.sh") | .logs.stdout | sub("stdout.log$"; "execution.log")' "$report")
			tail -n +4 "$execution_log" > "$RUNNER_FIXTURE/expected-reason"
			jq -e --rawfile expected "$RUNNER_FIXTURE/expected-reason" '.test_executions[] | select(.test_name == "test_metrics.sh") | .exit_code == 0 and .reason == $expected' "$report" > /dev/null || fatal "cleanup reason was interpreted instead of preserved"
			[[ ! -e $RUNNER_FIXTURE/unexpected-command ]] || fatal "cleanup reason executed a command"
		elif [[ $mode == cleanup ]]; then
			jq -e '.test_executions[] | select(.test_name == "test_metrics.sh") | .exit_code == 0 and .reason == "first line\nsecond line\n"' "$report" > /dev/null || fatal "cleanup result lost the raw exit code or multiline reason"
		else
			jq -e '.test_executions[] | select(.test_name == "test_metrics.sh") | .exit_code == null and (.reason | startswith("worker result is "))' "$report" > /dev/null || fatal "invalid worker result fabricated a test exit code: $mode"
		fi
	done
}

assert_runner_timeout() (
	cp "${RUNNER_FIXTURE}/integration/run.sh" "${RUNNER_FIXTURE}/run.timeout.saved"
	trap 'mv "${RUNNER_FIXTURE}/run.timeout.saved" "${RUNNER_FIXTURE}/integration/run.sh"' EXIT
	cat > "${RUNNER_FIXTURE}/integration/run.sh" << 'EOF'
#!/usr/bin/env bash
set -euo pipefail
trap '' TERM
fixture_root=$(cd "$(dirname "$0")/.." && pwd)
readlink /proc/self/ns/pid > "$fixture_root/timeout-namespace"
# A separate session must not escape the fixture's PID namespace teardown.
setsid bash -c 'trap "" TERM; exec sleep 60' &
wait "$!"
EOF
	local status=0 namespace
	(runner_fixture_timeout=1 assert_runner_result integration timeout_probe 0 '') \
		> "${RUNNER_FIXTURE}/timeout.log" 2>&1 || status=$?
	[[ ${status} == 1 ]] || fatal "timeout probe exit=${status}, expected 1"
	grep -Fq 'runner timed out after 1 seconds' "${RUNNER_FIXTURE}/timeout.log" || fatal "timeout diagnosis missing"
	namespace=$(< "${RUNNER_FIXTURE}/timeout-namespace")
	namespace=${namespace#*[}
	namespace=${namespace%]}
	# Older unshare exits before the kernel finishes tearing down the child namespace.
	# Keep a bounded check so a live descendant still fails the regression.
	pid_namespace_exited() {
		! lsns -t pid -n -o NS | awk -v ns="$namespace" '$1 == ns {found=1} END {exit !found}'
	}
	wait_until 5 0.05 pid_namespace_exited \
		|| fatal "timed-out fixture left processes in PID namespace ${namespace}"
)

runner_prepare_fixture
assert_runner_suites
assert_runner_reports
assert_runner_launch_signal
assert_runner_late_signal
assert_runner_report_read_failure
assert_runner_report_write_failure scheduling
assert_runner_report_write_failure cleanup
assert_runner_prerequisites
assert_runner_parallel_isolation
assert_runner_storage_lifecycle
assert_runner_inherited_environment
assert_runner_namespaces
assert_runner_pid_cancellation
log_info "runner prerequisites, result counts, fail-fast behavior, storage sharing, and cleanup passed"

assert_runner_execution_results

assert_runner_timeout
