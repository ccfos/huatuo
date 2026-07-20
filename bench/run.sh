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

# HUATUO performance-overhead benchmark orchestrator.
#
# Runs every scenario under bench/scenario_*.sh, assembles a single JSON report
# into ${BENCH_RESULTS_DIR}, prints a human-readable summary to stdout, and
# (when BENCH_FAIL_ON_REGRESSION=1) exits non-zero on a threshold breach.
#
# Usage:
#   make bench                       # build huatuo-bamai, then run this
#   bash bench/run.sh                # run against an existing _output build
#   BENCH_ITERATIONS=10 bash bench/run.sh

set -euo pipefail

BENCH_ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export ROOT_DIR="${ROOT_DIR:-$(cd "${BENCH_ROOT_DIR}/.." && pwd)}"

# shellcheck source=env.sh
source "${BENCH_ROOT_DIR}/env.sh"
# shellcheck source=lib.sh
source "${BENCH_ROOT_DIR}/lib.sh"

# Source every scenario so the scenario_* functions exist before we install the
# EXIT trap (scenarios rely on bench_cleanup registered here).
for s in "${BENCH_ROOT_DIR}"/scenario_*.sh; do
	# shellcheck source=/dev/null
	source "${s}"
done

trap bench_cleanup EXIT

mkdir -p "${BENCH_RESULTS_DIR}"
readonly TS="$(date -u +%Y%m%dT%H%M%SZ)"
readonly OUT_JSON="${BENCH_RESULTS_DIR}/bench-${TS}.json"
readonly OUT_SUMMARY="${BENCH_RESULTS_DIR}/bench-${TS}.summary.txt"

log_info "HUATUO performance-overhead benchmark"
log_info "binary : ${HUATUO_BAMAI_BIN}"
log_info "results: ${BENCH_RESULTS_DIR}"
log_info "iters  : ${BENCH_ITERATIONS} (warmup ${BENCH_WARMUP})"

# Quick precondition: the binary must exist. If not, emit a minimal but valid
# report marking every scenario skipped, so CI never sees malformed output.
if [[ ! -x "${HUATUO_BAMAI_BIN}" ]]; then
	log_warn "huatuo-bamai binary not found; writing a skipped-only report"
	skip_reason="huatuo-bamai binary missing: ${HUATUO_BAMAI_BIN}"
	cpu_json=$(scenario_skipped "${skip_reason}")
	mem_json=$(scenario_skipped "${skip_reason}")
	net_json=$(scenario_skipped "${skip_reason}")
	io_json=$(scenario_skipped "${skip_reason}")
else
	log_info "running scenarios (this takes several minutes)..."
	cpu_json=$(scenario_cpu)
	mem_json=$(scenario_memory)
	net_json=$(scenario_net)
	io_json=$(scenario_io)
fi

# ---- assemble JSON report ---------------------------------------------------
{
	printf '{\n'
	printf '  "version": 1,\n'
	printf '  "metadata": { %s },\n' "$(collect_metadata)"
	printf '  "scenarios": {\n'
	printf '    "cpu_overhead": %s,\n' "${cpu_json}"
	printf '    "memory_overhead": %s,\n' "${mem_json}"
	printf '    "net_latency": %s,\n' "${net_json}"
	printf '    "io_latency": %s\n' "${io_json}"
	printf '  }\n'
	printf '}\n'
} > "${OUT_JSON}"

# Validate + pretty-print if python3 is available; otherwise keep compact form.
if command -v python3 > /dev/null 2>&1; then
	if python3 -m json.tool "${OUT_JSON}" > "${OUT_JSON}.tmp" 2> /dev/null; then
		mv "${OUT_JSON}.tmp" "${OUT_JSON}"
	else
		rm -f "${OUT_JSON}.tmp"
		log_warn "produced JSON failed validation; see ${OUT_JSON}"
	fi
fi

log_info "report written: ${OUT_JSON}"

