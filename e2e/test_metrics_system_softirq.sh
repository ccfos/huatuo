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
source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"
source "${ROOT_DIR}/integration/lib_cpu_metrics.sh"

require_commands ip unshare taskset awk jq ss "${STRESS_NG_BIN:-stress-ng}"
require_readable /proc/softirqs /proc/self/status
unshare --net ip link set dev lo up \
	> "${HUATUO_BAMAI_TEST_TMPDIR}/softirq-netns-probe.log" 2>&1 \
	|| skip "network namespace or loopback setup is unavailable; inspect ${HUATUO_BAMAI_TEST_TMPDIR}/softirq-netns-probe.log"
for event in softirq_raise softirq_entry; do
	tracepoint_available irq "${event}" || skip "irq/${event} tracepoint is unavailable"
done

readonly SOFTIRQ_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/softirq"
readonly SOFTIRQ_STRESS_NG=$(command -v "${STRESS_NG_BIN:-stress-ng}")
softirq_disabled=false
softirq_netem=false
softirq_generation=0
softirq_load_pids=()
softirq_load_cpus=()
mkdir -p "${SOFTIRQ_DIR}"

softirq_cleanup() {
	local status=$? pid
	trap - EXIT
	cpu_metrics_stop_queries
	for pid in "${softirq_load_pids[@]}"; do
		stop_and_wait_by_pid "${pid}" || status=1
	done
	huatuo_bamai_stop || status=1
	exit "${status}"
}
trap softirq_cleanup EXIT
trap 'exit 143' TERM
trap 'exit 130' INT

cpu_metrics_prepare
uname -a > "${SOFTIRQ_DIR}/kernel.txt"
"${HUATUO_BAMAI_BIN}" --version > "${SOFTIRQ_DIR}/bamai-version.txt"
"${SOFTIRQ_STRESS_NG}" --version | tee "${SOFTIRQ_DIR}/stress-ng-version.txt"
mapfile -t softirq_cpus < <(awk '/^Cpus_allowed_list:/ {
 n=split($2, ranges, ",")
 for (i=1; i<=n; i++) {
  m=split(ranges[i], ends, "-"); last=(m==2 ? ends[2] : ends[1])
  for (cpu=ends[1]; cpu<=last; cpu++) print cpu
 }
}' /proc/self/status)
readonly SOFTIRQ_MAX_CPU=$(awk 'NR == 1 { sub(/^CPU/, "", $NF); print $NF; exit }' /proc/softirqs)

# Loopback traffic alone need not schedule NET_TX. A delayed qdisc exercises
# deferred transmission without changing any host interface or network setting.
if command -v tc > /dev/null && unshare --net bash -ec '
 ip link set dev lo up
 tc qdisc add dev lo root netem delay 1ms
' > "${SOFTIRQ_DIR}/netem-probe.log" 2>&1; then
	softirq_netem=true
else
	log_info "softirq: NET_TX workload skipped; tc/netem is unavailable (NET_TX metrics are still validated)"
fi

