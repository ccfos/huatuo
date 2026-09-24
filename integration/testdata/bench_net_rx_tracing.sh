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
# The rate and the CPU percentage are divided by the generator's own traffic
# window, not by the wall clock span of the round: starting the interpreter can
# take seconds on a loaded machine, and a denominator that includes them reports
# the load, not the hook. The CPU counters are read by that same process at the
# two ends of the window it timed, so the numerator and the denominator cover
# one interval instead of two. The raw CPU seconds are reported next to the
# percentage, because they are the column a noisy host disturbs least.
#
# The fixture is stopped after the generator returns, so its collection window
# always covers the whole measured interval; its own timeout is a fault bound
# only. A fixture that reached that bound, or exited before the traffic ended,
# leaves the tail of the traffic unmeasured, and that round is invalid.
#
# The generator sends TCP SYNs to a closed port of the peer namespace, which is
# what keeps the packet rate high enough to see a per-packet hook: the slow TCP
# server fixture serializes on purpose and cannot drive one. Every SYN enters
# tcp_v4_rcv, and the peer's RST re-enters it on the other side, so the offered
# rate is the control and the CPU time is the signal. A generator-limited rate
# means a more expensive hook shows up as CPU, not as fewer packets; that is why
# both columns are reported.
#
# The generator runs inside the client namespace, where the peer address is
# reachable. From the host namespace the target sits behind the default route
# and the connects time out, so the counters would climb while no packet ever
# reached the hook.
#
# A round only counts when the traffic provably arrived: the peer's veth must
# have received packets and the fixture must have reported an RX_STAGE_TCPV4
# event for the measured address pair. Anything less and the round is reported
# as invalid instead of being averaged into the comparison.
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
# The fixture's own timeout is only there to bound a fault: the round stops it
# after the traffic, long before this bound, and a fixture that reached it
# covered a window that ended inside the traffic.
readonly FIXTURE_FAULT_SECONDS=$((ROUND_SECONDS + 60))

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


def cpu_counters():
    # Busy excludes idle and iowait; the hook runs in softirq context, so that
    # column moves first when the entry point gets more expensive. /proc/stat is
    # the host's, also from inside a network namespace.
    with open('/proc/stat') as stat:
        fields = stat.readline().split()
    return (int(fields[1]) + int(fields[2]) + int(fields[3])
            + int(fields[6]) + int(fields[7]) + int(fields[8]), int(fields[7]))


target = (sys.argv[1], int(sys.argv[2]))
duration = float(sys.argv[3])

sent = 0
start = time.monotonic()
# The counters are read inside the timed window, and the duration is read after
# them, so the counter interval sits strictly inside the interval the duration
# covers and the residue of the two /proc/stat reads can only shrink the
# numerator. The opposite order would let the same residue inflate a
# percentage.
busy_before, softirq_before = cpu_counters()
end = start + duration
while time.monotonic() < end:
    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    sock.setblocking(False)
    try:
        sock.connect(target)
    except OSError:
        pass
    sock.close()
    sent += 1

busy_after, softirq_after = cpu_counters()

# The window this process measured, not the caller's: a rate or a CPU
# percentage can never divide a wider numerator by a narrower denominator
# because the interpreter started, or because entering the namespace took time.
elapsed = time.monotonic() - start
print(sent, elapsed, busy_before, softirq_before, busy_after, softirq_after)
PY
}

# ------------------------------- measurement --------------------------------

