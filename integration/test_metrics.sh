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

# Verify all expected procfs-based metrics are present.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"

readonly METRICS_FIXTURE_ROOT="${HUATUO_BAMAI_TEST_TMPDIR}/metrics-fixtures"
readonly METRICS_SOCKSTAT="${METRICS_FIXTURE_ROOT}/proc/1/net/sockstat"
readonly METRICS_TCP_MEM_SOCKSTAT="${METRICS_FIXTURE_ROOT}/proc/net/sockstat"
readonly METRICS_TCP_MEM="${METRICS_FIXTURE_ROOT}/proc/sys/net/ipv4/tcp_mem"
readonly METRICS_SOCKSTAT_EXPECTED="${HUATUO_BAMAI_TEST_TMPDIR}/expected-sockstat.txt"
require_commands getconf awk grep
METRICS_PAGE_SIZE=$(getconf PAGESIZE) || fatal "getconf PAGESIZE failed"
METRICS_LONG_BITS=$(getconf LONG_BIT) || fatal "getconf LONG_BIT failed"
[[ ${METRICS_PAGE_SIZE} =~ ^[1-9][0-9]*$ ]] || fatal "invalid page size: ${METRICS_PAGE_SIZE}"
[[ ${METRICS_LONG_BITS} == 32 || ${METRICS_LONG_BITS} == 64 ]] || fatal "unsupported native word size: ${METRICS_LONG_BITS}"
readonly METRICS_PAGE_SIZE METRICS_LONG_BITS

