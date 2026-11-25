#!/usr/bin/env bash

# Copyright 2026 The HuaTuo Authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Verify early burst suppression and triggering after the sampling window fills.
set -euo pipefail
source "${ROOT_DIR}/integration/lib_memburst.sh"
require_commands go jq mount umount awk timeout readlink
burst_setup
trap burst_cleanup EXIT
burst_build_fixture
burst_prepare_ranked_workloads
huatuo_bamai_stop
HUATUO_BAMAI_TEST_TMPDIR="${MEMBURST_ROOT_WORKSPACE}/window_fill"
mkdir -p "${HUATUO_BAMAI_TEST_TMPDIR}"
# Omit SlidingWindowLength to exercise the daemon default.
burst_window=""
burst_input low
burst_start
burst_wait_samples low
burst_input high
burst_wait_samples high 3
assert_burst_count 0 "history triggered before filling"
burst_input low
wait_until 90 .2 memsnapshot_memburst_samples_ready "${burst_sample_offset}" 60 100000 || fatal "default memburst history did not fill"
assert_burst_count 0 "event without burst"
burst_input high
wait_until 20 .1 burst_expected_count 1 || fatal "burst did not persist"
assert_burst_event
burst_stop TERM
log_info "PASS: memburst window_fill"
