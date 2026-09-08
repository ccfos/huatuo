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
source "${ROOT_DIR}/integration/config.sh"
source "${ROOT_DIR}/integration/lib_memsnapshot.sh"
is_virtual_machine || skip "live workload requires a disposable VM"
require_commands jq timeout
observability_workers=()
observability_cleanup() {
	local pid
	for pid in "${observability_workers[@]}"; do
		kill -TERM "${pid}" 2> /dev/null || true
	done
	for pid in "${observability_workers[@]}"; do wait "${pid}" 2> /dev/null || true; done
}
observability_exit() {
	local status=$?
	observability_cleanup
	huatuo_bamai_stop || status=1
	return "${status}"
}
trap observability_exit EXIT
observability_event_ready() {
	local tracer=$1
	[[ -s ${HUATUO_BAMAI_TEST_TMPDIR}/events/${tracer} ]] \
		&& jq -e -s 'length > 0' "${HUATUO_BAMAI_TEST_TMPDIR}/events/${tracer}" > /dev/null 2>&1
}
assert_observability_stop() {
	huatuo_bamai_stop_with_signal TERM
	assert_memsnapshot_logs "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log"
}