assert_sockstat_value() {
	local name=$1 expected=$2 actual
	expected=$(awk -v value="${expected}" 'BEGIN { printf "%.17g", value }')
	actual=$(awk -v name="huatuo_bamai_sockstat_${name}" '
		index($1, name "{") == 1 {
            if (NF != 2 || $2 !~ /^[+-]?([0-9]+([.][0-9]*)?|[.][0-9]+)([eE][+-]?[0-9]+)?$/) {
                print "invalid sample: " $0
            } else {
                printf "%.17g\n", $2
            }
        }
	' "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt")
	assert_eq "${actual}" "${expected}" "sockstat ${name}" \
		|| fatal "sockstat mismatch; see ${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt"
	grep -Fxq "# TYPE huatuo_bamai_sockstat_${name} gauge" "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt" \
		|| fatal "sockstat ${name}: expected gauge type"
}

assert_tcp_memory_usage_percent() {
	local metrics_file="${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt"
	awk '
		$1 ~ /^huatuo_bamai_tcp_memory_usage_pages\{/ { usage = $2 }
		$1 ~ /^huatuo_bamai_tcp_memory_limit_pages\{/ { limit = $2 }
		$1 ~ /^huatuo_bamai_tcp_memory_usage_percent\{/ { percent = $2 }
		END {
			if (usage == "" || limit == "" || percent == "") {
				printf "tcp memory samples missing: usage_pages=%s limit_pages=%s usage_percent=%s\n", usage, limit, percent > "/dev/stderr"
				exit 2
			}
			if (limit <= 0) {
				printf "tcp memory limit_pages is not positive: %s\n", limit > "/dev/stderr"
				exit 3
			}
			expected = usage / limit * 100
			delta = percent - expected
			if (delta < 0) {
				delta = -delta
			}
			if (delta > 1e-12) {
				printf "tcp memory usage_percent=%s, want %s (usage_pages=%s / limit_pages=%s * 100)\n", percent, expected, usage, limit > "/dev/stderr"
				exit 4
			}
		}
	' "${metrics_file}" \
		|| fatal "tcp memory usage_percent does not match usage_pages / limit_pages * 100; see ${metrics_file}"
}

assert_tcp_memory_at_limit() {
	local metrics_file="${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt"
	awk '
		$1 ~ /^huatuo_bamai_tcp_memory_usage_pages\{/ { usage = $2 }
		$1 ~ /^huatuo_bamai_tcp_memory_limit_pages\{/ { limit = $2 }
		$1 ~ /^huatuo_bamai_tcp_memory_usage_percent\{/ { percent = $2 }
		END {
			if (usage != 12340770 || limit != 12340770 || percent != 100) {
				printf "at-limit check failed: usage_pages=%s limit_pages=%s usage_percent=%s\n", usage, limit, percent > "/dev/stderr"
				exit 2
			}
		}
	' "${metrics_file}" \
		|| fatal "tcp memory at the limit must report usage_pages=limit_pages=12340770 and usage_percent=100; see ${metrics_file}"
}

assert_tcp_memory_zero_limit() {
	local metrics_file="${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt"
	awk '
		$1 ~ /^huatuo_bamai_tcp_memory_usage_pages\{/ { usage = $2 }
		$1 ~ /^huatuo_bamai_tcp_memory_limit_pages\{/ { limit = $2 }
		$1 ~ /^huatuo_bamai_tcp_memory_usage_percent\{/ { percent = $2 }
		END {
			if (usage == "" || limit == "" || percent == "") {
				printf "tcp memory samples missing: usage_pages=%s limit_pages=%s usage_percent=%s\n", usage, limit, percent > "/dev/stderr"
				exit 2
			}
			if (limit != 0 || usage <= 0 || percent != 0) {
				printf "zero-limit check failed: usage_pages=%s limit_pages=%s usage_percent=%s\n", usage, limit, percent > "/dev/stderr"
				exit 3
			}
		}
	' "${metrics_file}" \
		|| fatal "tcp memory zero limit must report usage_percent=0 with non-zero usage_pages; see ${metrics_file}"
}

assert_expected_metrics() {
	local file=$1 status=0 missing
	local samples="${HUATUO_BAMAI_TEST_TMPDIR}/expected-samples.txt"
	grep -v '^[[:space:]]*\(#\|$\)' "${file}" > "${samples}" || status=$?
	[[ ${status} -eq 0 ]] || fatal "cannot read nonempty metric expectations from ${file}: grep status=${status}"
	missing=$(grep -Fvx -f "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt" "${samples}") || status=$?
	case ${status} in
	0) fatal "missing metrics from ${file}: ${missing}" ;;
	1) log_info "metric expectations matched: ${file}" ;;
	*) fatal "cannot compare metrics from ${file}: grep status=${status}" ;;
	esac
}

# The checked-in byte expectations use 4 KiB pages; only these two samples
# depend on the host page size. Keep all other baseline records unchanged.
awk -v size="${METRICS_PAGE_SIZE}" '
	$1 ~ /^huatuo_bamai_sockstat_(TCP|UDP)_mem_bytes\{/ {
		$2 = sprintf("%.0f", $2 / 4096 * size)
	}
	{ print }
' "${HUATUO_BAMAI_TEST_EXPECTED}/test_sockstat.txt" > "${METRICS_SOCKSTAT_EXPECTED}"

cp -a "${HUATUO_BAMAI_TEST_FIXTURES}" "${METRICS_FIXTURE_ROOT}"
integration_huatuo_bamai_start write_config_bamai_default \
	--region dev --procfs-prefix "${METRICS_FIXTURE_ROOT}" \
	--disable-storage --disable-kubelet --log-debug

huatuo_bamai_await_metrics

for metrics_expected in "${HUATUO_BAMAI_TEST_EXPECTED}"/test_*.txt; do
	if [[ ${metrics_expected} == "${HUATUO_BAMAI_TEST_EXPECTED}/test_sockstat.txt" ]]; then
		metrics_expected=${METRICS_SOCKSTAT_EXPECTED}
	fi
	assert_expected_metrics "${metrics_expected}"
done

assert_tcp_memory_usage_percent

# At the limit the documented percentage is 100, not the raw ratio 1.
printf 'TCP: inuse 3 mem 12340770\nUDP: inuse 2 mem 6\nRAW: inuse 1\n' > "${METRICS_TCP_MEM_SOCKSTAT}.next"
mv "${METRICS_TCP_MEM_SOCKSTAT}.next" "${METRICS_TCP_MEM_SOCKSTAT}"
huatuo_bamai_collect_metrics
assert_tcp_memory_at_limit

# Changing the limit to zero must keep the percentage finite at zero instead
# of producing NaN or dividing by zero.
printf 'TCP: inuse 3 mem 10\nUDP: inuse 2 mem 6\nRAW: inuse 1\n' > "${METRICS_TCP_MEM_SOCKSTAT}.next"
mv "${METRICS_TCP_MEM_SOCKSTAT}.next" "${METRICS_TCP_MEM_SOCKSTAT}"
printf '0 0 0\n' > "${METRICS_TCP_MEM}.next"
mv "${METRICS_TCP_MEM}.next" "${METRICS_TCP_MEM}"
huatuo_bamai_collect_metrics
assert_tcp_memory_zero_limit

# Use a power of two so both the input and expected float64 result are exact.
# The product exceeds native int on both 32-bit and 64-bit Linux.
metrics_large_pages=$((1 << (METRICS_LONG_BITS - 2)))
metrics_large_bytes=$(awk -v pages="${metrics_large_pages}" -v size="${METRICS_PAGE_SIZE}" \
	'BEGIN { printf "%.0f", pages * size }')
for metrics_pages in 10 0 "${metrics_large_pages}"; do
	printf 'TCP: inuse 3 mem %s\nUDP: inuse 2 mem 6\nRAW: inuse 1\n' "${metrics_pages}" > "${METRICS_SOCKSTAT}.next"
	mv "${METRICS_SOCKSTAT}.next" "${METRICS_SOCKSTAT}"
	huatuo_bamai_collect_metrics
	if [[ ${metrics_pages} == "${metrics_large_pages}" ]]; then
		metrics_bytes=${metrics_large_bytes}
	else
		metrics_bytes=$((metrics_pages * METRICS_PAGE_SIZE))
	fi
	assert_sockstat_value TCP_mem "${metrics_pages}"
	assert_sockstat_value TCP_mem_bytes "${metrics_bytes}"
	assert_sockstat_value UDP_mem_bytes "$((6 * METRICS_PAGE_SIZE))"
	assert_sockstat_value RAW_inuse 1
	assert_metrics "protocol without Mem" -- 'sockstat_RAW_mem(_bytes)?\{'
done

# A removed field must also disappear from the next public scrape.
printf 'TCP: inuse 3\n' > "${METRICS_SOCKSTAT}.next"
mv "${METRICS_SOCKSTAT}.next" "${METRICS_SOCKSTAT}"
huatuo_bamai_collect_metrics
assert_sockstat_value TCP_inuse 3
assert_metrics "removed Mem" -- 'sockstat_TCP_mem(_bytes)?\{'
