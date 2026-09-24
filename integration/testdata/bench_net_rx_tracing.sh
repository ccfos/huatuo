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

# Reproduce the net_rx_latency entry point comparison: one kernel, one machine,
# one traffic generator, one threshold, measured round by round
#   - with no probe attached (baseline),
#   - with the kprobe entry point of tcp_v4_rcv forced,
#   - with the fentry entry point of tcp_v4_rcv forced.
#
# The entry point is forced by the test fixture (`-mode`), never by production
# configuration, and the measured window starts only after the fixture reports
# the attach, so the load and probe phase is outside every round.
#
# The generator sends TCP SYNs to a closed port of the peer namespace, which is
# what keeps the packet rate high enough to see a per-packet hook: the slow TCP
# server fixture serializes on purpose and cannot drive one. Every SYN enters
# tcp_v4_rcv, and the peer's RST re-enters it on the other side, so the offered
# rate is the control and the CPU time is the signal. A generator-limited rate
# means a more expensive hook shows up as CPU, not as fewer packets; that is why
# both columns are reported.
#
# The object rate-limits its events (BPF_RATELIMIT, 100 per second), so the
# fixture reports about 100 events per second no matter how many packets pass:
# that is production behaviour and it keeps the userspace cost equal across the
# modes. The cost that differs is the entry itself plus the latency check, which
# every packet pays.
#
# This script asserts nothing about performance. It prints every round with its
# raw counters, summarizes mean/min/max per mode, and leaves the raw JSON on
# disk. Report the numbers as they are, including a missing gain.
#
# Run it as root from the repository root:
#   ROUNDS=5 ROUND_SECONDS=10 ./integration/testdata/bench_net_rx_tracing.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/../../integration/env.sh"
source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_namespace.sh"

[[ "${EUID}" -eq 0 ]] || fatal "this benchmark needs root to load BPF programs"

# The slow TCP server fixture binds 10.200.1.2, so the pair matches the other
# net_rx_latency tests. No listener runs on BENCH_PORT: the SYN is answered with
# a RST, which is irrelevant to the inbound hook under test.
SERVER_IP="10.200.1.2"
CLIENT_IP="10.200.1.1"
BENCH_PORT=19877

readonly ROUNDS=${ROUNDS:-5}
readonly ROUND_SECONDS=${ROUND_SECONDS:-10}
readonly WARMUP_ROUNDS=${WARMUP_ROUNDS:-1}
readonly FIXTURE_EVENTS=5000000

readonly MODE_BASELINE="none"
readonly MODES=("kprobe" "fentry")

readonly GO_CACHE_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/go-cache"
readonly GO_TMP_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/go-tmp"
WORK_DIR=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/net-rx-bench.XXXXXX")
readonly RESULTS="${WORK_DIR}/rounds.tsv"
readonly GENERATOR="${WORK_DIR}/syn_generator.py"
FIXTURE_BIN="${WORK_DIR}/net_rx_tracing"
FIXTURE_PID=""

cleanup_all() {
	[[ -z "${FIXTURE_PID}" ]] || stop_by_pid "${FIXTURE_PID}" 2 || true
	tcp_namespace_cleanup
	rm -rf -- "${GO_CACHE_DIR}" "${GO_TMP_DIR}"
}
trap cleanup_all EXIT

clock_ticks=$(getconf CLK_TCK) || fatal "cannot determine kernel clock tick rate"
[[ "${clock_ticks}" =~ ^[1-9][0-9]*$ ]] || fatal "invalid kernel clock tick rate: ${clock_ticks}"

for object in net_rx_latency.o net_rx_latency_fentry.o; do
	[[ -r "${ROOT_DIR}/_output/bpf/${object}" ]] \
		|| fatal "missing ${object}; run make build first"
done
require_python3
command -v jq > /dev/null 2>&1 || fatal "jq not found"

# The baseline round cannot be skipped: without it the probe's CPU cost is not
# separable from the cost of the traffic itself.
[[ "${ROUNDS}" =~ ^[1-9][0-9]*$ ]] || fatal "ROUNDS must be a positive integer"
[[ "${ROUND_SECONDS}" =~ ^[1-9][0-9]*$ ]] || fatal "ROUND_SECONDS must be a positive integer"

# ------------------------------- fixtures -----------------------------------

compile_go_fixture() {
	local build_log="${WORK_DIR}/fixture.build.log"

	mkdir -p "${GO_CACHE_DIR}" "${GO_TMP_DIR}"
	log_info "building fixture: test_net_rx_tracing.go"
	GOCACHE="${GO_CACHE_DIR}" \
		GOTMPDIR="${GO_TMP_DIR}" \
		go build -mod=vendor -tags=integration -o "${FIXTURE_BIN}" \
		"${ROOT_DIR}/integration/testdata/test_net_rx_tracing.go" \
		> "${build_log}" 2>&1 \
		|| fatal "failed to build net_rx_tracing:"$'\n'"$(< "${build_log}")"
}

