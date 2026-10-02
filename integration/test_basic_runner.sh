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
[[ ${EUID} -eq 0 ]] || skip "runner tests require root for integration namespaces"
require_commands unshare mount findmnt readlink
RUNNER_PARENT_UTS=$(readlink /proc/self/ns/uts)
RUNNER_PARENT_MOUNT=$(readlink /proc/self/ns/mnt)
export RUNNER_PARENT_UTS RUNNER_PARENT_MOUNT

mkdir -p "${RUNNER_FIXTURE}"/{integration,e2e,_output}
cp "${ROOT_DIR}/integration/"{run.sh,lib.sh} "${RUNNER_FIXTURE}/integration/"
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
		bash "${RUNNER_FIXTURE}/${suite}/run.sh" "$@"
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

log_info "runner prerequisites, result counts, fail-fast behavior, and cleanup passed"
