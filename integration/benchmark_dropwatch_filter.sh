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

# Manually compare the real dropwatch tracepoint hot path for two BPF
# object/binary combinations. Results are measurements, not pass/fail gates.

set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT_DIR=$(cd "${SCRIPT_DIR}/.." && pwd)
export ROOT_DIR
source "${ROOT_DIR}/integration/lib.sh"

BASELINE_BIN="${ROOT_DIR}/_output/bin/dropwatch"
CANDIDATE_BIN="${BASELINE_BIN}"
BASELINE_BPF=""
CANDIDATE_BPF=""
EVENTS=10000
ROUNDS=5
RATIOS="0,90,99"
SETTLE_SECONDS=1
RESULT_DIR="${ROOT_DIR}/_output/benchmark/dropwatch-filter-$(date -u +%Y%m%dT%H%M%SZ)"

usage() {
	cat << EOF
Usage: sudo $0 --baseline-bpf PATH --candidate-bpf PATH [options]

Options:
  --baseline-bin PATH   baseline dropwatch binary (default: ${BASELINE_BIN})
  --candidate-bin PATH  candidate dropwatch binary (default: baseline binary)
  --events N            generated drops per sample (default: ${EVENTS})
  --rounds N            samples per variant and ratio (default: ${ROUNDS})
  --ratios LIST         requested reject percentages (default: ${RATIOS})
  --settle-seconds N    wait after traffic before reading stats (default: ${SETTLE_SECONDS})
  --result-dir PATH     preserved report and raw-output directory
  -h, --help            show this help
EOF
}

while (($# > 0)); do
	case "$1" in
	--baseline-bpf)
		BASELINE_BPF=$2
		shift 2
		;;
	--candidate-bpf)
		CANDIDATE_BPF=$2
		shift 2
		;;
	--baseline-bin)
		BASELINE_BIN=$2
		shift 2
		;;
	--candidate-bin)
		CANDIDATE_BIN=$2
		shift 2
		;;
	--events)
		EVENTS=$2
		shift 2
		;;
	--rounds)
		ROUNDS=$2
		shift 2
		;;
	--ratios)
		RATIOS=$2
		shift 2
		;;
	--settle-seconds)
		SETTLE_SECONDS=$2
		shift 2
		;;
	--result-dir)
		RESULT_DIR=$2
		shift 2
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		usage >&2
		fatal "unknown argument: $1"
		;;
	esac
done

[[ ${EUID} -eq 0 ]] || fatal "requires root"
[[ -n "${BASELINE_BPF}" ]] || fatal "--baseline-bpf is required"
[[ -n "${CANDIDATE_BPF}" ]] || fatal "--candidate-bpf is required"
[[ -x "${BASELINE_BIN}" ]] || fatal "baseline binary is not executable: ${BASELINE_BIN}"
[[ -x "${CANDIDATE_BIN}" ]] || fatal "candidate binary is not executable: ${CANDIDATE_BIN}"
[[ -r "${BASELINE_BPF}" ]] || fatal "baseline BPF object is not readable: ${BASELINE_BPF}"
[[ -r "${CANDIDATE_BPF}" ]] || fatal "candidate BPF object is not readable: ${CANDIDATE_BPF}"
[[ "${EVENTS}" =~ ^[1-9][0-9]*$ ]] || fatal "--events must be a positive integer"
[[ "${ROUNDS}" =~ ^[1-9][0-9]*$ ]] || fatal "--rounds must be a positive integer"
[[ "${SETTLE_SECONDS}" =~ ^[0-9]+([.][0-9]+)?$ ]] \
	|| fatal "--settle-seconds must be non-negative"

for command in bpftool ip iptables jq python3 sha256sum; do
	command -v "${command}" > /dev/null 2>&1 || fatal "required command not found: ${command}"
done