write_generator() {
	cat > "${GENERATOR}" << 'PY'
import socket
import sys
import time

target = (sys.argv[1], int(sys.argv[2]))
duration = float(sys.argv[3])

sent = 0
end = time.monotonic() + duration
while time.monotonic() < end:
    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    sock.setblocking(False)
    try:
        sock.connect(target)
    except OSError:
        pass
    sock.close()
    sent += 1

print(sent)
PY
}

# ------------------------------- measurement --------------------------------

# read_cpu_counters prints "<busy jiffies> <softirq jiffies>" for all CPUs.
# Busy excludes idle and iowait; the hook runs in softirq context, so that
# column moves first when the entry point gets more expensive.
read_cpu_counters() {
	awk '/^cpu / { printf "%d %d\n", $2 + $3 + $4 + $7 + $8 + $9, $8 }' /proc/stat
}

# wait_for_attach waits for the fixture's ready marker, so the measured window
# never overlaps the load and probe phase.
wait_for_attach() {
	local out=$1 pid=$2 mode=$3
	local attempt=0

	while ((attempt < 40)); do
		grep -q '"ready":true' "${out}" 2> /dev/null && return 0
		kill -0 "${pid}" 2> /dev/null \
			|| fatal "mode ${mode} exited before attaching its entry point"
		sleep 0.5
		attempt=$((attempt + 1))
	done

	fatal "mode ${mode} attached no entry point within 20s"
}

# run_round <label> <mode>
# Runs one measured round and appends its counters to the results file.
run_round() {
	local label=$1 mode=$2
	# The mode belongs in the file names: every mode runs the same label, and
	# the raw rounds are the point of this script.
	local out="${WORK_DIR}/${label}-${mode}.json"
	local err="${WORK_DIR}/${label}-${mode}.err"
	local entry_point="-"
	local events="-"
	local duplicates="-"
	local lost="-"
	local pair_events="-"
	local busy0 softirq0 busy1 softirq1 syns start end elapsed
	local status=0

	if [[ "${mode}" != "${MODE_BASELINE}" ]]; then
		# The timeout bounds the fixture's own collection window, so it only
		# has to outlast the traffic: two seconds of grace, no idle tail.
		"${FIXTURE_BIN}" \
			-bpf-dir "${ROOT_DIR}/_output/bpf" \
			-mode "${mode}" \
			-timeout "$((ROUND_SECONDS + 2))s" \
			-events "${FIXTURE_EVENTS}" \
			> "${out}" 2> "${err}" &
		FIXTURE_PID=$!
		wait_for_attach "${out}" "${FIXTURE_PID}" "${mode}"
	fi

	# Only the generator's window is measured: nothing else runs in it.
	read -r busy0 softirq0 <<< "$(read_cpu_counters)"
	start=$(date +%s.%N)

	syns=$(python3 "${GENERATOR}" "${TCP_NS_SERVER_ADDR}" "${BENCH_PORT}" "${ROUND_SECONDS}")

	end=$(date +%s.%N)
	read -r busy1 softirq1 <<< "$(read_cpu_counters)"

	if [[ "${mode}" != "${MODE_BASELINE}" ]]; then
		wait "${FIXTURE_PID}" || status=$?
		FIXTURE_PID=""
	fi

	elapsed=$(awk -v a="${start}" -v b="${end}" 'BEGIN { printf "%.3f", b - a }')
	[[ "${elapsed}" != "0.000" ]] || fatal "round ${label} measured no elapsed time"

	if [[ "${mode}" != "${MODE_BASELINE}" ]]; then
		entry_point=$(jq -r 'select(.ready == true) | .entry_point' "${out}" | tail -1)
		events=$(jq -r 'select(.summary == true) | .events' "${out}")
		duplicates=$(jq -r 'select(.summary == true) | .duplicates' "${out}")
		lost=$(jq -r 'select(.summary == true) | .lost_samples' "${out}")
		pair_events=$(jq -s \
			--arg saddr "${TCP_NS_CLIENT_ADDR}" --arg daddr "${TCP_NS_SERVER_ADDR}" \
			'[.[] | select(.summary != true) | select(.ready != true)
				| select((.saddr == $saddr and .daddr == $daddr) or (.saddr == $daddr and .daddr == $saddr))]
			 | length' "${out}")
	fi

	local row
	row=$(awk -F'\t' -v OFS='\t' \
		-v label="${label}" -v mode="${mode}" -v elapsed="${elapsed}" \
		-v syns="${syns}" -v busy0="${busy0}" -v busy1="${busy1}" \
		-v softirq0="${softirq0}" -v softirq1="${softirq1}" \
		-v events="${events}" -v pair_events="${pair_events}" \
		-v duplicates="${duplicates}" -v lost="${lost}" \
		-v entry_point="${entry_point}" -v clock="${clock_ticks}" \
		'BEGIN {
			cpu = (busy1 - busy0) / clock
			soft = (softirq1 - softirq0) / clock
			printf "%s\t%s\t%s\t%s\t%.0f\t%.3f\t%.2f\t%.3f\t%.2f\t%s\t%s\t%s\t%s\t%s",
				label, mode, elapsed, syns, syns / elapsed,
				cpu, 100 * cpu / elapsed, soft, 100 * soft / elapsed,
				events, pair_events, duplicates, lost, entry_point
		}')

	printf '%s\n' "${row}" >> "${RESULTS}"
	log_info "round ${label}: mode=${mode} entry_point=${entry_point} syns=${syns} syns/s=$(cut -f5 <<< "${row}") cpu_percent=$(cut -f7 <<< "${row}") softirq_percent=$(cut -f9 <<< "${row}") events=${events} pair_events=${pair_events} duplicates=${duplicates} lost=${lost}"

	# The baseline round attaches nothing, so it has no event counters to
	# judge; its only job is the traffic and CPU reference.
	[[ "${mode}" == "${MODE_BASELINE}" ]] && return 0

	if [[ "${status}" -ne 0 ]]; then
		log_warn "round ${label} fixture exited with ${status}"
		return 1
	fi
	if [[ "${duplicates}" != "0" ]]; then
		log_warn "round ${label} reported ${duplicates} repeated packets; one hook must produce one event per packet"
		return 1
	fi
	if [[ "${events}" == "0" ]]; then
		log_warn "round ${label} reported no event; the probe was attached but saw no packet"
		return 1
	fi
	if [[ "${lost}" != "0" ]]; then
		log_warn "round ${label} lost ${lost} samples; do not compare this round's CPU against a round that lost none"
	fi

	return 0
}