# veth_peer_rx_packets counts what the generator's peer received, which is the
# only traffic the hook can see: without a packet there, the round measures the
# generator instead of the entry point.
veth_peer_rx_packets() {
	ip netns exec "${TCP_NS_SERVER}" \
		cat "/sys/class/net/${TCP_NS_VETH_SERVER}/statistics/rx_packets"
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
	local pair_tcpv4="-"
	local timeout_hit="-"
	local rx_before rx_after received
	local busy0 softirq0 busy1 softirq1 syns elapsed
	local status=0
	local invalid=0
	local early_exit=0

	if [[ "${mode}" != "${MODE_BASELINE}" ]]; then
		# The timeout is a fault bound, not the collection window: the round
		# stops the fixture after the traffic instead, so the window always
		# covers what is measured.
		"${FIXTURE_BIN}" \
			-bpf-dir "${ROOT_DIR}/_output/bpf" \
			-mode "${mode}" \
			-timeout "${FIXTURE_FAULT_SECONDS}s" \
			-events "${FIXTURE_EVENTS}" \
			> "${out}" 2> "${err}" &
		FIXTURE_PID=$!
		wait_for_attach "${out}" "${FIXTURE_PID}" "${mode}"
	fi

	rx_before=$(veth_peer_rx_packets)

	# The generator belongs in the client namespace: the peer address is only
	# routable from there. It reports its own window, so the traffic, the
	# duration and the two CPU counter readings cover one interval.
	read -r syns elapsed busy0 softirq0 busy1 softirq1 <<< "$(ip netns exec "${TCP_NS_CLIENT}" \
		python3 "${GENERATOR}" "${TCP_NS_SERVER_ADDR}" "${BENCH_PORT}" "${ROUND_SECONDS}")"

	rx_after=$(veth_peer_rx_packets)
	received=$((rx_after - rx_before))

	if [[ "${mode}" != "${MODE_BASELINE}" ]]; then
		# The traffic is over, so the fixture is stopped now: what it collected
		# up to this point covered the measured window. A fixture that is
		# already gone stopped on its own, which leaves the tail of the traffic
		# unmeasured whichever way it ended.
		if kill -0 "${FIXTURE_PID}" 2> /dev/null; then
			stop_and_wait_by_pid "${FIXTURE_PID}" 10 || status=$?
		else
			early_exit=1
			wait "${FIXTURE_PID}" || status=$?
		fi
		FIXTURE_PID=""
	fi

	# A window of zero cannot produce a rate or a percentage.
	[[ "${elapsed}" != "0.000" ]] || fatal "round ${label} measured no elapsed time"

	if [[ "${mode}" != "${MODE_BASELINE}" ]]; then
		entry_point=$(jq -r 'select(.ready == true) | .entry_point' "${out}" | tail -1)
		events=$(jq -r 'select(.summary == true) | .events' "${out}")
		duplicates=$(jq -r 'select(.summary == true) | .duplicates' "${out}")
		lost=$(jq -r 'select(.summary == true) | .lost_samples' "${out}")
		timeout_hit=$(jq -r 'select(.summary == true) | .timeout' "${out}" | tail -1)
		pair_events=$(jq -s \
			--arg saddr "${TCP_NS_CLIENT_ADDR}" --arg daddr "${TCP_NS_SERVER_ADDR}" \
			'[.[] | select(.summary != true) | select(.ready != true)
				| select((.saddr == $saddr and .daddr == $daddr) or (.saddr == $daddr and .daddr == $saddr))]
			 | length' "${out}")
		# The stage matters: the TCPV4 event is the one the migrated hook
		# produces, so only it proves the measured entry point saw the traffic.
		pair_tcpv4=$(jq -s \
			--arg saddr "${TCP_NS_CLIENT_ADDR}" --arg daddr "${TCP_NS_SERVER_ADDR}" \
			'[.[] | select(.summary != true) | select(.ready != true) | select(.stage == "RX_STAGE_TCPV4")
				| select((.saddr == $saddr and .daddr == $daddr) or (.saddr == $daddr and .daddr == $saddr))]
			 | length' "${out}")
	fi

	local row
	row=$(awk -F'\t' -v OFS='\t' \
		-v label="${label}" -v mode="${mode}" -v elapsed="${elapsed}" \
		-v syns="${syns}" -v busy0="${busy0}" -v busy1="${busy1}" \
		-v softirq0="${softirq0}" -v softirq1="${softirq1}" \
		-v events="${events}" -v pair_events="${pair_events}" \
		-v pair_tcpv4="${pair_tcpv4}" -v received="${received}" \
		-v duplicates="${duplicates}" -v lost="${lost}" \
		-v entry_point="${entry_point}" -v clock="${clock_ticks}" \
		'BEGIN {
			cpu = (busy1 - busy0) / clock
			soft = (softirq1 - softirq0) / clock
			printf "%s\t%s\t%s\t%s\t%.0f\t%s\t%.3f\t%.2f\t%.3f\t%.2f\t%s\t%s\t%s\t%s\t%s\t%s",
				label, mode, elapsed, syns, syns / elapsed, received,
				cpu, 100 * cpu / elapsed, soft, 100 * soft / elapsed,
				events, pair_events, pair_tcpv4, duplicates, lost, entry_point
		}')

	printf '%s\n' "${row}" >> "${RESULTS}"
	log_info "round ${label}: mode=${mode} entry_point=${entry_point} syns=${syns} syns/s=$(cut -f5 <<< "${row}") received=$(cut -f6 <<< "${row}") cpu_percent=$(cut -f8 <<< "${row}") softirq_percent=$(cut -f10 <<< "${row}") events=${events} pair_tcpv4=${pair_tcpv4} duplicates=${duplicates} lost=${lost}"

	# Whatever the mode, the traffic has to have crossed the pair: a round where
	# it did not measures the generator, not the hook.
	if [[ "${received}" -le 0 ]]; then
		log_warn "round ${label} (${mode}): the peer received no packet, the generator never reached it"
		invalid=1
	fi

	# The baseline round attaches nothing, so it has no event counters to
	# judge; its only job is the traffic and CPU reference.
	if [[ "${mode}" == "${MODE_BASELINE}" ]]; then
		return "${invalid}"
	fi

	if [[ "${status}" -ne 0 ]]; then
		log_warn "round ${label} fixture exited with ${status}"
		invalid=1
	fi
	if [[ "${early_exit}" -ne 0 ]]; then
		log_warn "round ${label} (${mode}): the fixture exited before the traffic ended, its window does not cover the round"
		invalid=1
	fi
	# The fixture reports whether its own deadline ended the collection. A
	# fixture this round stopped reports false; anything else means its window
	# ended inside the traffic, so the tail was not measured.
	if [[ "${timeout_hit}" != "false" ]]; then
		log_warn "round ${label} (${mode}): the fixture did not cover the whole window (timeout=${timeout_hit:-missing})"
		invalid=1
	fi
	if [[ "${events}" == "0" ]]; then
		log_warn "round ${label} reported no event; the probe was attached but saw no packet"
		invalid=1
	fi
	if [[ "${pair_tcpv4}" == "0" ]]; then
		log_warn "round ${label} reported no RX_STAGE_TCPV4 event for ${TCP_NS_CLIENT_ADDR} <-> ${TCP_NS_SERVER_ADDR}; this round cannot be compared"
		invalid=1
	fi

	# Repeated packets are a diagnostic: two connections can reuse a client port
	# and an initial sequence number, and the object rate-limits its events.
	# The entry point count of the loaded object is what rules out a double
	# attach, not this counter.
	if [[ "${duplicates}" != "0" ]]; then
		log_warn "round ${label} repeated ${duplicates} packet keys; check the raw events, this counter cannot tell a retransmit from a double attach"
	fi
	if [[ "${lost}" != "0" ]]; then
		log_warn "round ${label} lost ${lost} samples; do not compare this round's CPU against a round that lost none"
	fi

	return "${invalid}"
}