# ---- human-readable summary -------------------------------------------------
print_summary() {
	if command -v python3 > /dev/null 2>&1; then
		python3 - "${OUT_JSON}" << 'PY'
import json, sys
with open(sys.argv[1]) as f:
    data = json.load(f)
md = data.get("metadata", {})
print("=" * 72)
print("HUATUO performance-overhead benchmark")
print("=" * 72)
print("kernel    : %s" % md.get("kernel"))
print("arch      : %s  ncpu=%s  mem=%s KB  env=%s" % (
    md.get("arch"), md.get("ncpu"), md.get("mem_total_kb"), md.get("env")))
print("huatuo    : %s" % md.get("huatuo_version"))
print("iterations: %s" % md.get("iterations"))
print("-" * 72)


def pct(v):
    return "n/a" if v is None else "%.2f%%" % v


def delta(v):
    return "n/a" if v is None else "%.4f" % v


def show(name, key):
    sc = data["scenarios"].get(key, {})
    status = sc.get("status")
    unit = sc.get("unit", "")
    print("%-16s status=%-8s unit=%s" % (name, status, unit))
    if status != "ok":
        print("    reason: %s" % sc.get("reason", ""))
        return
    for pname, prof in sc.get("profiles", {}).items():
        if "delta_percent" in prof:
            b = prof.get("baseline", {})
            o = prof.get("observed", {})
            print("    %-7s baseline_mean=%s observed_mean=%s delta=%s (%s)"
                  % (pname,
                     ("%.4f" % b["mean"]) if b.get("mean") is not None else "n/a",
                     ("%.4f" % o["mean"]) if o.get("mean") is not None else "n/a",
                     delta(prof.get("delta_mean")),
                     pct(prof.get("delta_percent"))))
        else:
            parts = ", ".join("%s=%s" % (k, v) for k, v in prof.items())
            print("    %-7s %s" % (pname, parts))


show("CPU overhead", "cpu_overhead")
show("Memory", "memory_overhead")
show("Net latency", "net_latency")
show("IO latency", "io_latency")
print("=" * 72)
PY
		return 0
	fi
	log_warn "python3 missing; showing raw JSON path only"
	echo "See ${OUT_JSON}"
	return 0
}

print_summary | tee "${OUT_SUMMARY}"

# ---- optional regression gate ----------------------------------------------
check_regression() {
	[[ "${BENCH_FAIL_ON_REGRESSION}" == "1" ]] || return 0
	command -v python3 > /dev/null 2>&1 || {
		log_warn "BENCH_FAIL_ON_REGRESSION=1 but python3 missing; skipping gate"
		return 0
	}
	python3 - "${OUT_JSON}" "${BENCH_THRESHOLD_CPU_PCT}" "${BENCH_THRESHOLD_NET_PCT}" "${BENCH_THRESHOLD_IO_PCT}" << 'PY'
import json, sys
path, cpu_t, net_t, io_t = sys.argv[1], float(sys.argv[2]), float(sys.argv[3]), float(sys.argv[4])
with open(path) as f:
    data = json.load(f)
limits = {"cpu_overhead": cpu_t, "net_latency": net_t, "io_latency": io_t}
breaches = []
for key, thr in limits.items():
    sc = data["scenarios"].get(key, {})
    if sc.get("status") != "ok":
        continue
    full = sc.get("profiles", {}).get("full", {})
    dp = full.get("delta_percent")
    if dp is not None and dp > thr:
        breaches.append("%s full delta_percent=%.2f%% > %.2f%%" % (key, dp, thr))
if breaches:
    print("[BENCH][FAIL] regression threshold breached:", file=sys.stderr)
    for b in breaches:
        print("  - " + b, file=sys.stderr)
    sys.exit(1)
print("[BENCH] regression gate passed", file=sys.stderr)
PY
}

if ! check_regression; then
	exit 1
fi

log_info "done."
