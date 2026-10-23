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

set -euo pipefail

# Keep observing after a successful sample so late failures are still detected.
cpu_metrics_observe() {
	local duration=$1 interval=$2 deadline
	shift 2
	deadline=$(($(date +%s%N) + duration * 1000000000))
	while true; do
		"$@" || return 1
		(($(date +%s%N) < deadline)) || return 0
		sleep "${interval}"
	done
}

cpu_metrics_prepare() {
	CPU_METRICS_BIN="$1/metrics"
	if [[ -n ${CPU_METRICS_HELPER_BIN:-} ]]; then
		cp "${CPU_METRICS_HELPER_BIN}" "${CPU_METRICS_BIN}"
	else
		require_commands go
		CGO_ENABLED=0 go build -mod=vendor -o "${CPU_METRICS_BIN}" \
			"${ROOT_DIR}/e2e/testdata/test_metrics.go" "${ROOT_DIR}/e2e/testdata/metrics_helpers.go"
	fi
}

cpu_metrics_read() {
	"${CPU_METRICS_BIN}" --url "${HUATUO_BAMAI_METRICS_API}" > "$1"
}

assert_cpu_tracing_status() {
	local tracing=$1 enabled=$2 file="${HUATUO_BAMAI_TEST_TMPDIR}/tracing-status-${BASHPID}.json"
	cpu_metrics_read "${file}" || return 1
	jq -e --arg tracing "${tracing}" --arg host "$(hostname)" --argjson enabled "${enabled}" '
  def valid_labels: .labels.host == $host and .labels.region == "e2e";
  .huatuo_bamai_tracing_status_running as $running |
  .huatuo_bamai_tracing_status_hitcount as $hits |
  ($running.type == "GAUGE" and ($running.samples | length) == 1
   and ($running.samples[0] | valid_labels and .value == $enabled))
  and ([$hits.samples[]? | select(.labels.tracing == $tracing)] |
   length == $enabled and all(.[]; valid_labels and .value == 0))
  and (if $enabled == 1 then $hits.type == "GAUGE" else true end)
 ' "${file}" > /dev/null
}
