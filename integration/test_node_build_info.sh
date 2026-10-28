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

# Verify that the normal daemon exposes its resolved build identity through HTTP.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"

readonly BUILD_INFO_REGION="build-info-test"
readonly CURL_TIMEOUT=(--connect-timeout 2 --max-time 3)

require_commands curl jq ss

cleanup() {
	huatuo_bamai_stop
}
trap cleanup EXIT

integration_huatuo_bamai_start write_config_bamai_default \
	--region "${BUILD_INFO_REGION}" --procfs-prefix "${HUATUO_BAMAI_TEST_FIXTURES}" \
	--disable-storage --disable-kubelet --log-debug

assert_build_info "${HUATUO_BAMAI_ADDR}" huatuo_bamai "$(uname -n)" "${BUILD_INFO_REGION}"
assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" huatuo-bamai
log_info "build-info matches /version and preserves host/region labels"
