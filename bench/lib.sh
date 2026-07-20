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

# Shared helpers for the HUATUO performance-overhead benchmark. Sourced by
# bench/run.sh and bench/scenario_*.sh; never executed directly.

set -euo pipefail

TEST_LOG_TAG=${TEST_LOG_TAG:-"BENCH"}

# Progress logs go to stderr so a scenario's stdout is pure JSON (captured by
# run.sh). The human-readable summary is printed by run.sh after assembly.
log_info() { echo "[${TEST_LOG_TAG}] $*" >&2; }
log_warn() { echo "[${TEST_LOG_TAG}][WARN] $*" >&2; }
log_error() { echo "[${TEST_LOG_TAG}][ERROR] $*" >&2; }
bench_fatal() {
	echo "[${TEST_LOG_TAG}][FAIL] $*" >&2
	exit 1
}

# --------------------------- cleanup registration ----------------------------
#
# Scenarios register their scratch dirs; run.sh installs bench_cleanup as an
# EXIT trap so huatuo-bamai and temp dirs never leak, even on error/abort.
BENCH_CLEANUP_DIRS=()
bench_register_cleanup() { BENCH_CLEANUP_DIRS+=("$1"); }
bench_cleanup() {
	huatuo_stop
	local d
	for d in ${BENCH_CLEANUP_DIRS[@]+"${BENCH_CLEANUP_DIRS[@]}"}; do
		rm -rf "${d}" 2> /dev/null || true
	done
}

# --------------------------------- utils -------------------------------------

now_ms() { date +%s%3N; }

# is_container: 0 when running inside a container (overlay/btrfs root or
# systemd-detect-virt -c reports a container). Mirrors integration/lib.sh.
is_container() {
	local fstype
	fstype=$(findmnt -n -o FSTYPE / 2> /dev/null || true)
	case "${fstype}" in
	overlay | btrfs) return 0 ;;
	esac
	if command -v systemd-detect-virt > /dev/null 2>&1; then
		[[ "$(systemd-detect-virt -c 2> /dev/null)" != "none" ]] && return 0
	fi
	return 1
}

# json_str <raw>: emit a JSON string literal with quoting. Reads stdin or $1.
json_str() {
	local s="${1:-$(cat)}"
	s="${s//\\/\\\\}"
	s="${s//\"/\\\"}"
	s="${s//$'\n'/\\n}"
	s="${s//$'\t'/\\t}"
	printf '"%s"' "$s"
}

# mean_of <file>: print the arithmetic mean of one-number-per-line, or "null".
mean_of() {
	awk '{ s += $1; n++ } END { if (n > 0) printf "%.6f", s / n; else print "null" }' "$1"
}

# stats_json <file>: print a JSON object describing the distribution of the
# one-number-per-line input. Empty/missing file -> all-null object.
stats_json() {
	local file=$1
	if [[ ! -s "${file}" ]]; then
		echo '{"count":0,"min":null,"mean":null,"median":null,"p95":null,"max":null,"stddev":null,"samples":[]}'
		return 0
	fi
	local samples
	samples=$(paste -sd, "${file}")
	sort -n "${file}" | awk -v samples="${samples}" '
		{ a[NR] = $1; sum += $1 }
		END {
			n = NR
			mean = sum / n
			for (i = 1; i <= n; i++) { d = a[i] - mean; ss += d * d }
			sd = (n > 1) ? sqrt(ss / (n - 1)) : 0
			if (n % 2 == 1) { med = a[int(n / 2) + 1] } else { med = (a[n / 2] + a[n / 2 + 1]) / 2 }
			idx = int(0.95 * n + 0.999999)
			if (idx < 1) idx = 1
			if (idx > n) idx = n
			p95 = a[idx]
			printf "{\"count\":%d,\"min\":%.4f,\"mean\":%.4f,\"median\":%.4f,\"p95\":%.4f,\"max\":%.4f,\"stddev\":%.4f,\"samples\":[%s]}\n", \
				n, a[1], mean, med, p95, a[n], sd, samples
		}'
}