IFS=',' read -r -a REJECT_RATIOS <<< "${RATIOS}"
for ratio in "${REJECT_RATIOS[@]}"; do
	[[ "${ratio}" =~ ^(0|[1-9][0-9]?)$ ]] || fatal "invalid reject ratio: ${ratio}"
done

readonly BPF_STATS_FILE=/proc/sys/kernel/bpf_stats_enabled
[[ -r "${BPF_STATS_FILE}" && -w "${BPF_STATS_FILE}" ]] \
	|| fatal "kernel BPF statistics control is unavailable: ${BPF_STATS_FILE}"

mkdir -p "${RESULT_DIR}"
RESULT_DIR=$(cd "${RESULT_DIR}" && pwd)
readonly REPORT_CSV="${RESULT_DIR}/samples.csv"
readonly SUMMARY_CSV="${RESULT_DIR}/summary.csv"
readonly ENVIRONMENT_TXT="${RESULT_DIR}/environment.txt"
readonly NETNS="dwbench-$$"
readonly MATCH_PORT=39000
readonly REJECT_PORT=39001

ORIGINAL_BPF_STATS=$(< "${BPF_STATS_FILE}")
DROPWATCH_PID=""

cleanup() {
	local code=$?
	if [[ -n "${DROPWATCH_PID}" ]]; then
		kill -TERM "${DROPWATCH_PID}" 2> /dev/null || true
		wait "${DROPWATCH_PID}" 2> /dev/null || true
	fi
	ip netns del "${NETNS}" 2> /dev/null || true
	printf '%s\n' "${ORIGINAL_BPF_STATS}" > "${BPF_STATS_FILE}" 2> /dev/null || true
	if ((code != 0)); then
		log_error "benchmark artifacts preserved at ${RESULT_DIR}"
	fi
}
trap cleanup EXIT

printf '1\n' > "${BPF_STATS_FILE}"
[[ "$(< "${BPF_STATS_FILE}")" == "1" ]] || fatal "failed to enable BPF program statistics"

ip netns add "${NETNS}"
ip netns exec "${NETNS}" ip link set lo up
ip netns exec "${NETNS}" iptables -I OUTPUT 1 -p udp \
	--dport "${MATCH_PORT}:${REJECT_PORT}" -j DROP

{
	echo "timestamp_utc=$(date -u +%FT%TZ)"
	echo "git_commit=$(git -C "${ROOT_DIR}" rev-parse HEAD 2> /dev/null || echo unknown)"
	echo "kernel=$(uname -srvo)"
	echo "architecture=$(uname -m)"
	echo "online_cpus=$(getconf _NPROCESSORS_ONLN)"
	if command -v lscpu > /dev/null 2>&1; then
		lscpu | grep -E '^(Model name|CPU\(s\)|Thread|Core|Socket|NUMA)' || true
	fi
	echo "bpftool=$(bpftool version | head -1)"
	echo "bpf_stats_enabled_original=${ORIGINAL_BPF_STATS}"
	echo "events_per_sample=${EVENTS}"
	echo "rounds=${ROUNDS}"
	echo "reject_ratios=${RATIOS}"
	echo "baseline_binary=${BASELINE_BIN}"
	echo "baseline_binary_sha256=$(sha256sum "${BASELINE_BIN}" | awk '{print $1}')"
	echo "baseline_bpf=${BASELINE_BPF}"
	echo "baseline_bpf_sha256=$(sha256sum "${BASELINE_BPF}" | awk '{print $1}')"
	echo "candidate_binary=${CANDIDATE_BIN}"
	echo "candidate_binary_sha256=$(sha256sum "${CANDIDATE_BIN}" | awk '{print $1}')"
	echo "candidate_bpf=${CANDIDATE_BPF}"
	echo "candidate_bpf_sha256=$(sha256sum "${CANDIDATE_BPF}" | awk '{print $1}')"
} > "${ENVIRONMENT_TXT}"

