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
	echo "usage: $0 [test_*.sh] [repeat-count]" >&2
}

if (($# > 2)); then
	usage
	exit 2
fi

readonly REQUESTED_TEST=${1:-}
readonly REQUESTED_REPEAT_COUNT=${2:-1}
readonly INTEGRATION_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)

if [[ -n "${REQUESTED_TEST}" ]]; then
	[[ "${REQUESTED_TEST}" == test_*.sh && "${REQUESTED_TEST}" != */* ]] || {
		echo "integration test must be a test_*.sh file name: ${REQUESTED_TEST}" >&2
		exit 2
	}
	[[ -f "${INTEGRATION_DIR}/${REQUESTED_TEST}" ]] || {
		echo "integration test not found: ${REQUESTED_TEST}" >&2
		exit 2
	}
fi

[[ "${REQUESTED_REPEAT_COUNT}" =~ ^[1-9][0-9]*$ ]] || {
	echo "repeat count must be a positive integer: ${REQUESTED_REPEAT_COUNT}" >&2
	exit 2
}

# Integration tests need root: unshare --uts/--mount, BPF loading, and the
# test_*.sh cases themselves all require CAP_SYS_ADMIN/CAP_BPF. Skip cleanly
# when invoked without privilege so `make integration` is a no-op for
# unprivileged developers and CI lanes that don't grant root.
if [[ ${EUID} -ne 0 ]]; then
	echo "[INTEGRATION TEST] skipped: requires root (EUID=${EUID})" >&2
	exit 0
fi

# Run the core integration tests.
unshare --uts --mount bash -c '
	mount --make-rprivate /
	echo "huatuo-dev" > /proc/sys/kernel/hostname
	hostname huatuo-dev 2>/dev/null || true

	set -euo pipefail
	source "./integration/env.sh"
	source "${ROOT_DIR}/integration/lib.sh"
	requested_test=$1
	requested_repeat_count=$2
	active_test_workspace=""

	runner_cleanup() {
		local runner_status=$?
		[[ -n "${active_test_workspace}" ]] || return 0
		integration_test_exit "${runner_status}" "${active_test_workspace}" || true
	}
	trap runner_cleanup EXIT

	if [[ -n "${requested_test}" ]]; then
		test_scripts=("${ROOT_DIR}/integration/${requested_test}")
	else
		test_scripts=("${ROOT_DIR}"/integration/test_*.sh)
	fi

	# Run each test in an isolated workspace owned by this runner.
	for ((run = 1; run <= requested_repeat_count; run++)); do
		for test_script in "${test_scripts[@]}"; do
			[[ -f "${test_script}" ]] || continue
			test_name=$(basename "${test_script}" .sh)
			test_workspace=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/${test_name}.XXXXXX")
			active_test_workspace="${test_workspace}"
			log_info "🚀🚀 start: $(basename "${test_script}") (${run}/${requested_repeat_count})"

			chmod +x "${test_script}"
			if HUATUO_BAMAI_TEST_TMPDIR="${test_workspace}" bash "${test_script}"; then
				test_status=0
			else
				test_status=$?
			fi

			integration_test_exit "${test_status}" "${test_workspace}"
			active_test_workspace=""
			if [[ ${test_status} -ne 0 ]]; then
				fatal "❌ failed: $(basename "${test_script}") (${run}/${requested_repeat_count})"
			fi

			log_info "✅✅ passed: $(basename "${test_script}") (${run}/${requested_repeat_count})"
		done
	done

	# Failed runs exit earlier and retain this root with their artifacts.
	rmdir -- "${HUATUO_BAMAI_TEST_TMPDIR}"
	log_info "🎉🎉 all integration tests passed."
' integration-runner "${REQUESTED_TEST}" "${REQUESTED_REPEAT_COUNT}"
