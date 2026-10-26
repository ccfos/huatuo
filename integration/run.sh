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

# The runner creates UTS and mount namespaces before executing any case.
if [[ ${EUID} -ne 0 ]]; then
	log_info "SKIP: requires root (EUID=${EUID})"
	test_skipped=$((${#test_scripts[@]} * REQUESTED_REPEAT_COUNT))
	test_results_summary
	exit 0
fi
runner_prerequisites=(unshare mount)
if ((REQUESTED_JOBS > 1)); then runner_prerequisites+=(flock ss); fi
if ! (require_commands "${runner_prerequisites[@]}"); then
	test_skipped=$((${#test_scripts[@]} * REQUESTED_REPEAT_COUNT))
	test_results_summary
	exit 0
fi
require_build_output

exec unshare --uts --mount bash -c '
	set -euo pipefail
	mount --make-rprivate /
	echo "huatuo-dev" > /proc/sys/kernel/hostname
	hostname huatuo-dev 2>/dev/null || true

	cd "$1"
	source ./integration/env.sh
	source ./integration/lib.sh
	source ./integration/lib_runner.sh
	shift
	run_test_suite "$@"
' huatuo-test-runner "${ROOT_DIR}" "${REQUESTED_REPEAT_COUNT}" "${TEST_SUITE}" "${REQUESTED_JOBS}" "${test_scripts[@]}"