echo 'variant,round,reject_percent,generated,requested_accepted,program_id,run_cnt,run_time_ns,ns_per_event,output_events,dropwatch_cpu_seconds,host_cpu_percent' \
	> "${REPORT_CSV}"

program_ids() {
	bpftool -j prog show | jq -r '.[].id' | sort
}

new_dropwatch_program_id() {
	local before_file=$1 after_file=$2 id info name
	program_ids > "${after_file}"
	while read -r id; do
		[[ -n "${id}" ]] || continue
		info=$(bpftool -j prog show id "${id}" 2> /dev/null || true)
		[[ -n "${info}" ]] || continue
		name=$(jq -r '(if type == "array" then .[0] else . end).name // ""' <<< "${info}")
		if [[ "${name}" == bpf_kfree_skb* ]]; then
			echo "${id}"
			return 0
		fi
	done < <(comm -13 "${before_file}" "${after_file}")
	return 1
}

wait_for_dropwatch_program() {
	local pid=$1 before_file=$2 after_file=$3 attempt program_id
	for ((attempt = 0; attempt < 100; attempt++)); do
		if program_id=$(new_dropwatch_program_id "${before_file}" "${after_file}"); then
			echo "${program_id}"
			return 0
		fi
		kill -0 "${pid}" 2> /dev/null || return 1
		sleep 0.05
	done
	return 1
}

program_stats() {
	local program_id=$1
	bpftool -j prog show id "${program_id}" \
		| jq -r '(if type == "array" then .[0] else . end)
			| "\(.run_time_ns // 0) \(.run_cnt // 0)"'
}

process_cpu_ticks() {
	local pid=$1
	sed 's/^.*) //' "/proc/${pid}/stat" | awk '{print $12 + $13}'
}

host_cpu_sample() {
	awk '/^cpu / {
		total = 0
		for (i = 2; i <= NF; i++) total += $i
		print total, $5 + $6
	}' /proc/stat
}

generate_drops() {
	local reject_percent=$1
	ip netns exec "${NETNS}" python3 - \
		"${EVENTS}" "${reject_percent}" "${MATCH_PORT}" "${REJECT_PORT}" << 'PY'
import errno
import socket
import sys

count = int(sys.argv[1])
reject_percent = int(sys.argv[2])
match_port = int(sys.argv[3])
reject_port = int(sys.argv[4])

sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
for index in range(count):
    port = reject_port if index % 100 < reject_percent else match_port
    try:
        sock.sendto(b"x", ("127.0.0.1", port))
    except OSError as error:
        if error.errno != errno.EPERM:
            raise
sock.close()
PY
}