softirq_restart() {
	local signal=${1:-TERM}
	if [[ -f ${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid ]]; then
		assert_huatuo_bamai_shutdown_with_signal "${signal}"
		assert_huatuo_bamai_log_clean || fatal "softirq daemon has unexpected errors"
		cp "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${SOFTIRQ_DIR}/daemon-${softirq_generation}.log"
	fi
	softirq_generation=$((softirq_generation + 1))
	integration_huatuo_bamai_start write_config_bamai_softirq --region e2e --disable-storage --disable-kubelet
}

assert_softirq_metrics() {
	local file=$1
	assert_cpu_tracing_metrics softirq 1 "${file}" || return 1
	jq -e --arg host "$(hostname)" --argjson max_cpu "${SOFTIRQ_MAX_CPU}" "${CPU_METRICS_JQ}"'
 .huatuo_bamai_softirq_latency as $f |
 if collector_ok(.; "softirq") and $f.type == "COUNTER"
  and ($f.help | type == "string" and length > 0)
  and ($f.samples | length) == 2 * ($max_cpu + 1) * 4
  and all($f.samples[];
   .labels == {host: $host, region: "e2e", type: .labels.type,
    cpuid: .labels.cpuid, zone: .labels.zone}
   and (.labels.type as $type | ["NET_RX", "NET_TX"] | index($type) != null)
   and (.value | type == "number" and . >= 0 and . <= 9007199254740992 and floor == .))
 then reduce ["NET_RX", "NET_TX"][] as $type ({};
  .[$type] = (reduce range(0; $max_cpu + 1) as $cpu ({};
   .[$cpu | tostring] = (
    [$f.samples[] | select(.labels.type == $type and .labels.cpuid == ($cpu | tostring))]
    | sort_by(.labels.zone)
    | if map(.labels.zone) == ["0", "1", "2", "3"] then map(.value)
      else error("missing or duplicate softirq zones: " + $type + "/" + ($cpu | tostring)) end))))
 else error("missing or invalid softirq counters") end
 ' "${file}"
}

softirq_snapshot() {
	local output=$1
	cpu_metrics_read "${output}.metrics.json" || return 1
	assert_softirq_metrics "${output}.metrics.json" > "${output}.json"
}

softirq_reference() {
	local output=$1
	cp /proc/softirqs "${output}.softirqs"
	awk '
 NR == 1 { for (i=1; i<=NF; i++) { sub(/^CPU/, "", $i); cpus[i]=$i }; next }
 $1 == "NET_RX:" || $1 == "NET_TX:" {
  sub(/:$/, "", $1)
  for (i=2; i<=NF; i++) print $1, cpus[i-1], $i
 }' "${output}.softirqs" | jq -Rn '
 reduce inputs as $line ({}; ($line | split(" ")) as $v |
  .[$v[0]][$v[1]] = ($v[2] | tonumber))
 ' > "${output}.json"
}

softirq_start_load() {
	local cpu=$1
	log_info "softirq workload: cpu=${cpu} udp=1 netem=${softirq_netem}"
	unshare --net bash -ec '
  ip link set dev lo up
  if [[ $1 == true ]]; then tc qdisc add dev lo root netem delay 1ms; fi
  exec taskset -c "$2" "$3" --udp 1 --udp-domain ipv4 --udp-port 19001 --timeout 300s --metrics-brief
 ' softirq-load "${softirq_netem}" "${cpu}" "${SOFTIRQ_STRESS_NG}" \
		> "${SOFTIRQ_DIR}/load-cpu-${cpu}.log" 2>&1 &
	softirq_load_pids+=("$!")
	softirq_load_cpus+=("${cpu}")
}

softirq_load_ready() {
	local pid
	for pid in "${softirq_load_pids[@]}"; do
		kill -0 "${pid}" || return 1
	done
	softirq_snapshot "${SOFTIRQ_DIR}/load-ready" || return 1
	jq -e --argjson cpus "$(printf '%s\n' "${softirq_load_cpus[@]}" | jq -R . | jq -s .)" '
 .NET_RX as $rx | all($cpus[]; ($rx[.] | add) > 0)
 ' "${SOFTIRQ_DIR}/load-ready.json" > /dev/null
}

assert_softirq_delta() {
	local before=$1 after=$2 reference=$3 output=$4
	# /proc/softirqs counts entries, whereas BPF counts entries paired with a
	# raise. Bracket the scrape interval and allow one in-flight entry per CPU.
	jq -en --slurpfile before "${before}.json" --slurpfile after "${after}.json" \
		--slurpfile lower "${reference}.json" --slurpfile upper "${output}-reference.json" '
 ["NET_RX", "NET_TX"] | map(. as $type | {key: $type, value:
  ($after[0][$type] | to_entries | map(. as $row | {key: $row.key, value: {
   zones: [range(0; 4) as $zone | $row.value[$zone] - $before[0][$type][$row.key][$zone]],
   entries: ($upper[0][$type][$row.key] - $lower[0][$type][$row.key])}}) | from_entries)})
 | from_entries
 ' > "${output}-growth.json"
	jq -e '
 all(.[][]; all(.zones[]; . >= 0) and (.zones | add) <= .entries + 1)
 ' "${output}-growth.json" > /dev/null \
		|| fatal "softirq counters decreased or exceeded kernel entry counts; inspect ${output}-growth.json"
}

softirq_sample() {
	local phase=$1 pid output
	for pid in "${softirq_load_pids[@]}"; do
		kill -0 "${pid}" || fatal "UDP workload pid=${pid} exited; inspect ${SOFTIRQ_DIR}/load-cpu-*.log"
	done
	softirq_sample_index=$((softirq_sample_index + 1))
	output="${SOFTIRQ_DIR}/${phase}-sample-${softirq_sample_index}"
	softirq_reference "${output}-reference-before"
	softirq_snapshot "${output}" || return 1
	softirq_reference "${output}-reference"
	assert_softirq_delta "${softirq_previous}" "${output}" "${softirq_previous_reference}" "${output}"
	softirq_previous=${output}
	softirq_previous_reference="${output}-reference-before"
}

softirq_phase() {
	local phase=$1 duration=${2:-10} growth
	log_info "softirq phase: ${phase} (${duration}-second measurement window)"
	softirq_reference "${SOFTIRQ_DIR}/${phase}-reference-before"
	wait_until 15 1 softirq_snapshot "${SOFTIRQ_DIR}/${phase}-before" || fatal "softirq metrics did not become ready"
	softirq_sample_index=0
	softirq_previous="${SOFTIRQ_DIR}/${phase}-before"
	softirq_previous_reference="${SOFTIRQ_DIR}/${phase}-reference-before"
	cpu_metrics_observe "${duration}" 1 softirq_sample "${phase}" || fatal "${phase}: invalid softirq metrics"
	softirq_reference "${SOFTIRQ_DIR}/${phase}-reference"
	assert_softirq_delta "${SOFTIRQ_DIR}/${phase}-before" "${softirq_previous}" \
		"${SOFTIRQ_DIR}/${phase}-reference-before" "${SOFTIRQ_DIR}/${phase}"
	growth="${SOFTIRQ_DIR}/${phase}-growth.json"
	jq '{NET_RX: [.NET_RX[].zones] | transpose | map(add), NET_TX: [.NET_TX[].zones] | transpose | map(add)}' "${growth}"
	if ((${#softirq_load_cpus[@]})); then
		jq -e --argjson cpus "$(printf '%s\n' "${softirq_load_cpus[@]}" | jq -R . | jq -s .)" \
			--argjson tx "${softirq_netem}" '
   .NET_RX as $rx | all($cpus[]; ($rx[.].zones | add) > 0)
   and (if $tx then ([.NET_TX[].zones | add] | add) > 0 else true end)
  ' "${growth}" > /dev/null || fatal "${phase}: network workload produced no expected softirq growth; inspect ${growth}"
	fi
}

softirq_restart
softirq_phase baseline 2
softirq_start_load "${softirq_cpus[-1]}"
wait_until 10 1 softirq_load_ready || fatal "UDP workload did not produce NET_RX samples"
softirq_phase single-cpu
if ((${#softirq_cpus[@]} >= 2)); then
	softirq_start_load "${softirq_cpus[-2]}"
	wait_until 10 1 softirq_load_ready || fatal "second CPU produced no NET_RX samples"
	softirq_phase multi-cpu
fi
cpu_metrics_query_batch "${SOFTIRQ_DIR}" 4 4 assert_softirq_metrics \
	> "${SOFTIRQ_DIR}/concurrent-zones.json" || fatal "concurrent softirq queries failed; inspect query files"
for softirq_signal in TERM KILL; do
	softirq_restart "${softirq_signal}"
	softirq_phase "after-${softirq_signal}"
done
softirq_disabled=true
softirq_restart
assert_cpu_collector_disabled softirq || fatal "blacklisted softirq still exports metrics"
assert_cpu_tracing_status softirq 0 || fatal "blacklisted softirq is still running"
softirq_disabled=false
softirq_restart
softirq_phase reenabled
assert_huatuo_bamai_shutdown_with_signal TERM
assert_huatuo_bamai_log_clean || fatal "softirq daemon has unexpected errors"
log_info "softirq E2E passed; artifacts: ${SOFTIRQ_DIR}"
