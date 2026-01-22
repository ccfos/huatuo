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

set -o errexit
set -o nounset
set -o pipefail

BASEDIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${BASEDIR}/utils.sh"

# Always kill the huatuo-bamai process.
trap test_teardown EXIT

# Run the core integration test flow.
test_run() {
  test_setup
  test_metrics
}

# Run integration test and preserve exit code even under `set -e`.
test_run && test_exit_code=$? || test_exit_code=$?

# Dump bamai metrics on failure to aid debugging.
if [ "${test_exit_code}" -ne 0 ]; then
  log_info "
========== HUATUO-BAMAI INTEGRATION TEST FAILED ==========

Summary:
  - One or more expected metrics are missing.

Temporary artifacts preserved at:
  ${TMPDIR}

Key files:
  - metrics.txt
  - huatuo.log

=========================================================
"
fi

exit "${test_exit_code}"

