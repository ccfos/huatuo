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

# The logger quotes text fields; exercise both text variants and JSON output.
for runner_log_format in text quoted json json-spaced; do
	for runner_log_level in info error panic fatal; do
		runner_log_probe="${HUATUO_BAMAI_TEST_TMPDIR}/log-${runner_log_format}-${runner_log_level}.log"
		case ${runner_log_format} in
		text) printf 'level=%s msg=probe\n' "${runner_log_level}" > "${runner_log_probe}" ;;
		quoted) printf 'level="%s" msg="probe"\n' "${runner_log_level}" > "${runner_log_probe}" ;;
		json) printf '{"level":"%s","msg":"probe"}\n' "${runner_log_level}" > "${runner_log_probe}" ;;
		json-spaced) printf '{"level": "%s", "msg": "probe"}\n' "${runner_log_level}" > "${runner_log_probe}" ;;
		esac
		runner_log_status=0
		(assert_log_has_no_failure "${runner_log_probe}" fixture) \
			> "${runner_log_probe}.assertion" 2>&1 || runner_log_status=$?
		runner_log_expected=1
		[[ ${runner_log_level} != info ]] || runner_log_expected=0
		assert_eq "${runner_log_status}" "${runner_log_expected}" "${runner_log_format}/${runner_log_level}" \
			|| fatal "log assertion misclassified ${runner_log_probe}; see ${runner_log_probe}.assertion"
	done
done
log_info "text and JSON log levels checked"

# Check idle Pod manifests and lifecycle errors without requiring a cluster.
for pod_failure in none run delete; do
	pod_calls="${HUATUO_BAMAI_TEST_TMPDIR}/pods-${pod_failure}.log"
	pod_status=0
	TEST_POD_CALLS="${pod_calls}" TEST_POD_FAILURE="${pod_failure}" bash -euc '
source "$ROOT_DIR/e2e/lib.sh"
kubectl() {
 printf "%s\n" "$*" >> "$TEST_POD_CALLS"
 [[ $1 != "$TEST_POD_FAILURE" ]] || return 42
}
k8s_create_pod fixtures test busybox:1.36.1 app=test 2 || exit $?
k8s_delete_pod fixtures app=test
' > /dev/null 2>&1 || pod_status=$?
	if [[ ${pod_failure} == none ]]; then
		[[ ${pod_status} == 0 ]] || fatal "idle Pod lifecycle failed: exit=${pod_status}"
		[[ $(grep -cF -- '--overrides={"spec":{"terminationGracePeriodSeconds":1}}' "${pod_calls}") == 2 ]] \
			|| fatal "idle Pods must use a one-second termination grace period"
		grep -Fxq 'delete pod --namespace fixtures -l app=test' "${pod_calls}" \
			|| fatal "Pod deletion must retain default synchronous waiting"
	else
		[[ ${pod_status} == 42 ]] || fatal "kubectl ${pod_failure} failure was lost: exit=${pod_status}"
		if [[ ${pod_failure} == run ]]; then
			[[ $(wc -l < "${pod_calls}") == 1 ]] || fatal "Pod creation continued after failure"
		fi
	fi
done
log_info "idle Pod grace period and lifecycle failures checked"

readonly RUNNER_FIXTURE="${HUATUO_BAMAI_TEST_TMPDIR}/runner"
[[ ${EUID} -eq 0 ]] || skip "runner tests require root for integration namespaces"
require_commands unshare mount findmnt readlink flock ss stat
RUNNER_PARENT_UTS=$(readlink /proc/self/ns/uts)
RUNNER_PARENT_MOUNT=$(readlink /proc/self/ns/mnt)
export RUNNER_PARENT_UTS RUNNER_PARENT_MOUNT

