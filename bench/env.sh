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

# Benchmark environment defaults. Source once; override any variable from the
# environment before invoking bench/run.sh (e.g. from CI or the Makefile).

set -euo pipefail

if [[ -n "${__HUATUO_BENCH_ENV_SH_LOADED:-}" ]]; then
	return 0
fi
export __HUATUO_BENCH_ENV_SH_LOADED=1

BENCH_ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export BENCH_ROOT_DIR
export ROOT_DIR="${ROOT_DIR:-$(cd "${BENCH_ROOT_DIR}/.." && pwd)}"

# ---- binaries ---------------------------------------------------------------

# The huatuo-bamai binary under test. Defaults to the `make all` output path so
# `make bench` works out of the box.
HUATUO_BAMAI_BIN="${HUATUO_BAMAI_BIN:-${ROOT_DIR}/_output/bin/huatuo-bamai}"
export HUATUO_BAMAI_BIN

# ---- tunables ---------------------------------------------------------------

# Number of A/B repetitions per scenario (one extra iteration is run as warmup).
BENCH_ITERATIONS="${BENCH_ITERATIONS:-5}"
export BENCH_ITERATIONS

# Discard the first iteration of each phase to hide cold-start effects.
BENCH_WARMUP="${BENCH_WARMUP:-1}"
export BENCH_WARMUP

# When 1, fail the run if any measured delta_percent crosses its threshold.
# 0 (default) only records the regression in the report so a benchmark lane can
# never turn red on a noisy measurement.
BENCH_FAIL_ON_REGRESSION="${BENCH_FAIL_ON_REGRESSION:-0}"
export BENCH_FAIL_ON_REGRESSION

# When 1, exit non-zero if a scenario's precondition (root, binary, kernel
# feature) is not satisfied. Default 0 -> skip and record the reason.
BENCH_FAIL_ON_MISSING="${BENCH_FAIL_ON_MISSING:-0}"
export BENCH_FAIL_ON_MISSING

# CI regression thresholds (percent). Used only when BENCH_FAIL_ON_REGRESSION=1.
BENCH_THRESHOLD_CPU_PCT="${BENCH_THRESHOLD_CPU_PCT:-5}"
BENCH_THRESHOLD_NET_PCT="${BENCH_THRESHOLD_NET_PCT:-10}"
BENCH_THRESHOLD_IO_PCT="${BENCH_THRESHOLD_IO_PCT:-10}"
export BENCH_THRESHOLD_CPU_PCT BENCH_THRESHOLD_NET_PCT BENCH_THRESHOLD_IO_PCT

# ---- workload sizes ---------------------------------------------------------

# CPU workload: bytes to copy through dd. Larger => lower relative noise.
BENCH_CPU_BYTES="${BENCH_CPU_BYTES:-$((64 * 1024 * 1024))}" # 64 MiB
export BENCH_CPU_BYTES

# Memory overhead: sampling window (seconds) under sustained load.
BENCH_MEM_DURATION="${BENCH_MEM_DURATION:-15}"
export BENCH_MEM_DURATION

# Network latency: ping packet count per sample.
BENCH_NET_PACKETS="${BENCH_NET_PACKETS:-200}"
export BENCH_NET_PACKETS

# IO latency: number of synchronized writes per sample.
BENCH_IO_OPS="${BENCH_IO_OPS:-200}"
export BENCH_IO_OPS

# IO workload block size (KiB).
BENCH_IO_BLOCK_KIB="${BENCH_IO_BLOCK_KIB:-4}"
export BENCH_IO_BLOCK_KIB

# ---- huatuo-bamai module model ---------------------------------------------
#
# HUATUO_CORE_BLACKLIST   always-blacklisted (hardware collectors that need the
#                         matching device to be present).
# HUATUO_OPTIONAL_MODULES software collectors that the BlackList can toggle.
#
# The scenarios run each metric under up to three collection profiles:
#   "full"    = the shipped huatuo-bamai.conf (realistic multi-collector setup,
#               worst-case overhead)
#   "minimal" = core + every optional collector blacklisted -> lower bound
#   "single"  = core + every optional collector except one -> isolates that one
#               (net/io scenarios only)
# bench_blacklist_all      builds the "minimal" blacklist.
# bench_blacklist_except M builds the "single" blacklist (keeps M enabled).
HUATUO_CORE_BLACKLIST=("metax_gpu" "ascend_npu")
export HUATUO_CORE_BLACKLIST
HUATUO_OPTIONAL_MODULES=(
	"softlockup" "ethtool" "netstat_hw" "iolatency" "memory_free"
	"memory_reclaim" "reschedipi" "softirq" "iotracing" "dropwatch"
	"netdev_hw"
)
export HUATUO_OPTIONAL_MODULES

# Which optional module the single profile isolates for each probe-heavy metric.
BENCH_NET_SINGLE_MODULE="${BENCH_NET_SINGLE_MODULE:-dropwatch}"
export BENCH_NET_SINGLE_MODULE
BENCH_IO_SINGLE_MODULE="${BENCH_IO_SINGLE_MODULE:-iolatency}"
export BENCH_IO_SINGLE_MODULE

# ---- output -----------------------------------------------------------------

BENCH_RESULTS_DIR="${BENCH_RESULTS_DIR:-${BENCH_ROOT_DIR}/results}"
export BENCH_RESULTS_DIR
