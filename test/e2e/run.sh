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

source "./test/env.sh"
source "${ROOT_DIR}/test/e2e/lib.sh"
source "${ROOT_DIR}/test/e2e/cases/container.sh"
source "${ROOT_DIR}/test/e2e/cases/metrics.sh"

trap "
    [ \$? -eq 0 ] && sleep 10 # wait more logs to be collected
    e2e_test_teardown
" EXIT

huatuo_bamai_start "${HUATUO_BAMAI_ARGS_E2E[@]}"
test_huatuo_bamai_metrics
test_huatuo_bamai_default_container_exists
test_huatuo_bamai_e2e_container_create
test_huatuo_bamai_e2e_container_delete