# profile_result_json <baseline_file> <observed_file> [extra-kv...]:
# print a JSON object comparing an A (baseline) and B (observed) sample set.
# Extra-kv arguments are raw "key":value JSON fragments appended to the object.
profile_result_json() {
	local baseline_file=$1 observed_file=$2
	shift 2
	local bstats ostats bmean omean delta dpct extra=""
	bstats=$(stats_json "${baseline_file}")
	ostats=$(stats_json "${observed_file}")
	bmean=$(mean_of "${baseline_file}")
	omean=$(mean_of "${observed_file}")
	delta=$(awk -v b="${bmean}" -v o="${omean}" \
		'BEGIN { if (b == "null" || o == "null") print "null"; else printf "%.6f", o - b }')
	dpct=$(awk -v b="${bmean}" -v o="${omean}" \
		'BEGIN { if (b == "null" || o == "null" || b + 0 == 0) print "null"; else printf "%.4f", (o - b) / b * 100 }')
	local kv
	for kv in "$@"; do
		extra+=",${kv}"
	done
	printf '{"baseline":%s,"observed":%s,"delta_mean":%s,"delta_percent":%s%s}\n' \
		"${bstats}" "${ostats}" "${delta}" "${dpct}" "${extra}"
}

# result_obj <status> <json-body...>: wrap a scenario body with status field.
result_obj() {
	local status=$1
	shift
	printf '{"status":%s,%s}\n' "$(json_str "${status}")" "$*"
}

scenario_skipped() { result_obj skipped "\"reason\":$(json_str "$1")"; }
scenario_error() { result_obj error "\"reason\":$(json_str "$1")"; }

# --------------------------- huatuo module model -----------------------------

# bench_blacklist_except <module>: print blacklist items = core + every optional
# module except the named one (single-module profile).
bench_blacklist_except() {
	local keep=$1 m
	for m in "${HUATUO_CORE_BLACKLIST[@]}"; do
		printf '%s\n' "${m}"
	done
	for m in "${HUATUO_OPTIONAL_MODULES[@]}"; do
		[[ "${m}" != "${keep}" ]] && printf '%s\n' "${m}"
	done
}

# bench_blacklist_all: print every core+optional module (minimal profile).
bench_blacklist_all() {
	local m
	for m in "${HUATUO_CORE_BLACKLIST[@]}" "${HUATUO_OPTIONAL_MODULES[@]}"; do
		printf '%s\n' "${m}"
	done
}

# blacklist_to_toml_array <items-file>: print a TOML array literal.
blacklist_to_toml_array() {
	local items=$1 first=1 m
	printf '['
	while read -r m; do
		[[ -z "${m}" ]] && continue
		if [[ ${first} -eq 1 ]]; then first=0; else printf ', '; fi
		printf '"%s"' "${m}"
	done < "${items}"
	printf ']'
}

# gen_bench_conf <out-path> <blacklist-items-file> [extra-body-file]:
# write a standalone bamai.conf. Storage/kubelet are disabled via CLI flags at
# start time, so the file only needs BlackList (+ optional event subsections).
gen_bench_conf() {
	local out=$1 items=$2 extra=${3:-/dev/null}
	local arr
	arr=$(blacklist_to_toml_array "${items}")
	{
		printf 'BlackList = %s\n\n' "${arr}"
		printf '[Log]\n    Level = "Warn"\n'
		if [[ -s "${extra}" ]]; then
			printf '\n'
			cat "${extra}"
		fi
	} > "${out}"
}

# full_config_path: the realistic multi-collector config shipped with the repo.
full_config_path() { printf '%s\n' "${ROOT_DIR}/huatuo-bamai.conf"; }

# ----------------------------- huatuo lifecycle ------------------------------

BENCH_HUATUO_PID=""
BENCH_HUATUO_RUN_DIR=""
BENCH_HUATUO_READY_TIMEOUT=${BENCH_HUATUO_READY_TIMEOUT:-60}
BENCH_HUATUO_ADDR=${BENCH_HUATUO_ADDR:-"http://127.0.0.1:19704"}

