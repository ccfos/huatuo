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

# Check real-container IOLatency ownership and same-window histogram sums.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_io.sh"

run_container_latency() {
	set -euo pipefail
	source "${ROOT_DIR}/integration/lib.sh"
	source "${ROOT_DIR}/integration/config.sh"
	source "${ROOT_DIR}/integration/lib_io.sh"
	source "${ROOT_DIR}/integration/lib_io_container.sh"
	source "${ROOT_DIR}/integration/lib_iolatency.sh"
	CURL_TIMEOUT=(--connect-timeout 2 --max-time 3)
	WAIT_HUATUO_BAMAI_TIMEOUT=30
	disk_lock=""

	cleanup_container_metrics() {
		local status=$? containers_gone=1
		trap - EXIT
		trap '' INT TERM
		set +e
		huatuo_bamai_stop || status=1
		io_container_cleanup || {
			containers_gone=0
			status=1
		}
		if ((containers_gone)); then
			[[ -z ${disk_lock} ]] || exec {disk_lock}<&-
			if ! io_test_cleanup_disks; then
				log_error "cannot reclaim IO disks ${io_test_disks[*]}; verify scsi_debug owner ${io_test_scsi_owner}"
				status=1
			fi
		else
			log_error "retaining disk and FIFO resources until container IO exits: ${runtime_work}"
		fi
		exit "${status}"
	}
	trap cleanup_container_metrics EXIT
	trap 'log_error "container IO test interrupted by INT; entering cleanup"; exit 130' INT
	trap 'log_error "container IO test interrupted by TERM or timeout; entering cleanup"; exit 143' TERM

	io_container_prepare
	io_test_prepare_disks 1
	trap 'log_error "container IO test interrupted by INT; entering cleanup"; exit 130' INT
	trap 'log_error "container IO test interrupted by TERM or timeout; entering cleanup"; exit 143' TERM
	((${#io_test_disks[@]} > 0)) || skip "container IO metrics require an eligible block device"
	device="/dev/${io_test_disks[0]}"
	disk=${device##*/}
	[[ ${device} -ef /proc/1/root${device} ]] \
		|| fatal "test device is not visible to host containerd: ${device}"
	printf 'device=%s\nowned=%s\nscsi_debug_owner=%s\n' \
		"${device}" "${io_test_scsi_owned}" "${io_test_scsi_owner}" >> "${CONTAINER_WORK}/resources.txt"
	write_container_metrics_config() {
		write_sched_tick_config
		sed 's/"iolatency"/"sched_tick"/' "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" \
			> "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf.next"
		mv "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf.next" "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf"
		io_container_write_pod_config
	}

	container_read() {
		local index=$1 before after
		read -ra before < "/sys/block/${disk}/stat"
		container_ctr tasks exec --exec-id "read-${index}-${RANDOM}" --fifo-dir "${runtime_work}/fifos" \
			"${container_ids[index]}" /bin/dd "if=${device}" of=/dev/null bs=4096 count=256 iflag=direct \
			> "${CONTAINER_WORK}/read-${index}.log" 2>&1 \
			|| fatal "direct read failed in container ${container_ids[index]}"
		read -ra after < "/sys/block/${disk}/stat"
		((after[0] > before[0])) || fatal "disk statistics did not confirm container reads: ${device}"
	}

	lock_container_disk() {
		if ((io_test_scsi_owned)) && command -v flock > /dev/null 2>&1; then
			exec {disk_lock}< "${device}"
			if ! flock --exclusive --timeout 5 "${disk_lock}"; then
				exec {disk_lock}<&-
				disk_lock=""
				log_info "NOT_RUN L08 strict conservation: exclusive disk lock is unavailable"
			fi
		fi
	}

	scrape_container_iolatency() {
		local output=$1
		container_scrape_started=$(date +%s.%N)
		huatuo_bamai_metrics > "${output}" || fatal "container interval scrape failed: ${output}"
		container_scrape_ended=$(date +%s.%N)
	}

	assert_container_metrics() {
		local active=$1 output=$2 strict=${3:-0} mode=${4:-active} status=0 container_status=0
		assert_iolatency_metrics "${disk}" read "${mode}" "${output}" \
			"${container_scrape_started}" "${container_scrape_ended}" \
			"${CONTAINER_WORK}/last-interval" || status=$?
		((status == 0 || status == 3)) || return "${status}"
		awk -v active="${active}" -v disk="${disk}" -v pod_namespace="${POD_NAMESPACE}" \
			-v strict="${strict}" -v mode="${mode}" '
		function label(row, key, parts) {
			if (split(row, parts, key "=\"") != 2) return ""
			sub(/".*/, "", parts[2]); return parts[2]
		}
		function fail(message) { print message > "/dev/stderr"; bad = 1 }
		BEGIN {
			n = split(active, owners, ","); for (i = 1; i <= n; i++) wanted[owners[i]] = 1
			n = split("q2g_seconds_bucket q2d_seconds_bucket d2c_seconds_bucket queued_io_size_bytes_bucket issued_io_size_bytes_bucket", names)
			for (i = 1; i <= n; i++) known[names[i]] = 1
			required["d2c_seconds_bucket"] = required["queued_io_size_bytes_bucket"] = required["issued_io_size_bytes_bucket"] = 1
		}
		/^huatuo_bamai_iolatency_/ {
			if (label($1, "disk") != disk || label($1, "operation") != "read") next
			is_container = ($1 ~ /^huatuo_bamai_iolatency_container_/)
			if (is_container && label($1, "container_hostnamespace") != pod_namespace) next
			name = $1; sub(/^huatuo_bamai_iolatency_(container_)?/, "", name); sub(/\{.*/, "", name)
			if (!(name in known)) next
			bound = label($1, "le")
			if (bound == "" || $2 !~ /^[0-9]+([.][0-9]+)?([eE][+-]?[0-9]+)?$/ || $2 + 0 != int($2 + 0)) fail("invalid bucket: " $0)
			key = name SUBSEP bound
			if (!is_container) {
				if (++host_seen[key] != 1) fail("duplicate host bucket: " $0)
				host[key] = $2 + 0
				if (bound == "+Inf" && $2 + 0 > 0) has_positive = 1
				next
			}
			owner = label($1, "container_name"); sub(/^reader-/, "", owner)
			if (owner !~ /^[01]$/ || label($1, "container_host") != "huatuo-io-" owner ||
				label($1, "container_level") != "besteffort" || label($1, "container_type") != "normal") fail("incorrect container labels: " $0)
			if (++seen[owner, key] != 1) fail("duplicate container bucket: " $0)
			if (!(owner in wanted) && $2 + 0 != 0) fail("inactive container received IO: " $0)
			if (bound == "+Inf" && $2 + 0 > 0) positive[owner, name] = has_positive = 1
			container_sum[key] += $2 + 0
		}
		END {
			for (owner in wanted) for (name in required) if (!positive[owner, name]) {
				if (mode == "warmup") missing = 1
				else fail("completed IO lacks " name " for active container: " owner)
			}
			if (strict) {
				for (key in host) if (!(key in container_sum) || host[key] != container_sum[key]) fail("host/container bucket sum differs: " key)
				for (key in container_sum) if (!(key in host)) fail("container bucket lacks host counterpart: " key)
			}
			exit (bad ? 1 : (has_positive && !missing ? 0 : 3))
		}
	' "${output}" || container_status=$?
		((container_status == 0 || container_status == 3)) || return "${container_status}"
		((status == 0 && container_status == 0)) || return 3
	}

	assert_container_window() {
		local active=$1 owners=$2 output=$3 index strict=0 expected_sectors before after status=0
		local -a readers=() stats_before=() stats_after=()
		read -ra stats_before < "/sys/block/${disk}/stat"
		scrape_container_iolatency "${output}.baseline"
		assert_iolatency_metrics "${disk}" read drain "${output}.baseline" \
			"${container_scrape_started}" "${container_scrape_ended}" \
			"${CONTAINER_WORK}/last-interval" || status=$?
		((status == 0 || status == 3)) || fatal "container interval boundary violates the public IOLatency contract"
		IFS=, read -ra readers <<< "${active}"
		for index in "${readers[@]}"; do container_read "${index}"; done
		scrape_container_iolatency "${output}"
		read -ra stats_after < "/sys/block/${disk}/stat"
		expected_sectors=$((${#readers[@]} * 256 * 4096 / 512))
		before=${stats_before[2]}
		after=${stats_after[2]}
		printf 'before=%s\nafter=%s\nexpected_read_sectors=%s\n' \
			"${stats_before[*]}" "${stats_after[*]}" "${expected_sectors}" > "${output}.diskstats"
		# Completed raw direct reads have known bytes, not a known request count.
		# A locked owned disk and matching sector delta exclude unrelated reads.
		if ((io_test_scsi_owned)) && [[ -n ${disk_lock} ]] && ((after - before == expected_sectors)); then
			strict=1
		else
			log_info "NOT_RUN L08 strict conservation for ${output##*/}: disk ownership, lock or read-byte isolation was not established"
		fi
		status=0
		assert_container_metrics "${owners}" "${output}" "${strict}" || status=$?
		((status == 0)) || fatal "container labels, mandatory distributions or same-window bucket conservation failed for ${active}"
		if ((strict)); then
			log_info "L08: same-window host/container bucket sums verified for ${output##*/}"
		fi
	}

	assert_container_distribution_ready() {
		local prefix=$1 attempt status
		for ((attempt = 1; attempt <= 10; attempt++)); do
			container_read 0
			scrape_container_iolatency "${CONTAINER_WORK}/${prefix}-${attempt}.metrics"
			status=0
			assert_container_metrics 0 "${CONTAINER_WORK}/${prefix}-${attempt}.metrics" 0 warmup || status=$?
			((status != 0)) || return 0
			((status == 3)) || fatal "container IOLatency schema or ownership contract failed during warmup"
			sleep 1
		done
		fatal "completed container reads did not produce mandatory host/owner IOLatency distributions; inspect ${CONTAINER_WORK}/${prefix}-*.metrics"
	}

	start_io_container 0
	write_container_pods 1
	io_container_start_kubelet
	SSL_CERT_FILE="${CONTAINER_WORK}/kubelet.crt" \
		integration_huatuo_bamai_start write_container_metrics_config --region dev --disable-storage --log-debug
	wait_until 30 1 assert_container_metadata_ready 0 || fatal "Huatuo did not recognize the first real container"
	lock_container_disk
	assert_container_distribution_ready single

	start_io_container 1
	write_container_pods 2
	wait_until 30 1 assert_container_metadata_ready 1 || fatal "Huatuo did not recognize the second real container"
	for active in 0 1 0,1; do
		assert_container_window "${active}" "${active}" "${CONTAINER_WORK}/active-${active}.metrics"
	done
	log_info "L08: ordinary single/dual container IO labels, nonempty distributions and owner isolation verified"

	[[ -z ${disk_lock} ]] || exec {disk_lock}<&-
	disk_lock=""
	huatuo_bamai_stop
	write_container_pods 2 shared
	SSL_CERT_FILE="${CONTAINER_WORK}/kubelet.crt" \
		integration_huatuo_bamai_start write_container_metrics_config --region dev --disable-storage --log-debug
	for index in 0 1; do
		wait_until 30 1 assert_container_metadata_ready "${index}" || fatal "shared-label container was not recognized"
	done
	lock_container_disk
	assert_container_distribution_ready shared
	# Each real identity must contribute to the same public label set, including
	# when both read between scrapes. Duplicate label sets fail the same checker.
	for active in 1 0,1; do
		assert_container_window "${active}" 0 "${CONTAINER_WORK}/shared-active-${active}.metrics"
	done
	log_info "L08: different container identities merged into one public histogram label set"
	exit 0
}

case ${TEST_IOLATENCY_SCOPE:-all} in
all) io_test_run_cases "$0" TEST_IOLATENCY_SCOPE container ;;
container) run_container_latency ;;
*) fatal "TEST_IOLATENCY_SCOPE must be all or container" ;;
esac
