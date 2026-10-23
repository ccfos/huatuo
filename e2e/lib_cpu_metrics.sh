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

cpu_metrics_query_pids=()

cpu_metrics_prepare() {
	CPU_METRICS_BIN=${CPU_METRICS_HELPER_BIN:-${ROOT_DIR}/_output/tests/cpu-metrics}
}

cpu_metrics_read() {
	"${CPU_METRICS_BIN}" --url "${HUATUO_BAMAI_METRICS_API}" --raw-output "$1.prom" > "$1"
}

assert_cpu_tracing_status() {
	local tracing=$1 enabled=$2 file="${HUATUO_BAMAI_TEST_TMPDIR}/tracing-status-${BASHPID}.json"
	cpu_metrics_read "${file}" || return 1
	assert_cpu_tracing_metrics "${tracing}" "${enabled}" "${file}"
}

assert_cpu_tracing_metrics() {
	local tracing=$1 enabled=$2 file=$3
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

# Keep metric structure checks shared while each case owns its numeric expectations.
CPU_METRICS_JQ='
def container_labels($t; $host):
 {host: $host, region: "e2e", container_host: $t.Pod, container_name: $t.Container,
  container_hostnamespace: $t.Namespace, container_type: ($t.Type // "normal"),
  container_level: ($t.QoS // "burstable")};
def cpu_metric($f; $labels):
 [$f.samples[]? | select(.labels | contains($labels))] as $rows |
 if $f.type == "GAUGE" and ($f.help | type == "string" and length > 0)
  and ($rows | length) == 1 and $rows[0].labels == $labels
  and ($rows[0].value | type == "number" and . >= 0)
 then $rows[0].value else error("invalid Gauge sample for " + ($labels | tostring)) end;
def collector_ok($families; $collector):
 $families.huatuo_bamai_scrape_collector_success as $f |
 [$f.samples[]? | select(.labels.collector == $collector)] as $rows |
 $f.type == "GAUGE" and ($rows | length) == 1 and $rows[0].value == 1;
'

assert_cpu_metrics_absent() {
	local collector=$1 targets=$2 file="${HUATUO_BAMAI_TEST_TMPDIR}/absent-${BASHPID}.json"
	cpu_metrics_read "${file}" || return 1
	jq -e --arg prefix "huatuo_bamai_${collector}_" --slurpfile targets "${targets}" '
  [. | to_entries[] | select(.key | startswith($prefix)) | .value.samples[]? |
   .labels as $labels | select(any($targets[0][];
    .Pod == $labels.container_host and .Container == $labels.container_name
    and .Namespace == $labels.container_hostnamespace))] | length == 0
 ' "${file}" > /dev/null
}

assert_cpu_collector_disabled() {
	local collector=$1 file="${HUATUO_BAMAI_TEST_TMPDIR}/disabled-${BASHPID}.json"
	cpu_metrics_read "${file}" || return 1
	jq -e --arg collector "${collector}" --arg prefix "huatuo_bamai_${collector}_" '
  (keys | all(.[]; startswith($prefix) | not))
  and ([.huatuo_bamai_scrape_collector_success.samples[]? | select(.labels.collector == $collector)] | length == 0)
 ' "${file}" > /dev/null
}

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

cpu_metrics_wait_queries() {
	local status=0
	while ((${#cpu_metrics_query_pids[@]})); do
		wait "${cpu_metrics_query_pids[0]}" || status=1
		cpu_metrics_query_pids=("${cpu_metrics_query_pids[@]:1}")
	done
	return "${status}"
}

cpu_metrics_stop_queries() {
	local pid
	for pid in "${cpu_metrics_query_pids[@]}"; do kill -TERM "${pid}" 2> /dev/null || true; done
	for pid in "${cpu_metrics_query_pids[@]}"; do stop_and_wait_by_pid "${pid}" 5 || true; done
	cpu_metrics_query_pids=()
}

cpu_metrics_query_batch() {
	local directory=$1 workers=$2 rounds=$3 assertion=$4 round query file
	for ((round = 1; round <= rounds; round++)); do
		for ((query = 1; query <= workers; query++)); do
			file="${directory}/query-${round}-${query}.json"
			"${CPU_METRICS_BIN}" --url "${HUATUO_BAMAI_METRICS_API}" --raw-output "${file}.prom" \
				> "${file}" 2> "${file}.err" &
			cpu_metrics_query_pids+=("$!")
		done
		cpu_metrics_wait_queries || return 1
		for ((query = 1; query <= workers; query++)); do
			"${assertion}" "${directory}/query-${round}-${query}.json" || return 1
		done
	done
}