# huatuo_bamai_ready: 0 once /metrics responds.
huatuo_bamai_ready() {
	[[ -n "${BENCH_HUATUO_PID}" ]] || return 1
	kill -0 "${BENCH_HUATUO_PID}" 2> /dev/null || return 1
	curl -sf --connect-timeout 1 --max-time 2 "${BENCH_HUATUO_ADDR}/metrics" > /dev/null
}

# huatuo_start <conf-path>: launch huatuo-bamai with a given config; returns
# non-zero if it exits or never becomes ready within BENCH_HUATUO_READY_TIMEOUT.
huatuo_start() {
	local conf_path=$1
	local conf_dir conf_name
	conf_dir=$(dirname "${conf_path}")
	conf_name=$(basename "${conf_path}")

	[[ -x "${HUATUO_BAMAI_BIN}" ]] || {
		log_error "binary missing: ${HUATUO_BAMAI_BIN}"
		return 1
	}
	[[ -r "${conf_path}" ]] || {
		log_error "config missing: ${conf_path}"
		return 1
	}

	BENCH_HUATUO_RUN_DIR=$(mktemp -d /tmp/huatuo-bench.XXXXXX)
	log_info "starting huatuo-bamai (conf=${conf_path})"
	"${HUATUO_BAMAI_BIN}" \
		--config-dir "${conf_dir}" \
		--config "${conf_name}" \
		--region bench \
		--disable-storage \
		--disable-kubelet \
		--disable-cgroup \
		> "${BENCH_HUATUO_RUN_DIR}/huatuo.log" 2>&1 &
	BENCH_HUATUO_PID=$!
	echo "${BENCH_HUATUO_PID}" > "${BENCH_HUATUO_RUN_DIR}/huatuo.pid"

	local end=$(($(date +%s) + BENCH_HUATUO_READY_TIMEOUT))
	while [[ $(date +%s) -lt ${end} ]]; do
		if ! kill -0 "${BENCH_HUATUO_PID}" 2> /dev/null; then
			log_error "huatuo-bamai exited during startup; tail of log:"
			tail -20 "${BENCH_HUATUO_RUN_DIR}/huatuo.log" >&2 || true
			return 1
		fi
		if huatuo_bamai_ready; then
			log_info "huatuo-bamai ready (pid=${BENCH_HUATUO_PID})"
			return 0
		fi
		sleep 1
	done
	log_error "huatuo-bamai not ready within ${BENCH_HUATUO_READY_TIMEOUT}s; tail of log:"
	tail -20 "${BENCH_HUATUO_RUN_DIR}/huatuo.log" >&2 || true
	return 1
}

# huatuo_stop: SIGTERM with graceful polling, then SIGKILL; release the port.
huatuo_stop() {
	local pid=${BENCH_HUATUO_PID}
	if [[ -n "${pid}" ]] && kill -0 "${pid}" 2> /dev/null; then
		kill -TERM "${pid}" 2> /dev/null || true
		local waited=0
		while kill -0 "${pid}" 2> /dev/null && [[ ${waited} -lt 15 ]]; do
			sleep 1
			waited=$((waited + 1))
		done
		kill -KILL "${pid}" 2> /dev/null || true
		# wait for the port to be released so the next start can bind.
		local waited2=0
		while [[ ${waited2} -lt 10 ]]; do
			! curl -sf --connect-timeout 1 --max-time 1 "${BENCH_HUATUO_ADDR}/metrics" > /dev/null 2>&1 && break
			sleep 1
			waited2=$((waited2 + 1))
		done
	fi
	BENCH_HUATUO_PID=""
	rm -rf "${BENCH_HUATUO_RUN_DIR}"
	BENCH_HUATUO_RUN_DIR=""
}

# huatuo_rss_kib / huatuo_hwm_kib: resident / peak resident memory of the pid.
huatuo_rss_kib() { awk '/^VmRSS:/ { print $2 }' "/proc/${BENCH_HUATUO_PID}/status" 2> /dev/null || echo 0; }
huatuo_hwm_kib() { awk '/^VmHWM:/ { print $2 }' "/proc/${BENCH_HUATUO_PID}/status" 2> /dev/null || echo 0; }

