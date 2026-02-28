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

source "${ROOT_DIR}/integration/lib.sh"

fetch_huatuo_bamai_metrics() {
	huatuo_bamai_metrics >${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt
}

wait_and_fetch_metrics() {
	wait_until "${WAIT_HUATUO_BAMAI_TIMEOUT}" \
		"${WAIT_HUATUO_BAMAI_INTERVAL}" \
		"metrics endpoint ready" \
		fetch_huatuo_bamai_metrics
}

# Verify all expected metric files and dump metrics on success.
check_procfs_metrics() {
	for f in "${HUATUO_BAMAI_TEST_EXPECTED}"/*.txt; do
		prefix="$(basename "$f" .txt)"

		check_metrics_from_file "${f}"

		log_info "metric prefix ok: huatuo_bamai_${prefix}"
		grep "^huatuo_bamai_${prefix}" "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt" || log_info "(no metrics found)"
	done
}

check_metrics_from_file() {
	local file="$1"

	missing_metrics=$(
		grep -v '^[[:space:]]*\(#\|$\)' "${file}" |
			grep -Fvw -f "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt" || true
	)

	if [[ -z "${missing_metrics}" ]]; then
		return
	fi

	log_info "the missing metrics:"
	log_info "${missing_metrics}"
	log_info "the metrics file ${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt:"
	log_info "$(cat ${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt)"
	exit 1
}

test_huatuo_bamai_metrics() {
	wait_and_fetch_metrics
	check_procfs_metrics
	# ...
}

test_huatuo_bamai_metrics
