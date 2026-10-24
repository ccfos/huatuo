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

# Check fixture calculations, metrics transport and completed real-disk IO.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_io.sh"

run_fixture_metrics() {
	set -euo pipefail
	source "${ROOT_DIR}/integration/lib.sh"
	source "${ROOT_DIR}/integration/config.sh"
	require_commands python3 jq

	# Restore the shared curl bounds: exported arrays do not reach this subprocess.
	CURL_TIMEOUT=(--connect-timeout 2 --max-time 3)
	WAIT_HUATUO_BAMAI_TIMEOUT=20
	fixture_root="${HUATUO_BAMAI_TEST_TMPDIR}/iotracing-fixtures"
	metrics_file="${HUATUO_BAMAI_TEST_TMPDIR}/iotracing-metrics.txt"
	previous_start=0
	previous_end=0
	second_disk=0

	monotonic_ns() {
		python3 -c 'import time; print(int(time.monotonic() * 1000000000))'
	}

	write_iotracing_diskstats() {
		local first=$1 second=${2:-0}
		{
			printf '8 0 sda %s 0 %s %s %s 0 %s %s 0 %s %s\n' \
				"$((100 + 2 * first))" "$((1000 + 4 * first))" "$((100 + 5 * first))" \
				"$((100 + 2 * first))" "$((1000 + 6 * first))" "$((100 + 7 * first))" \
				"$((100 + 10 * first))" "$((100 + 2000 * first))"
			if ((second_disk)); then
				printf '8 16 sdb %s 0 %s %s %s 0 %s %s 0 %s %s\n' \
					"$((100 + 2 * second))" "$((1000 + 16 * second))" "$((100 + 14 * second))" \
					"$((100 + 2 * second))" "$((1000 + 32 * second))" "$((100 + 22 * second))" \
					"$((100 + 20 * second))" "$((100 + 4000 * second))"
			fi
			printf '%s\n' \
				'8 1 sda1 100 0 1000 100 100 0 1000 100 0 100 100' \
				'7 0 loop0 100 0 1000 100 100 0 1000 100 0 100 100' \
				'1 0 ram0 100 0 1000 100 100 0 1000 100 0 100 100'
		} > "${fixture_root}/proc/diskstats.next"
		mv "${fixture_root}/proc/diskstats.next" "${fixture_root}/proc/diskstats"
	}

	write_iotracing_metrics_config() {
		write_iotracing_autotracing_config
		# The public statistics fixture needs no tracing probes or capture process.
		sed -e 's/^BlackList = \[/BlackList = ["blk_throtl", "io_health", "irqtracing", "tcp_retransmit", /' \
			-e 's/UtilThreshold = 1$/UtilThreshold = 1000000/' \
			"${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" \
			> "${HUATUO_BAMAI_TEST_TMPDIR}/iotracing-metrics.conf"
		mv "${HUATUO_BAMAI_TEST_TMPDIR}/iotracing-metrics.conf" "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf"
	}

	assert_iotracing_metrics() {
		local active=$1 disks=${2:-sda} allow_missing=${3:-0}
		local started ended minimum maximum
		started=$(monotonic_ns)
		huatuo_bamai_metrics > "${metrics_file}"
		ended=$(monotonic_ns)
		minimum=$((started - previous_end))
		maximum=$((ended - previous_start))
		awk -v active="${active}" -v disks="${disks}" -v allow_missing="${allow_missing}" \
			-v minimum="${minimum}" -v maximum="${maximum}" '
		function fail(message) { print message > "/dev/stderr"; bad = 1 }
		function check_rate(name, value, delta, lower, upper) {
			lower = delta / (maximum / 1000000000) - 0.00501
			upper = delta / (minimum / 1000000000) + 0.00501
			if (value < lower || value > upper)
				fail(name "=" value " outside request-bounded range [" lower "," upper "]")
		}
		BEGIN {
			n = split("read_bps write_bps read_iops write_iops read_await_ms write_await_ms io_util_percent average_queue_size", suffix)
			for (i = 1; i <= n; i++) expected["huatuo_bamai_iotracing_" suffix[i]] = 1
			count = split(disks, device)
			for (i = 1; i <= count; i++) wanted[device[i]] = 1
			if (active && (minimum <= 0 || maximum < minimum)) fail("invalid scrape interval")
		}
		$1 == "#" && $2 == "TYPE" && ($3 in expected) {
			if ($4 != "gauge") bad = 1
			types[$3]++
		}
		$1 ~ /^huatuo_bamai_iotracing_/ {
			name = $1
			sub(/\{.*/, "", name)
			disk = $1
			sub(/.*disk="/, "", disk)
			sub(/".*/, "", disk)
			if (!(name in expected) || !(disk in wanted)) fail("unexpected series: " $0)
			if ($1 !~ /host="huatuo-dev"/ || $1 !~ /region="dev"/) fail("missing public labels: " $0)
			rows[disk, name]++
			if ($2 !~ /^[0-9]+([.][0-9]+)?([eE][+-]?[0-9]+)?$/) fail("invalid value: " $0)
			value = $2 + 0
			if (!active && value != 0) fail("stale or cross-baseline increment: " $0)
			if (!active) next
			if (!(value > 0)) fail("missing positive increment: " $0)
			if (name ~ /read_await_ms$/ && value != (disk == "sdb" ? 7 : 2.5)) fail("incorrect read await: " $0)
			if (name ~ /write_await_ms$/ && value != (disk == "sdb" ? 11 : 3.5)) fail("incorrect write await: " $0)
			if (name ~ /read_bps$/) check_rate(name, value, disk == "sdb" ? 8192 : 2048)
			if (name ~ /write_bps$/) check_rate(name, value, disk == "sdb" ? 16384 : 3072)
			if (name ~ /_iops$/) check_rate(name, value, 2)
			if (name ~ /io_util_percent$/) check_rate(name, value, disk == "sdb" ? 2 : 1)
			if (name ~ /average_queue_size$/) check_rate(name, value, disk == "sdb" ? 4 : 2)
		}
		END {
			for (disk in wanted) for (name in expected) {
				if (allow_missing && rows[disk, name] == 0) continue
				if (types[name] != 1 || rows[disk, name] != 1) fail("missing or duplicate series: " disk "/" name)
			}
			exit bad
		}
	' "${metrics_file}" || fatal "iotracing Prometheus contract mismatch: ${metrics_file}"
		previous_start=${started}
		previous_end=${ended}
	}

	cp -a "${HUATUO_BAMAI_TEST_FIXTURES}" "${fixture_root}"
	mkdir -p "${fixture_root}/sys/dev/block/"{8:0,8:1,7:0,1:0}
	printf '1\n' > "${fixture_root}/sys/dev/block/8:1/partition"
	write_iotracing_diskstats 0

	assert_iotracing_collection_failed() {
		awk '
		$1 ~ /^huatuo_bamai_scrape_collector_success\{/ && $1 ~ /collector="iotracing"/ { found++; if ($2 != 0) bad = 1 }
		END { exit !(found == 1 && !bad) }
	' "${metrics_file}" || fatal "missing failed iotracing collector status after diskstats read error"
	}

	integration_huatuo_bamai_start write_iotracing_metrics_config \
		--region dev --procfs-prefix "${fixture_root}" \
		--disable-storage --disable-kubelet --log-debug
	assert_iotracing_metrics 0

	write_iotracing_diskstats 1
	assert_iotracing_metrics 1
	assert_iotracing_metrics 0
	log_info "M02 M03 M05: bounded rates, fractional await, idle intervals and device exclusions passed"

	# Removing the public input causes a real read error, without substituting the
	# collector or interpreting an HTTP failure as a failed collection.
	mv "${fixture_root}/proc/diskstats" "${fixture_root}/proc/diskstats.saved"
	assert_iotracing_metrics 0 sda 1
	assert_iotracing_collection_failed

	write_iotracing_diskstats 100
	assert_iotracing_metrics 0 sda 1
	write_iotracing_diskstats 101
	assert_iotracing_metrics 1
	log_info "R03: read failure discarded the old interval and recovery rebuilt the baseline"

	write_iotracing_diskstats 0
	assert_iotracing_metrics 0 sda 1
	write_iotracing_diskstats 1
	assert_iotracing_metrics 1
	log_info "M06: reset counters were not carried across the baseline"

	mkdir -p "${fixture_root}/sys/dev/block/8:16"
	second_disk=1
	write_iotracing_diskstats 1 0
	assert_iotracing_metrics 0 'sda sdb' 1
	write_iotracing_diskstats 2 1
	assert_iotracing_metrics 1 'sda sdb'
	assert_iotracing_metrics 0 'sda sdb'
	log_info "M04: new disk baseline and distinct per-device rates passed"

	huatuo_bamai_stop
	cp "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-iotracing-recovery.log"
	integration_huatuo_bamai_start write_iotracing_metrics_config \
		--region dev --procfs-prefix "${fixture_root}" \
		--disable-storage --disable-kubelet --log-debug
	assert_iotracing_metrics 0 'sda sdb'
	write_iotracing_diskstats 3 2
	assert_iotracing_metrics 1 'sda sdb'
	log_info "R05: daemon restart established a fresh baseline"
	assert_iotracing_metrics 0 'sda sdb'
	assert_iotracing_protobuf() {
		local response="${HUATUO_BAMAI_TEST_TMPDIR}/metrics-protobuf"
		curl --fail --silent --show-error "${CURL_TIMEOUT[@]}" \
			-H 'Accept: application/vnd.google.protobuf; proto=io.prometheus.client.MetricFamily; encoding=delimited' \
			-D "${response}.headers" -o "${response}.body" "${HUATUO_BAMAI_METRICS_API}" \
			|| fatal "protobuf metrics HTTP request failed"
		awk '
			BEGIN { IGNORECASE = 1 }
			{ line = tolower($0) }
			line ~ /^content-type:/ {
				found++
				if (line !~ /^content-type: application\/vnd.google.protobuf;/ ||
					line !~ /proto=io.prometheus.client.metricfamily/ || line !~ /encoding=delimited/) bad = 1
			}
			END { exit !(found == 1 && !bad) }
		' "${response}.headers" || fatal "metrics did not negotiate delimited MetricFamily protobuf"
		python3 "${ROOT_DIR}/integration/testdata/test_metrics_protobuf.py" "${response}.body" \
			> "${response}.json" || fatal "cannot decode the protobuf metrics response"
		jq -e '
			[.[] | select(.name == "huatuo_bamai_iotracing_read_bps")] as $families
			| ($families | length) == 1
			and ($families[0] | .type == 1 and (.metrics | length) == 2
				and all(.metrics[];
					.gauges == [0] and (.labels | length) == 3
					and (.labels | map({key: .name, value: .value}) | from_entries
						| .host == "huatuo-dev" and .region == "dev" and (.disk == "sda" or .disk == "sdb")))
				and ([.metrics[].labels[] | select(.name == "disk") | .value] | sort) == ["sda", "sdb"])
		' "${response}.json" > /dev/null || fatal "protobuf metrics omitted or changed the idle fixture Gauges"
	}
	assert_iotracing_protobuf
	huatuo_bamai_stop
	cp "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-iotracing-enabled.log"

	integration_huatuo_bamai_start write_iotracing_metrics_config \
		--region dev --procfs-prefix "${fixture_root}" \
		--disable-storage --disable-kubelet --log-debug --disable-tracing iotracing
	assert_iotracing_disabled() {
		huatuo_bamai_metrics > "${metrics_file}"
		if grep -q '^huatuo_bamai_iotracing_' "${metrics_file}"; then
			fatal "blacklisted iotracing still exports metrics"
		fi
	}
	assert_iotracing_disabled
	huatuo_bamai_stop
	cp "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-iotracing-disabled.log"

	log_info "iotracing metrics and blacklist verified through /metrics"
	exit 0
}

run_disk_metrics() {
	set -euo pipefail
	source "${ROOT_DIR}/integration/lib.sh"
	source "${ROOT_DIR}/integration/config.sh"
	source "${ROOT_DIR}/integration/lib_io.sh"
	CURL_TIMEOUT=(--connect-timeout 2 --max-time 3)
	WAIT_HUATUO_BAMAI_TIMEOUT=20
	require_commands dd timeout awk

	cleanup_iotracing_disk_io() {
		local status=$?
		trap - EXIT
		trap '' INT TERM
		set +e
		huatuo_bamai_stop || status=1
		if ! io_test_cleanup_disks; then
			log_error "cannot reclaim IO disks; inspect ${HUATUO_BAMAI_TEST_TMPDIR}/io-disks.resources before reusing the environment"
			status=1
		fi
		exit "${status}"
	}
	trap cleanup_iotracing_disk_io EXIT
	trap 'log_error "disk IO test interrupted by INT; entering cleanup"; exit 130' INT
	trap 'log_error "disk IO test interrupted by TERM or execution timeout; entering cleanup"; exit 143' TERM
	io_test_prepare_disks 1
	trap 'log_error "disk IO test interrupted by INT; entering cleanup"; exit 130' INT
	trap 'log_error "disk IO test interrupted by TERM or execution timeout; entering cleanup"; exit 143' TERM
	((${#io_test_disks[@]} > 0)) || skip "real IO metrics require an eligible disk"
	disk=${io_test_disks[0]}

	write_real_iotracing_config() {
		write_sched_tick_config
		sed 's/"iotracing"/"sched_tick"/' "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" \
			> "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf.next"
		mv "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf.next" "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf"
		cat >> "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" << 'EOF'

[AutoTracing.IOTracing]
    RbpsThreshold = 1000000
    WbpsThreshold = 1000000
    UtilThreshold = 1000000
    AwaitThreshold = 1000000
EOF
	}

	run_disk_workload() {
		local direction=$1 size=$2 count=$3 owner
		if [[ ${direction} == write ]]; then
			((io_test_disk_writable)) || fatal "writes are not authorized for /dev/${disk}"
			if ((io_test_scsi_owned)); then
				read -r owner < /sys/module/scsi_debug/parameters/inq_vendor
				[[ ${owner} == "${io_test_scsi_owner}" ]] || fatal "scsi_debug ownership changed"
				read -r owner < "/sys/block/${disk}/device/vendor"
				[[ ${owner} == "${io_test_scsi_owner}" ]] || fatal "test disk identity changed: ${disk}"
			else
				[[ ${disk} == vd* ]] && io_test_disk_is_free "${disk}" \
					|| fatal "authorized virtio disk is no longer free: ${disk}"
			fi
			timeout --kill-after=2 10 dd if=/dev/zero "of=/dev/${disk}" "bs=${size}" "count=${count}" \
				oflag=direct conv=fsync status=none || fatal "direct write failed on ${disk}"
		else
			timeout --kill-after=2 10 dd "if=/dev/${disk}" of=/dev/null "bs=${size}" "count=${count}" \
				iflag=direct status=none || fatal "direct read failed on ${disk}"
		fi
	}

	assert_real_disk_metrics() {
		local direction=$1 size=$2 count=$3 before after file
		file="${HUATUO_BAMAI_TEST_TMPDIR}/${direction}-${size}"
		huatuo_bamai_metrics > "${file}.baseline" || fatal "baseline scrape failed"
		read -ra before < "/sys/block/${disk}/stat"
		[[ ${direction} == write ]] || run_disk_workload read "${size}" "${count}"
		[[ ${direction} == read ]] || run_disk_workload write "${size}" "${count}"
		read -ra after < "/sys/block/${disk}/stat"
		printf 'before=%s\nafter=%s\n' "${before[*]}" "${after[*]}" > "${file}.diskstats"
		[[ ${direction} == write ]] || ((after[0] > before[0])) || fatal "no completed read IO on ${disk}"
		[[ ${direction} == read ]] || ((after[4] > before[4])) || fatal "no completed write IO on ${disk}"
		huatuo_bamai_metrics > "${file}.metrics" || fatal "post-workload scrape failed"
		awk -v disk="${disk}" -v direction="${direction}" '
		function fail(message) { print message > "/dev/stderr"; bad = 1 }
		BEGIN {
			n = split("read_bps write_bps read_iops write_iops read_await_ms write_await_ms io_util_percent average_queue_size", suffix)
			for (i = 1; i <= n; i++) expected["huatuo_bamai_iotracing_" suffix[i]] = 1
		}
		$1 == "#" && $2 == "TYPE" && ($3 in expected) { types[$3]++; if ($4 != "gauge") fail("expected Gauge: " $0) }
		$1 ~ /^huatuo_bamai_iotracing_/ && $1 ~ ("[{,]disk=\"" disk "\"") {
			name = $1; sub(/\{.*/, "", name)
			if (!(name in expected)) fail("unexpected metric: " $0)
			rows[name]++
			if ($1 !~ /[{,]host="huatuo-dev"/ || $1 !~ /[{,]region="dev"/) fail("incorrect labels: " $0)
			labels = $1; sub(/^[^{]*\{/, "", labels); sub(/\}$/, "", labels)
			if (split(labels, label, ",") != 3) fail("unexpected label set: " $0)
			if ($2 !~ /^[0-9]+([.][0-9]+)?([eE][+-]?[0-9]+)?$/) fail("nonfinite or negative value: " $0)
			if (direction != "write" && name ~ /read_(bps|iops)$/ && !($2 + 0 > 0)) fail("missing read activity: " $0)
			if (direction != "read" && name ~ /write_(bps|iops)$/ && !($2 + 0 > 0)) fail("missing write activity: " $0)
		}
		END { for (name in expected) if (rows[name] != 1 || types[name] != 1) fail("missing/duplicate metric: " name); exit bad }
	' "${file}.metrics" || fatal "${direction}/${size}: real-disk iotracing contract failed"
		log_info "M01: ${disk} ${direction} input_size=${size} verified through diskstats and eight metrics"
	}

	integration_huatuo_bamai_start write_real_iotracing_config \
		--region dev --disable-kubelet --disable-storage --log-debug
	assert_real_disk_metrics read 4096 64
	assert_real_disk_metrics read 1048576 4
	if ((io_test_disk_writable)); then
		assert_real_disk_metrics write 4096 64
		assert_real_disk_metrics write 1048576 4
		# Both directions complete inside one interval; no precise request count is assumed.
		assert_real_disk_metrics mixed 65536 16
	else
		log_info "NOT_RUN M01 write/mixed: /dev/${disk} is a read-only fallback"
	fi
	exit 0
}

case ${TEST_IOTRACING_METRICS_MODE:-all} in
all) io_test_run_cases "$0" TEST_IOTRACING_METRICS_MODE fixture disk ;;
fixture) run_fixture_metrics ;;
disk) run_disk_metrics ;;
*) fatal "TEST_IOTRACING_METRICS_MODE must be all, fixture or disk" ;;
esac
