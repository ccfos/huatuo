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
require_commands unshare mount findmnt readlink flock ss stat jq
runner_parent_uts=$(readlink /proc/self/ns/uts)
runner_parent_mount=$(readlink /proc/self/ns/mnt)
export runner_parent_uts runner_parent_mount

assert_runner_result() {
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

runner_prepare_fixture() {
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
assert_huatuo_bamai_log_clean() { [[ ${RUNNER_LOG_FAIL:-0} == 0 ]]; }
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
		assert_runner_result "${runner_suite}" mixed 0 'total=2 passed=1 skipped=1 failed=0'
		[[ -f "${RUNNER_FIXTURE}/skip-cleaned" ]] || fatal "${runner_suite}: skip bypassed EXIT cleanup"
		assert_runner_result "${runner_suite}" repeated_pass 0 'total=2 passed=2 skipped=0 failed=0' test_pass.sh 2
		assert_runner_result "${runner_suite}" repeated_skip 0 'total=2 passed=0 skipped=2 failed=0' test_skip.sh 2
		RUNNER_STOP_FAIL=1 assert_runner_result "${runner_suite}" cleanup_failure 1 'total=1 passed=0 skipped=0 failed=1' test_skip.sh

		cat > "${RUNNER_FIXTURE}/${runner_suite}/test_zz_failure.sh" << 'EOF'
set -euo pipefail
false
EOF
		cat > "${RUNNER_FIXTURE}/${runner_suite}/test_zzz_not_run.sh" << 'EOF'
touch "${ROOT_DIR}/unexpected-run"
EOF
		assert_runner_result "${runner_suite}" failure 1 'total=3 passed=1 skipped=1 failed=1'
		[[ ! -e "${RUNNER_FIXTURE}/unexpected-run" ]] || fatal "${runner_suite}: continued after failure"
	done
	RUNNER_START_FAIL=1 assert_runner_result e2e startup_failure 1 'total=1 passed=0 skipped=0 failed=1' test_pass.sh
	RUNNER_LOG_FAIL=1 assert_runner_result e2e log_failure 1 'total=1 passed=0 skipped=0 failed=1' test_skip.sh
	assert_runner_result integration explicit_suite 0 'total=1 passed=1 skipped=0 failed=0' --suite integration test_pass.sh
	cp "${RUNNER_FIXTURE}/e2e/test_pass.sh" "${RUNNER_FIXTURE}/e2e/test_e2e_only.sh"
	assert_runner_result integration e2e_suite 0 'total=2 passed=2 skipped=0 failed=0' --suite e2e test_e2e_only.sh 2
	assert_runner_result integration missing_suite 2 '' --suite
	assert_runner_result integration invalid_suite 2 '' --suite unknown
	assert_runner_result integration missing_jobs 2 '' -j
	assert_runner_result integration invalid_jobs 2 '' -j 0
	assert_runner_result integration invalid_jobs_large 2 '' -j 9
	assert_runner_result integration e2e_parallel 2 '' --suite e2e -j 2
	assert_runner_result integration invalid_repeat 2 '' --suite e2e test_pass.sh 0
	assert_runner_result integration invalid_path 2 '' --suite e2e ../e2e/test_pass.sh
	assert_runner_result e2e invalid_repeat 2 '' test_pass.sh 0

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

	# Cases use built binaries directly; a missing binary must fail, not skip.
	cat > "${RUNNER_FIXTURE}/integration/test_missing_build_binary.sh" << 'EOF'
set -euo pipefail
"${ROOT_DIR}/_output/bin/profiler" --help
EOF
	assert_runner_result integration missing_build_binary 1 'total=1 passed=0 skipped=0 failed=1' test_missing_build_binary.sh
	grep -Fq "${RUNNER_FIXTURE}/_output/bin/profiler" "${HUATUO_BAMAI_TEST_TMPDIR}/integration-missing_build_binary.log" \
		|| fatal "missing build binary failed for an unrelated reason"
}

assert_runner_memray_artifacts() {
	# Missing supported runtimes must fail; unsupported Python versions may skip.
	cp "${ROOT_DIR}/integration/lib_profiler_python_memory.sh" "${RUNNER_FIXTURE}/integration/"
	mkdir -p "${RUNNER_FIXTURE}/_output/tools/memray/runtimes/py3.12/python/memray"
	cat > "${RUNNER_FIXTURE}/integration/test_memory_artifacts.sh" << 'EOF'
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
PROFILER_TOOL_DIR="${ROOT_DIR}/_output/tools"
source "${ROOT_DIR}/integration/lib_profiler_python_memory.sh"
memory_require_runtime "${RUNNER_MEMORY_PYTHON_VERSION:-3.12}"
EOF
	assert_runner_result integration missing_memory_extension 1 'total=1 passed=0 skipped=0 failed=1' test_memory_artifacts.sh
	grep -q 'Memray runtime for Python 3.12 missing' "${HUATUO_BAMAI_TEST_TMPDIR}/integration-missing_memory_extension.log" \
		|| fatal "missing Memray extension failed for an unrelated reason"
	touch "${RUNNER_FIXTURE}/_output/tools/memray/runtimes/py3.12/python/memray/_memray.fixture.so"
	assert_runner_result integration missing_memory_injector 1 'total=1 passed=0 skipped=0 failed=1' test_memory_artifacts.sh
	grep -q 'Memray injector for Python 3.12 missing' "${HUATUO_BAMAI_TEST_TMPDIR}/integration-missing_memory_injector.log" \
		|| fatal "missing Memray injector failed for an unrelated reason"
	touch "${RUNNER_FIXTURE}/_output/tools/memray/runtimes/py3.12/python/memray/_inject.fixture.so"
	assert_runner_result integration complete_memory_artifacts 0 'total=1 passed=1 skipped=0 failed=0' test_memory_artifacts.sh
	RUNNER_MEMORY_PYTHON_VERSION=3.6 assert_runner_result integration unsupported_memory_python_old 0 'total=1 passed=0 skipped=1 failed=0' test_memory_artifacts.sh
	RUNNER_MEMORY_PYTHON_VERSION=3.15 assert_runner_result integration unsupported_memory_python_new 0 'total=1 passed=0 skipped=1 failed=0' test_memory_artifacts.sh

	cat > "${RUNNER_FIXTURE}/integration/test_memory_node.sh" << 'EOF'
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
PROFILER_TOOL_DIR="${ROOT_DIR}/_output/tools"
source "${ROOT_DIR}/integration/lib_profiler_python_memory.sh"
node=$(memory_node_for_local_addresses "$ROOT_DIR/${RUNNER_MEMORY_NODES}" "$ROOT_DIR/local-addresses.json")
[[ "$node" == local-node ]] || fatal "selected a non-local Kubernetes node: $node"
EOF
	cat > "${RUNNER_FIXTURE}/local-addresses.json" << 'EOF'
[{"addr_info":[{"local":"127.0.0.1"},{"local":"10.0.0.2"},{"local":"fd00::2"}]}]
EOF
	cat > "${RUNNER_FIXTURE}/local-node.json" << 'EOF'
{"items":[{"metadata":{"name":"local-node"},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.2"}]}}]}
EOF
	cat > "${RUNNER_FIXTURE}/remote-node.json" << 'EOF'
{"items":[{"metadata":{"name":"remote-node"},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.3"}]}}]}
EOF
	jq -s '{items:([.[].items[]] | reverse)}' \
		"${RUNNER_FIXTURE}/local-node.json" "${RUNNER_FIXTURE}/remote-node.json" \
		> "${RUNNER_FIXTURE}/multiple-nodes.json"
	jq '.items[0].status.addresses[0].address = "fd00::2"' "${RUNNER_FIXTURE}/local-node.json" \
		> "${RUNNER_FIXTURE}/ipv6-node.json"
	jq '.items += [.items[0] | .metadata.name = "ambiguous-node"]' "${RUNNER_FIXTURE}/local-node.json" \
		> "${RUNNER_FIXTURE}/ambiguous-nodes.json"
	RUNNER_MEMORY_NODES=local-node.json assert_runner_result integration memory_single_node 0 'total=1 passed=1 skipped=0 failed=0' test_memory_node.sh
	RUNNER_MEMORY_NODES=multiple-nodes.json assert_runner_result integration memory_multiple_nodes 0 'total=1 passed=1 skipped=0 failed=0' test_memory_node.sh
	RUNNER_MEMORY_NODES=ipv6-node.json assert_runner_result integration memory_ipv6_node 0 'total=1 passed=1 skipped=0 failed=0' test_memory_node.sh
	RUNNER_MEMORY_NODES=remote-node.json assert_runner_result integration memory_no_local_node 1 'total=1 passed=0 skipped=0 failed=1' test_memory_node.sh
	RUNNER_MEMORY_NODES=ambiguous-nodes.json assert_runner_result integration memory_ambiguous_node 1 'total=1 passed=0 skipped=0 failed=1' test_memory_node.sh
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
	assert_runner_result integration parallel_isolation 0 'total=2 passed=2 skipped=0 failed=0' -j 2
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
		success) assert_runner_result integration shared_storage 0 'total=2 passed=2 skipped=0 failed=0' ;;
		skip) assert_runner_result integration shared_storage_skip 0 'total=2 passed=0 skipped=2 failed=0' ;;
		failure) assert_runner_result integration shared_storage_failure 1 'total=1 passed=0 skipped=0 failed=1' ;;
		esac
		[[ $(grep -c '^start$' "${RUNNER_FIXTURE}/storage-lifecycle") == 1 ]] || fatal "${runner_mode}: shared storage did not start once"
		[[ $(grep -c '^stop$' "${RUNNER_FIXTURE}/storage-lifecycle") == 1 ]] || fatal "${runner_mode}: shared storage did not stop once"
		if [[ ${runner_mode} == success ]]; then
			[[ $(sort -u "${RUNNER_FIXTURE}/storage-indices" | wc -l) == 2 ]] || fatal "shared storage reused an index"
		fi
	done
	export runner_storage_mode=success
	: > "${RUNNER_FIXTURE}/storage-lifecycle"
	assert_runner_result integration repeated_storage 0 'total=2 passed=2 skipped=0 failed=0' test_apiserver_continuous_profiling_api.sh 2
	[[ $(grep -c '^start$' "${RUNNER_FIXTURE}/storage-lifecycle") == 2 ]] || fatal "repeated storage reused the previous round"
	[[ $(grep -c '^stop$' "${RUNNER_FIXTURE}/storage-lifecycle") == 2 ]] || fatal "repeated storage cleanup did not run"
	unset runner_storage_mode

}

runner_prepare_fixture
assert_runner_suites
assert_runner_prerequisites
assert_runner_memray_artifacts
assert_runner_parallel_isolation
assert_runner_storage_lifecycle
log_info "runner prerequisites, result counts, fail-fast behavior, storage sharing, and cleanup passed"