mkdir -p "${RUNNER_FIXTURE}"/{integration,e2e,_output}
cp "${ROOT_DIR}/integration/"{run.sh,lib.sh,lib_runner.sh,exec_case.sh} "${RUNNER_FIXTURE}/integration/"
cp "${ROOT_DIR}/e2e/"{run.sh,lib.sh} "${RUNNER_FIXTURE}/e2e/"
cat > "${RUNNER_FIXTURE}/integration/env.sh" << 'EOF'
export ROOT_DIR=$PWD
HUATUO_BAMAI_TEST_TMPDIR=$(mktemp -d "${ROOT_DIR}/workspace.XXXXXX")
export HUATUO_BAMAI_TEST_TMPDIR
HUATUO_BAMAI_ARGS_E2E=()
EOF
# Keep result classification and workspace cleanup real; replace service I/O.
cat >> "${RUNNER_FIXTURE}/integration/lib.sh" << 'EOF'
huatuo_bamai_start() {
    : > "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log"
    [[ ${RUNNER_START_FAIL:-0} == 0 ]] || fatal "fixture startup failure"
}
huatuo_bamai_stop() { [[ ${RUNNER_STOP_FAIL:-0} == 0 ]]; }
huatuo_apiserver_stop() { return 0; }
huatuo_bamai_log_check() { [[ ${RUNNER_LOG_FAIL:-0} == 0 ]]; }
EOF

runner_check() {
	local suite=$1 label=$2 expected_status=$3 expected_summary=$4 status=0
	shift 4
	local log="${HUATUO_BAMAI_TEST_TMPDIR}/${suite}-${label}.log" summary_count=1
	(
		cd /
		INTEGRATION_JOBS=1 bash "${RUNNER_FIXTURE}/${suite}/run.sh" "$@"
	) > "${log}" 2>&1 || status=$?
	if [[ ${status} != "${expected_status}" ]] || { [[ -n "${expected_summary}" ]] && ! grep -Fq "summary: ${expected_summary}" "${log}"; }; then
		cat "${log}"
		fatal "${suite}/${label}: exit=${status}, expected exit=${expected_status}, summary=${expected_summary}"
	fi
	[[ -n "${expected_summary}" ]] || summary_count=0
	[[ $(grep -c 'summary:' "${log}") == "${summary_count}" ]] || fatal "${suite}/${label}: unexpected summary count"
	if grep -q 'PASS: test_skip.sh' "${log}"; then
		fatal "${suite}/${label}: skipped case reported as passed"
	fi
}

for runner_suite in integration e2e; do
	rm -f "${RUNNER_FIXTURE}/skip-cleaned"
	cat > "${RUNNER_FIXTURE}/${runner_suite}/test_pass.sh" << 'EOF'
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
require_commands bash "${ROOT_DIR}/integration/run.sh"
require_build_output
[[ $(hostname) == huatuo-dev ]] || fatal "runner hostname was not isolated"
[[ $(readlink /proc/self/ns/uts) != "${RUNNER_PARENT_UTS}" ]] || fatal "runner reused the parent UTS namespace"
[[ $(readlink /proc/self/ns/mnt) != "${RUNNER_PARENT_MOUNT}" ]] || fatal "runner reused the parent mount namespace"
[[ $(findmnt -n -o PROPAGATION /) == private ]] || fatal "runner root mount is not private"
[[ ! -e "${HUATUO_BAMAI_TEST_TMPDIR}/case-marker" ]] || fatal "runner reused a case workspace"
touch "${HUATUO_BAMAI_TEST_TMPDIR}/case-marker"
if [[ ${TEST_LOG_TAG} == E2E ]]; then
    [[ -f "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" ]] || fatal "e2e baseline daemon was not started"
else
    [[ ! -e "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" ]] || fatal "integration started an unsolicited daemon"