# huatuo_cpu_seconds: user+system CPU seconds consumed by the pid so far.
huatuo_cpu_seconds() {
	[[ -n "${BENCH_HUATUO_PID}" ]] || {
		echo 0
		return
	}
	local hz rss
	hz=$(getconf CLK_TCK 2> /dev/null || echo 100)
	rss=$(awk '{ print $14 + $15 }' "/proc/${BENCH_HUATUO_PID}/stat" 2> /dev/null || echo 0)
	awk -v j="${rss}" -v hz="${hz}" 'BEGIN { printf "%.4f", j / hz }'
}

# ------------------------------- workloads ----------------------------------
#
# Each workload returns a single numeric sample on stdout (its cost under the
# current huatuo state). They are dependency-free (dd, ping, /proc) so the
# benchmark behaves identically across distros and in CI.

# cpu_sample: wall-clock seconds to copy BENCH_CPU_BYTES through dd.
cpu_sample() {
	local start end
	start=$(date +%s.%N)
	dd if=/dev/zero of=/dev/null bs=64K count=$((BENCH_CPU_BYTES / 65536)) status=none
	end=$(date +%s.%N)
	awk -v s="${start}" -v e="${end}" 'BEGIN { printf "%.6f", e - s }'
}

# net_sample: mean RTT in microseconds over BENCH_NET_PACKETS loopback pings.
# Parses the iputils-ping "rtt min/avg/max/mdev = a/b/c/d ms" summary (and the
# "round-trip" variant used by busybox), reporting the average in microseconds.
net_sample() {
	local tmp
	tmp=$(mktemp)
	# -i 0 (no inter-packet delay) needs root, which the benchmark requires.
	if ! ping -c "${BENCH_NET_PACKETS}" -i 0 127.0.0.1 > "${tmp}" 2>&1; then
		ping -c "${BENCH_NET_PACKETS}" 127.0.0.1 > "${tmp}" 2>&1
	fi
	awk -F'=' '
		/rtt min\/|round-trip min\// {
			v = $2
			gsub(/[[:space:]]|ms/, "", v)
			split(v, a, "/")
			printf "%.6f\n", (a[2] + 0) * 1000
		}' "${tmp}"
	rm -f "${tmp}"
}

# io_sample: mean milliseconds per synchronized write over BENCH_IO_OPS ops.
# Writes into BENCH_IO_DIR (a real filesystem set by the io scenario) when set,
# else a throwaway tmpdir.
io_sample() {
	local tmpdir tmp count block
	if [[ -n "${BENCH_IO_DIR:-}" && -d "${BENCH_IO_DIR}" ]]; then
		tmpdir="${BENCH_IO_DIR}"
	else
		tmpdir=$(mktemp -d /tmp/huatuo-bench-io.XXXXXX)
	fi
	tmp="${tmpdir}/io"
	count=${BENCH_IO_OPS}
	block=${BENCH_IO_BLOCK_KIB}
	local start end
	start=$(date +%s.%N)
	local i
	for ((i = 0; i < count; i++)); do
		dd if=/dev/zero of="${tmp}" bs=${block}K count=1 oflag=dsync status=none
	done
	end=$(date +%s.%N)
	[[ -z "${BENCH_IO_DIR:-}" ]] && rm -rf "${tmpdir}"
	awk -v s="${start}" -v e="${end}" -v n="${count}" 'BEGIN { printf "%.6f", (e - s) / n * 1000 }'
}

# sustained_load <duration-seconds>: keep the system busy (CPU + IO) so the
# memory scenario samples huatuo under realistic steady-state collection.
sustained_load() {
	local duration=$1
	local end=$(($(date +%s) + duration))
	local tmpdir
	tmpdir=$(mktemp -d /tmp/huatuo-bench-load.XXXXXX)
	(
		while [[ $(date +%s) -lt ${end} ]]; do
			dd if=/dev/zero of=/dev/null bs=64K count=1024 status=none
		done
	) &
	local cpu_pid=$!
	(
		while [[ $(date +%s) -lt ${end} ]]; do
			dd if=/dev/zero of="${tmpdir}/f" bs=4K count=64 oflag=dsync status=none
		done
	) &
	local io_pid=$!
	# Wait until the window elapses, then reap the workers.
	local waited=0
	while [[ $(date +%s) -lt ${end} ]] && [[ ${waited} -lt ${duration} ]]; do
		sleep 1
		waited=$((waited + 1))
	done
	kill "${cpu_pid}" "${io_pid}" 2> /dev/null || true
	wait "${cpu_pid}" 2> /dev/null || true
	wait "${io_pid}" 2> /dev/null || true
	rm -rf "${tmpdir}"
}

