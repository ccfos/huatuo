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

usage() {
	echo "usage: $0 [--suite integration|e2e] [-j 1..8] [test_*.sh] [repeat-count]" >&2
}

TEST_SUITE=integration
REQUESTED_JOBS=${INTEGRATION_JOBS:-1}
jobs_explicit=false
while (($# > 0)); do
	case $1 in
	--suite | -j)
		(($# >= 2)) || {
			usage
			exit 2
		}
		if [[ $1 == --suite ]]; then TEST_SUITE=$2; else
			REQUESTED_JOBS=$2
			jobs_explicit=true
		fi
		shift 2
		;;
	-j*)
		REQUESTED_JOBS=${1#-j}
		jobs_explicit=true
		shift
		;;
	-*)
		usage
		exit 2
		;;
	*) break ;;
	esac
done
case ${TEST_SUITE} in
integration | e2e) ;;
*)
	echo "unsupported test suite: ${TEST_SUITE}; expected integration or e2e" >&2
	exit 2
	;;
esac
if [[ ${TEST_SUITE} == e2e ]]; then
	if [[ ${jobs_explicit} == true && ${REQUESTED_JOBS} != 1 ]]; then
		echo 'e2e tests require -j 1' >&2
		exit 2
	fi
	REQUESTED_JOBS=1
fi
[[ ${REQUESTED_JOBS} =~ ^[1-8]$ ]] || {
	echo 'jobs must be between 1 and 8' >&2
	exit 2
}
readonly REQUESTED_JOBS
readonly TEST_SUITE