fi
EOF
	cat > "${RUNNER_FIXTURE}/${runner_suite}/test_skip.sh" << 'EOF'
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
trap 'touch "${ROOT_DIR}/skip-cleaned"' EXIT
require_commands huatuo-runner-missing-command
EOF
	runner_check "${runner_suite}" mixed 0 'total=2 passed=1 skipped=1 failed=0'
	[[ -f "${RUNNER_FIXTURE}/skip-cleaned" ]] || fatal "${runner_suite}: skip bypassed EXIT cleanup"
	runner_check "${runner_suite}" repeated_pass 0 'total=2 passed=2 skipped=0 failed=0' test_pass.sh 2
	runner_check "${runner_suite}" repeated_skip 0 'total=2 passed=0 skipped=2 failed=0' test_skip.sh 2
	RUNNER_STOP_FAIL=1 runner_check "${runner_suite}" cleanup_failure 1 'total=1 passed=0 skipped=0 failed=1' test_skip.sh

	cat > "${RUNNER_FIXTURE}/${runner_suite}/test_zz_failure.sh" << 'EOF'
set -euo pipefail
false
EOF
	cat > "${RUNNER_FIXTURE}/${runner_suite}/test_zzz_not_run.sh" << 'EOF'
touch "${ROOT_DIR}/unexpected-run"
EOF
	runner_check "${runner_suite}" failure 1 'total=3 passed=1 skipped=1 failed=1'
	[[ ! -e "${RUNNER_FIXTURE}/unexpected-run" ]] || fatal "${runner_suite}: continued after failure"
done
RUNNER_START_FAIL=1 runner_check e2e startup_failure 1 'total=1 passed=0 skipped=0 failed=1' test_pass.sh
RUNNER_LOG_FAIL=1 runner_check e2e log_failure 1 'total=1 passed=0 skipped=0 failed=1' test_skip.sh
runner_check integration explicit_suite 0 'total=1 passed=1 skipped=0 failed=0' --suite integration test_pass.sh
cp "${RUNNER_FIXTURE}/e2e/test_pass.sh" "${RUNNER_FIXTURE}/e2e/test_e2e_only.sh"
runner_check integration e2e_suite 0 'total=2 passed=2 skipped=0 failed=0' --suite e2e test_e2e_only.sh 2
runner_check integration missing_suite 2 '' --suite
runner_check integration invalid_suite 2 '' --suite unknown
runner_check integration missing_jobs 2 '' -j
runner_check integration invalid_jobs 2 '' -j 0
runner_check integration invalid_jobs_large 2 '' -j 9
runner_check integration e2e_parallel 2 '' --suite e2e -j 2
runner_check integration invalid_repeat 2 '' --suite e2e test_pass.sh 0
runner_check integration invalid_path 2 '' --suite e2e ../e2e/test_pass.sh
runner_check e2e invalid_repeat 2 '' test_pass.sh 0

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