# summarize prints mean/min/max per mode from the measured rounds. Warm-up
# rounds stay in the results file as raw data but are not averaged: they are
# there to reach a steady state, not to be reported.
summarize() {
	awk -F'\t' '
		NR == 1 { next }
		$1 ~ /^warmup/ { next }
		{
			m = $2
			if (!(m in seen)) { seen[m] = 1; order[++n] = m }
			rounds[m]++

			rate[m] += $5
			if (!(m in rmin) || $5 < rmin[m]) rmin[m] = $5
			if ($5 > rmax[m]) rmax[m] = $5

			cpusec[m] += $7

			cpu[m] += $8
			if (!(m in cmin) || $8 < cmin[m]) cmin[m] = $8
			if ($8 > cmax[m]) cmax[m] = $8

			soft[m] += $10
			if (!(m in smin) || $10 < smin[m]) smin[m] = $10
			if ($10 > smax[m]) smax[m] = $10

			events[m] += $11
			pair[m] += $12
			pairtcpv4[m] += $13
			duplicates[m] += $14
			lost[m] += $15
		}
		END {
			printf "%-9s %6s %18s %9s %21s %21s %10s %10s %9s %7s %7s\n",
				"mode", "rounds", "syns/s mean[min-max]", "cpu_s mean", "cpu% mean[min-max]",
				"softirq% mean[min-max]", "events", "pair_events", "pair_tcpv4", "dupes", "lost"
			for (i = 1; i <= n; i++) {
				m = order[i]
				printf "%-9s %6d %8.0f[%.0f-%.0f] %9.2f %8.2f[%.2f-%.2f] %8.2f[%.2f-%.2f] %10d %10d %9d %7d %7d\n",
					m, rounds[m],
					rate[m] / rounds[m], rmin[m], rmax[m],
					cpusec[m] / rounds[m],
					cpu[m] / rounds[m], cmin[m], cmax[m],
					soft[m] / rounds[m], smin[m], smax[m],
					events[m], pair[m], pairtcpv4[m], duplicates[m], lost[m]
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

printf 'label\tmode\telapsed_s\tsyns\tsyns_per_sec\treceived\tcpu_s\tcpu_percent\tsoftirq_s\tsoftirq_percent\tevents\tpair_events\tpair_tcpv4\tevents_duplicates\tlost_samples\tentry_point\n' \
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
	log_error "at least one round saw no packet or no TCPV4 event for the measured pair;"
	log_error "its counters are in the table above but the comparison is not usable"
	exit 1
fi

log_info "benchmark finished; compare the modes yourself, this script sets no target"