if (($# > 2)); then
	usage
	exit 2
fi

readonly REQUESTED_TEST=${1:-}
readonly REQUESTED_REPEAT_COUNT=${2:-1}
readonly INTEGRATION_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
readonly ROOT_DIR=$(cd "${INTEGRATION_DIR}/.." && pwd)
readonly TEST_DIR="${ROOT_DIR}/${TEST_SUITE}"

if [[ -n "${REQUESTED_TEST}" ]]; then
	[[ "${REQUESTED_TEST}" == test_*.sh && "${REQUESTED_TEST}" != */* ]] || {
		echo "${TEST_SUITE} test must be a test_*.sh file name: ${REQUESTED_TEST}" >&2
		exit 2
	}
	[[ -f "${TEST_DIR}/${REQUESTED_TEST}" ]] || {
		echo "${TEST_SUITE} test not found: ${REQUESTED_TEST}" >&2
		exit 2
	}
fi

readonly MAX_REPEAT_COUNT=1000
readonly MAX_TEST_EXECUTIONS=10000
# Check length before arithmetic so oversized decimal input cannot overflow.
[[ "${REQUESTED_REPEAT_COUNT}" =~ ^[1-9][0-9]*$ && ${#REQUESTED_REPEAT_COUNT} -le ${#MAX_REPEAT_COUNT} ]] \
	&& ((REQUESTED_REPEAT_COUNT <= MAX_REPEAT_COUNT)) || {
	echo "repeat count must be between 1 and ${MAX_REPEAT_COUNT}: ${REQUESTED_REPEAT_COUNT}" >&2
	exit 2
}

export TEST_LOG_TAG=${TEST_SUITE^^}
source "${INTEGRATION_DIR}/lib.sh"
shopt -s nullglob
if [[ -n "${REQUESTED_TEST}" ]]; then
	test_scripts=("${TEST_DIR}/${REQUESTED_TEST}")
else
	test_scripts=("${TEST_DIR}"/test_*.sh)
fi

if ((${#test_scripts[@]} * REQUESTED_REPEAT_COUNT > MAX_TEST_EXECUTIONS)); then
	echo "planned test executions exceed ${MAX_TEST_EXECUTIONS}; select fewer tests or reduce repeat count" >&2
	exit 2
fi

# Establish a report before checking privileges or entering namespaces.
unset RUNNER_REASON_DIR RUNNER_REASON_OWNER
source "${INTEGRATION_DIR}/lib_runner_report.sh"
command -v jq > /dev/null || {
	log_error "test reports require jq; install jq"
	exit 1
}
RUNNER_OUTPUT_DIR=$(mktemp -d /tmp/huatuo-test.XXXXXX)
export RUNNER_OUTPUT_DIR
report_init "${TEST_SUITE}" "${REQUESTED_REPEAT_COUNT}" "${test_scripts[@]}"
supervisor_pid="" supervisor_reason="" supervisor_signal=""
supervisor_ready_file="${RUNNER_OUTPUT_DIR}/.supervisor-ready"
# Once the child has exited, a signal owns finalization. Bash defers the trap
# until foreground report I/O returns; exiting here prevents the interrupted
# cleanup path from overwriting the reconciled report.
supervisor_exit_interrupted() {
	local signal=$1 code=143 report_status
	trap '' INT TERM
	[[ ${signal} != INT ]] || code=130
	supervisor_reason="runner received SIG${signal}"
	report_status=$(jq -r .status "${RUNNER_OUTPUT_DIR}/test_run_results.json") || exit 1
	if [[ ${report_status} == running ]] && jq -e 'any(.test_executions[]; .started_at != null)' "${RUNNER_OUTPUT_DIR}/test_run_results.json" > /dev/null; then
		log_error "runner exited with an incomplete report: ${RUNNER_OUTPUT_DIR}/test_run_results.json"
	else
		if ! jq -e --argjson code "${code}" '.status == "interrupted" and .exit_code == $code' "${RUNNER_OUTPUT_DIR}/test_run_results.json" > /dev/null; then
			report_finish interrupted "${code}" "${supervisor_reason}" || exit 1
			log_info "run interrupted during finalization: ${supervisor_reason}; exit code ${code}"
		fi
		report_summary || exit 1
	fi
	exit "${code}"
}
supervisor_finalize() {
	local code=$?
	trap - EXIT
	if [[ -n ${supervisor_pid} ]]; then
		wait "${supervisor_pid}" || code=$?
	fi
	trap 'supervisor_exit_interrupted INT' INT
	trap 'supervisor_exit_interrupted TERM' TERM
	if [[ -n ${supervisor_signal} ]]; then supervisor_exit_interrupted "${supervisor_signal}"; fi
	local report_status
	report_status=$(jq -r .status "${RUNNER_OUTPUT_DIR}/test_run_results.json") || {
		log_error "cannot read test results: ${RUNNER_OUTPUT_DIR}/test_run_results.json"
		exit 1
	}
	if [[ ${report_status} == running ]]; then
		if jq -e 'any(.test_executions[]; .started_at != null)' "${RUNNER_OUTPUT_DIR}/test_run_results.json" > /dev/null; then
			# An inner runner that started cases owns their outcomes. Do not turn
			# lost results into skips when its report writer or cleanup failed.
			log_error "runner exited with an incomplete report: ${RUNNER_OUTPUT_DIR}/test_run_results.json"
			[[ ${code} != 0 ]] || code=1
		else
			[[ -n ${supervisor_reason} ]] || supervisor_reason="runner setup exited with code ${code}"
			if report_finish aborted "${code}" "${supervisor_reason}"; then
				report_status=aborted
			else
				code=1
			fi
		fi
	fi
	if [[ ${report_status} != running ]]; then
		# Publish once after reconciling late signals; keep output and results consistent.
		trap '' INT TERM
		report_summary || code=1
	fi
	exit "${code}"
}
supervisor_cancel() {
	supervisor_signal=$1
	# Caught traps reset in the child; ignored signals would survive exec.
	trap ':' INT TERM
	if [[ -n ${supervisor_pid} && -e ${supervisor_ready_file} ]]; then
		trap '' INT TERM
		kill -s "$1" "${supervisor_pid}" 2> /dev/null || true
	fi
}
supervisor_child_ready_or_exited() {
	[[ -e ${supervisor_ready_file} ]] || process_exited "${supervisor_pid}"
}
trap supervisor_finalize EXIT
trap 'supervisor_cancel INT' INT
trap 'supervisor_cancel TERM' TERM
if [[ ${EUID} -ne 0 ]]; then
	supervisor_reason="requires root (EUID=${EUID})"
	log_info "SKIP: ${supervisor_reason}"
	exit 0
fi
runner_prerequisites=(unshare mount flock ss stat)
for prerequisite in "${runner_prerequisites[@]}"; do
	if ! command -v "${prerequisite}" > /dev/null; then
		supervisor_reason="command is not installed: ${prerequisite}"
		log_info "SKIP: ${supervisor_reason}"
		exit 0
	fi
done
supervisor_reason="build output directory missing: ${ROOT_DIR}/_output; run make build"
require_build_output
supervisor_reason="namespace or runner initialization failed"
[[ -z ${supervisor_signal} ]] || exit 1
set -m
(
	# Publish readiness only after cancellation has a terminating handler.
	trap 'exit 130' INT
	trap 'exit 143' TERM
	if [[ -n ${supervisor_signal} ]]; then
		[[ ${supervisor_signal} != INT ]] || exit 130
		exit 143
	fi
	: > "${supervisor_ready_file}"
	exec unshare --uts --mount bash -c '
	set -euo pipefail
	mount --make-rprivate /
	echo "huatuo-dev" > /proc/sys/kernel/hostname
	hostname huatuo-dev 2>/dev/null || true

	cd "$1"
	# Each invocation owns its environment, even when the caller loaded env.sh.
	unset __HUATUO_ENV_SH_LOADED
	source ./integration/env.sh
	source ./integration/lib.sh
	source ./integration/lib_cgroup.sh
	HUATUO_TEST_CGROUP_MODE=$(cgroup_mode) || fatal "cannot determine cgroup mode from /sys/fs/cgroup"
	readonly HUATUO_TEST_CGROUP_MODE
	export HUATUO_TEST_CGROUP_MODE
	source ./integration/lib_runner.sh
	shift
	run_test_suite "$@"
' huatuo-test-runner "${ROOT_DIR}" "${REQUESTED_REPEAT_COUNT}" "${TEST_SUITE}" "${REQUESTED_JOBS}" "${test_scripts[@]}"
) &
supervisor_pid=$!
set +m
if ! wait_until 10 0.01 supervisor_child_ready_or_exited > /dev/null; then
	kill -KILL "${supervisor_pid}" 2> /dev/null || true
	supervisor_reason="runner child did not initialize signal handling within 10 seconds"
	log_error "${supervisor_reason}"
	exit 1
fi
# Forward pending cancellation only after the child can act on it.
if [[ -n ${supervisor_signal} ]]; then supervisor_cancel "${supervisor_signal}"; fi
supervisor_code=0
wait "${supervisor_pid}" || supervisor_code=$?
if [[ -n ${supervisor_signal} ]]; then wait "${supervisor_pid}" || supervisor_code=$?; fi
supervisor_pid=""
exit "${supervisor_code}"