# Parallel cases have independent PID files, toolstream paths, and HTTP ports.
rm -f "${RUNNER_FIXTURE}/integration/"test_*.sh
RUNNER_SOCKET_ID=""
if [[ -S /var/run/docker.sock ]]; then RUNNER_SOCKET_ID=$(stat -Lc '%d:%i' /var/run/docker.sock); fi
RUNNER_NESTED_MOUNT="" RUNNER_NESTED_ID=""
runner_runtime_root=$(readlink -f /var/run)
while IFS= read -r runner_mount; do
	[[ ${runner_mount} == "${runner_runtime_root}/"* ]] || continue
	RUNNER_NESTED_MOUNT=${runner_mount#"${runner_runtime_root}"}
	RUNNER_NESTED_ID=$(stat -Lc '%d:%i' "${runner_mount}")
	break
done < <(findmnt -rn -o TARGET)
export RUNNER_SOCKET_ID RUNNER_NESTED_MOUNT RUNNER_NESTED_ID
cat > "${RUNNER_FIXTURE}/integration/test_metrics.sh" << 'EOF'
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
name=${0##*/}
exec 8>/var/run/huatuo-bamai.pid
exec 9>/var/run/huatuo-apiserver.pid
flock -n 8 || fatal "bamai PID lock was shared between workers"
flock -n 9 || fatal "apiserver PID lock was shared between workers"
for file in huatuo-bamai.pid huatuo-apiserver.pid huatuo-toolstream.sock; do
 printf '%s' "$name" > "/var/run/$file"
done
if [[ -n $RUNNER_SOCKET_ID ]]; then
 [[ $(stat -Lc '%d:%i' /var/run/docker.sock) == "$RUNNER_SOCKET_ID" ]] || fatal "host Docker socket was hidden"
fi
if [[ -n $RUNNER_NESTED_MOUNT ]]; then
 [[ $(stat -Lc '%d:%i' "/var/run$RUNNER_NESTED_MOUNT") == "$RUNNER_NESTED_ID" ]] || fatal "nested runtime mount was hidden"
fi
printf '%s\n' "$HUATUO_BAMAI_ADDR" > "$ROOT_DIR/$name.address"
touch "$ROOT_DIR/$name.started"
peer=test_metrics.sh
[[ $name != test_metrics.sh ]] || peer=test_node_config_api.sh
wait_until 5 0.05 test -f "$ROOT_DIR/$peer.started"
for file in huatuo-bamai.pid huatuo-apiserver.pid huatuo-toolstream.sock; do
 [[ $(< "/var/run/$file") == "$name" ]] || fatal "worker overwrote another case runtime file"
done
EOF
cp "${RUNNER_FIXTURE}/integration/"test_{metrics,node_config_api}.sh
runner_check integration parallel_isolation 0 'total=2 passed=2 skipped=0 failed=0' -j 2
[[ $(cat "${RUNNER_FIXTURE}/"*.address | sort -u | wc -l) == 2 ]] || fatal "parallel workers shared a daemon port"

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
case ${RUNNER_STORAGE_MODE} in
skip) skip "fixture skip after storage startup";;
failure) fatal "fixture failure after storage startup";;
esac
EOF
cp "${RUNNER_FIXTURE}/integration/"test_apiserver_continuous_profiling_{api,control}.sh
for mode in success skip failure; do
	export RUNNER_STORAGE_MODE=${mode}
	: > "${RUNNER_FIXTURE}/storage-lifecycle"
	: > "${RUNNER_FIXTURE}/storage-indices"
	case ${mode} in
	success) runner_check integration shared_storage 0 'total=2 passed=2 skipped=0 failed=0' ;;
	skip) runner_check integration shared_storage_skip 0 'total=2 passed=0 skipped=2 failed=0' ;;
	failure) runner_check integration shared_storage_failure 1 'total=1 passed=0 skipped=0 failed=1' ;;
	esac
	[[ $(grep -c '^start$' "${RUNNER_FIXTURE}/storage-lifecycle") == 1 ]] || fatal "${mode}: shared storage did not start once"
	[[ $(grep -c '^stop$' "${RUNNER_FIXTURE}/storage-lifecycle") == 1 ]] || fatal "${mode}: shared storage did not stop once"
	if [[ ${mode} == success ]]; then
		[[ $(sort -u "${RUNNER_FIXTURE}/storage-indices" | wc -l) == 2 ]] || fatal "shared storage reused an index"
	fi
done
export RUNNER_STORAGE_MODE=success
: > "${RUNNER_FIXTURE}/storage-lifecycle"
runner_check integration repeated_storage 0 'total=2 passed=2 skipped=0 failed=0' test_apiserver_continuous_profiling_api.sh 2
[[ $(grep -c '^start$' "${RUNNER_FIXTURE}/storage-lifecycle") == 2 ]] || fatal "repeated storage reused the previous round"
[[ $(grep -c '^stop$' "${RUNNER_FIXTURE}/storage-lifecycle") == 2 ]] || fatal "repeated storage cleanup did not run"
unset RUNNER_STORAGE_MODE

log_info "runner prerequisites, result counts, fail-fast behavior, storage sharing, and cleanup passed"