# summarize prints mean/min/max per mode from the results file.
summarize() {
	awk -F'\t' '
		NR == 1 { next }
		{
			m = $2
			if (!(m in seen)) { seen[m] = 1; order[++n] = m }
			rounds[m]++

			rate[m] += $5
			if (!(m in rmin) || $5 < rmin[m]) rmin[m] = $5
			if ($5 > rmax[m]) rmax[m] = $5

			cpu[m] += $7
			if (!(m in cmin) || $7 < cmin[m]) cmin[m] = $7
			if ($7 > cmax[m]) cmax[m] = $7

			soft[m] += $9
			if (!(m in smin) || $9 < smin[m]) smin[m] = $9
			if ($9 > smax[m]) smax[m] = $9

			events[m] += $10
			pair[m] += $11
			duplicates[m] += $12
			lost[m] += $13
		}
		END {
			printf "%-9s %6s %19s %21s %21s %10s %10s %7s %7s\n",
				"mode", "rounds", "syns/s mean[min-max]", "cpu% mean[min-max]",
				"softirq% mean[min-max]", "events", "pair_events", "dupes", "lost"
			for (i = 1; i <= n; i++) {
				m = order[i]
				printf "%-9s %6d %8.0f[%.0f-%.0f] %8.2f[%.2f-%.2f] %8.2f[%.2f-%.2f] %10d %10d %7d %7d\n",
					m, rounds[m],
					rate[m] / rounds[m], rmin[m], rmax[m],
					cpu[m] / rounds[m], cmin[m], cmax[m],
					soft[m] / rounds[m], smin[m], smax[m],
					events[m], pair[m], duplicates[m], lost[m]
			}
		}
	' "${RESULTS}"
}

# ------------------------------- driver -------------------------------------

log_info "kernel: $(uname -r); BTF: $([[ -r /sys/kernel/btf/vmlinux ]] && echo present || echo missing)"
log_info "rounds=${ROUNDS} round_seconds=${ROUND_SECONDS} warmup_rounds=${WARMUP_ROUNDS} work_dir=${WORK_DIR}"

tcp_namespace_setup rxlatbench "${SERVER_IP}" "${CLIENT_IP}"
sleep 0.5

compile_go_fixture
write_generator

printf 'label\tmode\telapsed_s\tsyns\tsyns_per_sec\tcpu_s\tcpu_percent\tsoftirq_s\tsoftirq_percent\tevents\tpair_events\tduplicates\tlost_samples\tentry_point\n' \
	> "${RESULTS}"

failed=0

log_info "warmup: ${WARMUP_ROUNDS} round(s) per mode, not measured"
for mode in "${MODE_BASELINE}" "${MODES[@]}"; do
	for ((warmup = 1; warmup <= WARMUP_ROUNDS; warmup++)); do
		run_round "warmup-${warmup}" "${mode}" || failed=1
	done
done

for ((round = 1; round <= ROUNDS; round++)); do
	# Alternate the entry points inside a round, so drift in the machine hits
	# both modes instead of one half of the run.
	for mode in "${MODE_BASELINE}" "${MODES[@]}"; do
		run_round "round-${round}" "${mode}" || failed=1
	done
done

echo
log_info "results"
summarize
echo
log_info "raw rounds: ${RESULTS}"
log_info "raw events: ${WORK_DIR}/*.json, logs: ${WORK_DIR}/*.err"

if [[ "${failed}" -ne 0 ]]; then
	log_error "at least one round failed or reported wrong events; the counters above are incomplete"
	exit 1
fi

log_info "benchmark finished; compare the modes yourself, this script sets no target"
