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

# Verify registration and the public Prometheus contract using diskstats input
# fixtures. No disk workload is needed; the normal agent performs each scrape.
# Each configuration has a 20-second startup bound and its own daemon.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"

# Restore the shared curl bounds: exported arrays do not reach this subprocess.
CURL_TIMEOUT=(--connect-timeout 2 --max-time 3)
WAIT_HUATUO_BAMAI_TIMEOUT=20
fixture_root="${HUATUO_BAMAI_TEST_TMPDIR}/iotracing-fixtures"
metrics_file="${HUATUO_BAMAI_TEST_TMPDIR}/iotracing-metrics.txt"

write_iotracing_metrics_config() {
	write_default_config
	# Enable only this tracer without changing the other fixture exclusions.
	sed 's/, "iotracing"//' "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" \
		> "${HUATUO_BAMAI_TEST_TMPDIR}/iotracing-metrics.conf"
	mv "${HUATUO_BAMAI_TEST_TMPDIR}/iotracing-metrics.conf" "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf"
}

check_iotracing_metrics() {
	local active=$1
	huatuo_bamai_metrics > "${metrics_file}"
	awk -v active="${active}" '
		BEGIN {
			n = split("read_bytes_per_second write_bytes_per_second read_iops write_iops read_await_milliseconds write_await_milliseconds io_utilization_percent average_queue_size", suffix)
			for (i = 1; i <= n; i++) expected["huatuo_bamai_iotracing_" suffix[i]] = 1
		}
		$1 == "#" && $2 == "TYPE" && ($3 in expected) {
			if ($4 != "gauge") bad = 1
			types[$3]++
		}
		$1 ~ /^huatuo_bamai_iotracing_/ {
			name = $1
			sub(/\{.*/, "", name)
			if (!(name in expected) || $1 !~ /device="sda"/) bad = 1
			rows[name]++
			if (active && !($2 + 0 > 0)) bad = 1
			if (!active && $2 + 0 != 0) bad = 1
			if (active && name ~ /read_await_milliseconds$/ && $2 + 0 != 2.5) bad = 1
			if (active && name ~ /write_await_milliseconds$/ && $2 + 0 != 3.5) bad = 1
		}
		END {
			for (name in expected) if (types[name] != 1 || rows[name] != 1) bad = 1
			exit bad
		}
	' "${metrics_file}" || fatal "iotracing Prometheus contract mismatch: ${metrics_file}"
}

cp -a "${HUATUO_BAMAI_TEST_FIXTURES}" "${fixture_root}"
mkdir -p "${fixture_root}/sys/dev/block/8:0"
printf '%s\n' '8 0 sda 100 0 1000 100 100 0 1000 100 0 100 100' \
	> "${fixture_root}/proc/diskstats"

integration_huatuo_bamai_start write_iotracing_metrics_config \
	--region dev --procfs-prefix "${fixture_root}" \
	--disable-storage --disable-kubelet --log-debug
check_iotracing_metrics 0

# Atomically replace input between two scrapes. Only rates depend on elapsed
# wall time; await has an exact fractional result independent of test timing.
printf '%s\n' '8 0 sda 102 0 1004 105 102 0 1006 107 0 110 120' \
	> "${fixture_root}/proc/diskstats.next"
mv "${fixture_root}/proc/diskstats.next" "${fixture_root}/proc/diskstats"
check_iotracing_metrics 1
huatuo_bamai_stop
cp "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-iotracing-enabled.log"

integration_huatuo_bamai_start write_iotracing_metrics_config \
	--region dev --procfs-prefix "${fixture_root}" \
	--disable-storage --disable-kubelet --log-debug --disable-tracing iotracing
huatuo_bamai_metrics > "${metrics_file}"
if grep -q '^huatuo_bamai_iotracing_' "${metrics_file}"; then
	fatal "blacklisted iotracing still exports metrics"
fi
huatuo_bamai_stop
cp "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-iotracing-disabled.log"

log_info "iotracing metrics and blacklist verified through /metrics"
