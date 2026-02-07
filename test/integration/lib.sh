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

export TEST_LOG_TAG="INTEGRATION TEST"

source "${ROOT_DIR}/test/env.sh"
source "${ROOT_DIR}/test/common/utils.sh"
source "${ROOT_DIR}/test/common/huatuo-bamai.sh"

integration_test_huatuo_bamai_config() {
	cat >"${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" <<'EOF'
# the blacklist for tracing and metrics
BlackList = ["softlockup", "ethtool", "netstat_hw", "iolatency", "memory_free", "memory_reclaim", "reschedipi", "softirq"]
EOF
}

integration_test_huatuo_bamai_start() {
	[[ -x ${HUATUO_BAMAI_BIN} ]] || fatal "❌ binary not found: ${HUATUO_BAMAI_BIN}"
	[[ -d ${HUATUO_BAMAI_TEST_EXPECTED} ]] || fatal "❌ expected metrics directory not found: ${HUATUO_BAMAI_TEST_EXPECTED}"

	log_info "starting huatuo-bamai (mock fixture fs)"

	integration_test_huatuo_bamai_config

	huatuo_bamai_start "${HUATUO_BAMAI_ARGS_INTEGRATION[@]}"
	log_info "huatuo-bamai started"
}

integration_test_teardown() {
	local exit_code=$1

	huatuo_bamai_stop || true

	# Print details on failure
	if [ "${exit_code}" -ne 0 ]; then
		log_info "the exit code: $exit_code"
		log_info "
========== HUATUO INTEGRATION TEST FAILED ================

Summary:
  - One or more expected metrics are missing.

Temporary artifacts preserved at:
  ${HUATUO_BAMAI_TEST_TMPDIR}

Key files:
  - metrics.txt
  - huatuo.log
  - bamai.conf

=========================================================
"
	fi
}
