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

source "./integration/env.sh"
source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/e2e/lib.sh"

_e2e_cleanup() {
	local code=$?
	[[ $code -eq 0 ]] && sleep 10 # wait more logs to be collected
	e2e_test_teardown "$code" || true
	exit $code
}
trap "_e2e_cleanup" EXIT

huatuo_bamai_start "${HUATUO_BAMAI_ARGS_E2E[@]}"

# auto run all test_*.sh scripts in the e2e
for case in "${ROOT_DIR}"/e2e/test_*.sh; do
	[[ -f "$case" ]] || continue
	log_info "⬅️⬅️ start: $(basename "$case")"

	if ! bash "$case"; then
		fatal "❌ failed: $(basename "$case")"
	fi

	log_info "✅✅ passed: $(basename "$case")"
done

log_info "🎉🎉 all e2e tests passed."
