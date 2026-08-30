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
	echo "usage: $0 [--suite integration|e2e] [test_*.sh] [repeat-count]" >&2
}

TEST_SUITE=integration
if [[ ${1:-} == --suite ]]; then
	if (($# < 2)); then
		usage
		exit 2
	fi
	TEST_SUITE=$2
	shift 2
fi
case ${TEST_SUITE} in
integration | e2e) ;;
*)
	echo "unsupported test suite: ${TEST_SUITE}; expected integration or e2e" >&2
	exit 2
	;;
esac
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

[[ "${REQUESTED_REPEAT_COUNT}" =~ ^[1-9][0-9]*$ ]] || {
	echo "repeat count must be a positive integer: ${REQUESTED_REPEAT_COUNT}" >&2
	exit 2
}

export TEST_LOG_TAG=${TEST_SUITE^^}
source "${INTEGRATION_DIR}/lib.sh"
test_results_init
shopt -s nullglob
if [[ -n "${REQUESTED_TEST}" ]]; then
	test_scripts=("${TEST_DIR}/${REQUESTED_TEST}")
else
	test_scripts=("${TEST_DIR}"/test_*.sh)
fi

# TEST_<case>_REQUIRED=1 makes missing suite prerequisites fatal.
required_test_env=""
for test_script in "${test_scripts[@]}"; do
	required_case=${test_script##*/}
	required_case=${required_case#test_}
	required_case=${required_case%.sh}
	required_case=${required_case//[^[:alnum:]_]/_}
	required_env="TEST_${required_case^^}_REQUIRED"
	if [[ ${!required_env:-} == 1 ]]; then
		required_test_env=${required_env}
		break
	fi
done

# The runner creates UTS and mount namespaces before executing any case.
if [[ ${EUID} -ne 0 ]]; then
	[[ -z ${required_test_env} ]] || fatal "${required_test_env}=1 requires root (EUID=${EUID})"
	log_info "SKIP: requires root (EUID=${EUID})"
	test_skipped=$((${#test_scripts[@]} * REQUESTED_REPEAT_COUNT))
	test_results_summary
	exit 0
fi
if ! (require_commands unshare mount); then
	[[ -z ${required_test_env} ]] || fatal "${required_test_env}=1 requires unshare and mount"
	test_skipped=$((${#test_scripts[@]} * REQUESTED_REPEAT_COUNT))
	test_results_summary
	exit 0
fi
require_build_output

unshare --uts --mount bash -c '
	set -euo pipefail
	mount --make-rprivate /
	echo "huatuo-dev" > /proc/sys/kernel/hostname
	hostname huatuo-dev 2>/dev/null || true

	requested_repeat_count=$1
	test_suite=$2
	cd "$3"
	shift 3
	test_scripts=("$@")
	source "./integration/env.sh"
	source "${ROOT_DIR}/integration/lib.sh"
	if [[ ${test_suite} == e2e ]]; then
		source "${ROOT_DIR}/e2e/lib.sh"
	fi
	test_results_init
	active_test_workspace=""
	active_test_name=""

	runner_test_exit() {
		local status=$1
		if [[ ${test_suite} == e2e ]]; then
			HUATUO_BAMAI_TEST_TMPDIR="${active_test_workspace}" e2e_test_teardown "${status}" || status=1
			if [[ ${status} -ne 0 && ${status} -ne 77 ]]; then
				log_error "e2e artifacts: ${active_test_workspace}"
				dump_text_files "${active_test_workspace}" || true
			fi
		else
			integration_test_exit "${status}" "${active_test_workspace}" || status=1
		fi
		return "${status}"
	}

	runner_cleanup() {
		local runner_status=$?
		if [[ -n "${active_test_workspace}" ]]; then
			runner_test_exit "${runner_status}" || runner_status=$?
			test_result_record "${active_test_name}" "${runner_status}"
		fi
		test_results_summary
		[[ ${runner_status} -ne 77 ]] || runner_status=0
		exit "${runner_status}"
	}
	trap runner_cleanup EXIT

	# Run each test in an isolated workspace owned by this runner.
	for ((run = 1; run <= requested_repeat_count; run++)); do
		for test_script in "${test_scripts[@]}"; do
			[[ -f "${test_script}" ]] || continue
			test_name=$(basename "${test_script}" .sh)
			test_workspace=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/${test_name}.XXXXXX")
			active_test_workspace="${test_workspace}"
			active_test_name="$(basename "${test_script}") (${run}/${requested_repeat_count})"
			log_info "🚀🚀 start: $(basename "${test_script}") (${run}/${requested_repeat_count})"

			if [[ ${test_suite} == e2e ]]; then
				# Cases may restart bamai; restore the baseline for each next case.
				HUATUO_BAMAI_TEST_TMPDIR="${test_workspace}" huatuo_bamai_start "${HUATUO_BAMAI_ARGS_E2E[@]}"
			fi
			if HUATUO_BAMAI_TEST_TMPDIR="${test_workspace}" bash "${test_script}"; then
				test_status=0
			else
				test_status=$?
			fi

			runner_test_exit "${test_status}" || test_status=$?
			active_test_workspace=""
			test_result_record "${active_test_name}" "${test_status}"
			if [[ ${test_status} -ne 0 && ${test_status} -ne 77 ]]; then
				exit "${test_status}"
			fi
		done
	done

	# E2E retains artifacts; successful integration runs leave an empty root.
	if [[ ${test_suite} == integration ]]; then
		rmdir -- "${HUATUO_BAMAI_TEST_TMPDIR}"
	fi
' huatuo-test-runner "${REQUESTED_REPEAT_COUNT}" "${TEST_SUITE}" "${ROOT_DIR}" "${test_scripts[@]}"
