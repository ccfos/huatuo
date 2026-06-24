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

# Verify all expected procfs-based metrics are present.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"

integration_huatuo_bamai_start

huatuo_bamai_await_metrics

for f in "${HUATUO_BAMAI_TEST_EXPECTED}"/*.txt; do
	prefix="$(basename "$f" .txt)"

	missing_metrics=$(
		grep -v '^[[:space:]]*\(#\|$\)' "${f}" |
			grep -Fvw -f "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt" || true
	)
	if [[ -n "${missing_metrics}" ]]; then
		log_info "missing metrics:"
		log_info "${missing_metrics}"
		log_info "metrics file ${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt:"
		log_info "$(cat "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt")"
		fatal "metrics check failed for prefix: ${prefix}"
	fi

	log_info "metric prefix ok: huatuo_bamai_${prefix}"
	grep "^huatuo_bamai_${prefix}" "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt" || log_info "(no metrics found)"
done