# ------------------------------ sampling loop --------------------------------

# sample_workload <workload_fn> <out_file>: run the workload
# (BENCH_ITERATIONS + BENCH_WARMUP) times; write the post-warmup samples (one
# per line) to out_file. Returns the number of recorded samples via stdout? no
# — out_file is the contract. Always returns 0.
sample_workload() {
	local fn=$1 out=$2 i total
	total=$((BENCH_ITERATIONS + BENCH_WARMUP))
	: > "${out}"
	for ((i = 1; i <= total; i++)); do
		local v
		v=$("${fn}")
		[[ ${i} -gt ${BENCH_WARMUP} ]] && printf '%s\n' "${v}" >> "${out}"
	done
}

# observed_samples <conf_path> <workload_fn> <out_file>:
# start huatuo with conf, sample the workload, stop huatuo. Returns 1 if huatuo
# failed to start; otherwise the exit status of sample_workload.
observed_samples() {
	local conf=$1 fn=$2 out=$3
	if ! huatuo_start "${conf}"; then
		huatuo_stop
		return 1
	fi
	sample_workload "${fn}" "${out}"
	local rc=$?
	huatuo_stop
	return ${rc}
}

# sample_rss_window <duration-seconds> <out_file>: poll huatuo-bamai's VmRSS
# (KiB) once per second for the given duration, writing one value per line.
sample_rss_window() {
	local duration=$1 out=$2 i
	: > "${out}"
	for ((i = 0; i < duration; i++)); do
		huatuo_rss_kib >> "${out}"
		sleep 1
	done
}

# rss_summary_json <rss-file>: print a JSON object with peak/mean RSS and the
# kernel-reported high-water mark (MiB), computed against the running pid.
rss_summary_json() {
	local file=$1
	local peak mean hwm
	peak=$(sort -n "${file}" | tail -1)
	[[ -z "${peak}" ]] && peak=0
	mean=$(mean_of "${file}")
	hwm=$(huatuo_hwm_kib)
	awk -v peak="${peak}" -v mean="${mean}" -v hwm="${hwm}" 'BEGIN {
		if (mean == "null") mean = 0
		printf "{\"rss_peak_mib\":%.4f,\"rss_mean_mib\":%.4f,\"hwm_mib\":%.4f}\n", peak / 1024, mean / 1024, hwm / 1024
	}'
}

# -------------------------------- metadata ----------------------------------

collect_metadata() {
	local kernel arch ncpu mem_total env huatuo_ver
	kernel=$(uname -r 2> /dev/null || echo "unknown")
	arch=$(uname -m 2> /dev/null || echo "unknown")
	ncpu=$(nproc 2> /dev/null || echo 0)
	mem_total=$(awk '/^MemTotal:/ { print $2 }' /proc/meminfo 2> /dev/null || echo 0)
	if is_container; then env="container"; else env="host"; fi
	huatuo_ver=$("${HUATUO_BAMAI_BIN}" --version 2> /dev/null | head -1 | tr -d '\n' || echo "unknown")

	cat << EOF
"kernel": $(json_str "${kernel}"),
"arch": $(json_str "${arch}"),
"ncpu": ${ncpu},
"mem_total_kb": ${mem_total},
"env": $(json_str "${env}"),
"huatuo_version": $(json_str "${huatuo_ver}"),
"iterations": ${BENCH_ITERATIONS},
"timestamp_utc": $(json_str "$(date -u +%Y-%m-%dT%H:%M:%SZ)")
EOF
}