run_sample() {
	local variant=$1 binary=$2 bpf_object=$3 reject_percent=$4 round=$5
	local prefix="${RESULT_DIR}/${variant}-reject${reject_percent}-round${round}"
	local before_ids="${prefix}.ids.before" after_ids="${prefix}.ids.after"
	local stdout_file="${prefix}.jsonl" stderr_file="${prefix}.stderr"
	local program_id runtime_before count_before
	local runtime_after count_after runtime_delta count_delta ns_per_event output_events
	local proc_before proc_after clock_ticks process_cpu host_cpu
	local total_before idle_before total_after idle_after total_delta idle_delta
	local requested_accepted=$((EVENTS * (100 - reject_percent) / 100))

	program_ids > "${before_ids}"
	"${binary}" --bpf-path "${bpf_object}" \
		--filter "udp and dst port ${MATCH_PORT}" \
		--max-events-per-second 0 --output json \
		> "${stdout_file}" 2> "${stderr_file}" &
	DROPWATCH_PID=$!

	program_id=$(wait_for_dropwatch_program "${DROPWATCH_PID}" "${before_ids}" "${after_ids}") \
		|| fatal "${variant} did not attach bpf_kfree_skb_prog; see ${stderr_file}"
	read -r runtime_before count_before <<< "$(program_stats "${program_id}")"
	proc_before=$(process_cpu_ticks "${DROPWATCH_PID}")
	read -r total_before idle_before <<< "$(host_cpu_sample)"

	generate_drops "${reject_percent}"
	sleep "${SETTLE_SECONDS}"

	read -r total_after idle_after <<< "$(host_cpu_sample)"
	proc_after=$(process_cpu_ticks "${DROPWATCH_PID}")
	read -r runtime_after count_after <<< "$(program_stats "${program_id}")"

	kill -TERM "${DROPWATCH_PID}"
	wait "${DROPWATCH_PID}" || fatal "${variant} exited unsuccessfully; see ${stderr_file}"
	DROPWATCH_PID=""

	runtime_delta=$((runtime_after - runtime_before))
	count_delta=$((count_after - count_before))
	output_events=$(wc -l < "${stdout_file}")
	clock_ticks=$(getconf CLK_TCK)
	process_cpu=$(awk -v ticks="$((proc_after - proc_before))" -v hz="${clock_ticks}" \
		'BEGIN { printf "%.6f", ticks / hz }')
	if ((count_delta > 0)); then
		ns_per_event=$(awk -v runtime="${runtime_delta}" -v count="${count_delta}" \
			'BEGIN { printf "%.2f", runtime / count }')
	else
		ns_per_event="nan"
	fi
	total_delta=$((total_after - total_before))
	idle_delta=$((idle_after - idle_before))
	if ((total_delta > 0)); then
		host_cpu=$(awk -v total="${total_delta}" -v idle="${idle_delta}" \
			'BEGIN { printf "%.2f", (total - idle) * 100 / total }')
	else
		host_cpu="nan"
	fi

	printf '%s,%d,%d,%d,%d,%d,%d,%d,%s,%d,%s,%s\n' \
		"${variant}" "${round}" "${reject_percent}" "${EVENTS}" \
		"${requested_accepted}" "${program_id}" "${count_delta}" \
		"${runtime_delta}" "${ns_per_event}" "${output_events}" \
		"${process_cpu}" "${host_cpu}" | tee -a "${REPORT_CSV}"
}

for ((round = 1; round <= ROUNDS; round++)); do
	for reject_percent in "${REJECT_RATIOS[@]}"; do
		run_sample baseline "${BASELINE_BIN}" "${BASELINE_BPF}" "${reject_percent}" "${round}"
		run_sample candidate "${CANDIDATE_BIN}" "${CANDIDATE_BPF}" "${reject_percent}" "${round}"
	done
done

python3 - "${REPORT_CSV}" "${SUMMARY_CSV}" << 'PY'
import csv
import math
import statistics
import sys

samples_path, summary_path = sys.argv[1:]
fields = (
    "run_cnt",
    "run_time_ns",
    "ns_per_event",
    "output_events",
    "dropwatch_cpu_seconds",
    "host_cpu_percent",
)
groups = {}
with open(samples_path, newline="", encoding="utf-8") as samples_file:
    for row in csv.DictReader(samples_file):
        groups.setdefault((row["variant"], row["reject_percent"]), []).append(row)

with open(summary_path, "w", newline="", encoding="utf-8") as summary_file:
    writer = csv.writer(summary_file)
    writer.writerow(("variant", "reject_percent", "samples") + tuple(
        f"median_{field}" for field in fields
    ))
    for (variant, reject_percent), rows in sorted(groups.items()):
        medians = []
        for field in fields:
            values = [float(row[field]) for row in rows]
            values = [value for value in values if math.isfinite(value)]
            medians.append(f"{statistics.median(values):.2f}" if values else "nan")
        writer.writerow((variant, reject_percent, len(rows), *medians))
PY

log_info "raw samples: ${REPORT_CSV}"
log_info "median summary: ${SUMMARY_CSV}"
log_info "environment: ${ENVIRONMENT_TXT}"
log_info "this benchmark reports measurements and enforces no performance threshold"
