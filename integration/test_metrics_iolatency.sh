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

# Check host IOLatency histograms and externally controlled response delays.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_io.sh"
io_test_isolate_run_directory

run_host_latency() {
	set -euo pipefail
	local scenario=${1:-host}

	source "${ROOT_DIR}/integration/lib.sh"
	source "${ROOT_DIR}/integration/config.sh"
	source "${ROOT_DIR}/integration/lib_io.sh"
	source "${ROOT_DIR}/integration/lib_iolatency.sh"
	CURL_TIMEOUT=(--connect-timeout 2 --max-time 3)
	WAIT_HUATUO_BAMAI_TIMEOUT=30
	require_commands fio jq timeout flock

	readonly IOLATENCY_WORK="${HUATUO_BAMAI_TEST_TMPDIR}/iolatency"
	mkdir -p "${IOLATENCY_WORK}"
	readonly IOLATENCY_DELAY_DIR=/sys/bus/pseudo/drivers/scsi_debug
	iolatency_delay_restore_pending=0
	iolatency_original_delay=""
	iolatency_original_ndelay=""
	iolatency_disk_lock=""

	cleanup_iolatency_metrics() {
		local status=$?
		trap - EXIT
		trap '' INT TERM
		set +e
		huatuo_bamai_stop || status=1
		[[ -z ${iolatency_disk_lock} ]] || exec {iolatency_disk_lock}<&-
		iolatency_disk_lock=""
		if ((iolatency_delay_restore_pending)) && ! restore_iolatency_delay; then
			log_error "cannot restore owned scsi_debug delay parameters; inspect ${IOLATENCY_WORK}/delay-resources.txt"
			status=1
		fi
		if ! io_test_cleanup_disks; then
			log_error "cannot reclaim IOLatency disks: ${io_test_disks[*]}; verify scsi_debug owner ${io_test_scsi_owner} before removing it"
			status=1
		fi
		exit "${status}"
	}

	trap cleanup_iolatency_metrics EXIT
	trap 'log_error "IOLatency test interrupted by INT; entering cleanup"; exit 130' INT
	trap 'log_error "IOLatency test interrupted by TERM or execution timeout; entering cleanup"; exit 143' TERM
	if [[ ${scenario} == partition ]]; then
		io_test_prepare_scsi_disks 1 1
		((io_test_scsi_owned)) || skip "partition remap requires an available scsi_debug module"
	else
		io_test_prepare_disks
	fi
	trap 'log_error "IOLatency test interrupted by INT; entering cleanup"; exit 130' INT
	trap 'log_error "IOLatency test interrupted by TERM or execution timeout; entering cleanup"; exit 143' TERM
	iolatency_device="/dev/${io_test_disks[0]}"
	iolatency_disk=${iolatency_device##*/}
	printf 'device=%s\nowned=%s\nwritable=%s\nscsi_debug_owner=%s\n' \
		"${iolatency_device}" "${io_test_scsi_owned}" "${io_test_disk_writable}" "${io_test_scsi_owner}" > "${IOLATENCY_WORK}/resources.txt"

	write_config_bamai_iolatency_metrics() {
		write_config_bamai_sched_tick | sed 's/"iolatency"/"sched_tick"/'
	}

	scrape_iolatency() {
		local label=$1
		iolatency_metrics_file="${IOLATENCY_WORK}/${label}.metrics"
		iolatency_scrape_started=$(date +%s.%N)
		huatuo_bamai_metrics > "${iolatency_metrics_file}" || fatal "IOLatency scrape failed: ${label}"
		iolatency_scrape_ended=$(date +%s.%N)
	}

	assert_iolatency() {
		assert_iolatency_metrics "${iolatency_disk}" "$1" "$2" \
			"${iolatency_metrics_file}" "${iolatency_scrape_started}" "${iolatency_scrape_ended}" "${IOLATENCY_WORK}/last-interval"
	}

	assert_iolatency_active() {
		local operation=$1 label=$2
		assert_iolatency "${operation}" active \
			|| fatal "IOLatency public contract failed after ${label}; inspect ${iolatency_metrics_file}"
	}

	assert_iolatency_boundary() {
		local operation=$1 label=$2 status=0
		scrape_iolatency "${label}"
		assert_iolatency "${operation}" drain || status=$?
		((status == 0 || status == 3)) \
			|| fatal "invalid IOLatency boundary ${label}: expected status 0 or 3, actual=${status}; inspect ${iolatency_metrics_file}.assertion.json"
	}

	assert_iolatency_workload() {
		local operation=$1 size=$2
		jq -e --arg disk "${iolatency_disk}" --arg operation "${operation}" --argjson size "${size}" '
			[.[] | select(.labels.disk == $disk and
				(.name | startswith("huatuo_bamai_iolatency_")) and
				(.name | startswith("huatuo_bamai_iolatency_container_") | not))] as $host |
			all($host[] | select(.labels.operation != $operation); .value == 0) and
			($size != 4096 or all(["queued", "issued"][]; . as $kind |
				[$host[] | select(.labels.operation == $operation and
					.name == ("huatuo_bamai_iolatency_" + $kind + "_io_size_bytes_bucket"))] as $rows |
				[$rows[] | select(.labels.le == "4096") | .value] as $first |
				[$rows[] | select(.labels.le == "+Inf") | .value] as $total |
				($first | length) == 1 and $first == $total and $first[0] > 0))
		' "${iolatency_metrics_file}.json" > /dev/null \
			|| fatal "${operation}/${size}: unexpected operation samples or 4 KiB size classification; inspect ${iolatency_metrics_file}.json"
	}

	assert_iolatency_partition_remap() {
		local target partition="${iolatency_disk}1" original_device=${iolatency_device}
		if ! command -v udevadm > /dev/null 2>&1; then
			log_info "NOT_RUN L05 partition remap: udevadm is unavailable"
			return
		fi
		wait_until 5 0.1 test -b "/dev/${partition}" \
			|| fatal "owned scsi_debug partition did not appear: /dev/${partition}"
		timeout --kill-after=1s 10s udevadm settle --timeout=8 \
			|| fatal "udev did not settle before partition-remap collection"
		for target in whole partition; do
			iolatency_device=${original_device}
			[[ ${target} != partition ]] || iolatency_device="/dev/${partition}"
			assert_iolatency_boundary read "remap-${target}-boundary"
			iolatency_workload read 4096 16
			cp "${IOLATENCY_WORK}/read-4096.json" "${IOLATENCY_WORK}/remap-${target}.fio.json"
			scrape_iolatency "remap-${target}"
			assert_iolatency_active read "L05 ${target} reads"
			assert_iolatency_workload read 4096
			jq -e --arg partition "${partition}" \
				'all(.[]; .labels.disk != $partition)' "${iolatency_metrics_file}.json" > /dev/null \
				|| fatal "partition remap created a separate physical-disk series: ${partition}"
		done
		iolatency_device=${original_device}
		jq -ne --slurpfile whole "${IOLATENCY_WORK}/remap-whole.metrics.assertion.json" \
			--slurpfile partition "${IOLATENCY_WORK}/remap-partition.metrics.assertion.json" '
			all(["queued_io_size_bytes_bucket", "issued_io_size_bytes_bucket", "d2c_seconds_bucket"][];
				. as $name | $whole[0].host_sample_counts.read[$name] as $expected |
				$expected > 0 and $partition[0].host_sample_counts.read[$name] == $expected)
		' > /dev/null || {
			cat "${IOLATENCY_WORK}/remap-"{whole,partition}".metrics.assertion.json" >&2
			fatal "partition remap changed completed-read sample counts relative to whole-disk IO"
		}
		log_info "L05: partition reads retain physical-disk attribution without duplicate queue or completion samples"
	}

	iolatency_workload() {
		local operation=$1 size=$2 count=$3 time_output=${4:-} index=0 flush=0
		local before after output="${IOLATENCY_WORK}/${operation}-${size}"
		read -ra before < "/sys/block/${iolatency_disk}/stat"
		printf 'device=%s operation=%s size=%s before=%s\n' \
			"${iolatency_device}" "${operation}" "${size}" "${before[*]}" > "${output}.diskstats.log"
		if [[ ${operation} == write ]]; then
			((io_test_disk_writable == 1)) || fatal "test disk is not authorized for writes: ${iolatency_device}"
			assert_io_test_disk_identity "${iolatency_disk}" \
				|| fatal "configured test disk identity changed: ${iolatency_disk}"
			index=4
			flush=1
		fi
		timeout --kill-after=2 15 fio --name=iolatency --filename="${iolatency_device}" --rw="${operation}" \
			--bs="${size}" --size="$((size * count))" --number_ios="${count}" --offset=0 \
			--ioengine=sync --direct=1 --iodepth=1 --numjobs=1 --thread=1 --allow_file_create=0 \
			--end_fsync="${flush}" --zero_buffers=1 --eta=never --output-format=json --output="${output}.json" \
			> "${output}.log" 2>&1 || fatal "fio ${operation} failed: ${iolatency_device} size=${size}; inspect ${output}.log and ${output}.json"
		assert_io_fio_workload "${output}.json" "${operation}" "${size}" "${count}"
		read -ra after < "/sys/block/${iolatency_disk}/stat"
		printf 'after=%s read_delta=%s write_delta=%s\n' "${after[*]}" \
			"$((after[0] - before[0]))" "$((after[4] - before[4]))" >> "${output}.diskstats.log"
		assert_io_disk_activity "${operation}" "${iolatency_device}" "${before[index]}" "${after[index]}"
		if [[ -n ${time_output} ]]; then
			# Exclude fio setup time from the externally observed delay comparison.
			jq -e --arg operation "${operation}" '{seconds: (.jobs[0][$operation].runtime / 1000)}' \
				"${output}.json" > "${time_output}" || fatal "invalid fio workload duration: ${output}.json"
		fi
	}

	iolatency_disk_idle() {
		local reads writes
		read -r reads writes < "/sys/block/${iolatency_disk}/inflight" || return 1
		((reads == 0 && writes == 0))
	}

	iolatency_idle_windows_ready() {
		local before after previous_end=0 mode=drain status=0
		iolatency_idle_attempt=$((iolatency_idle_attempt + 1))
		[[ ! -r ${IOLATENCY_WORK}/last-interval ]] || previous_end=$(< "${IOLATENCY_WORK}/last-interval")
		read -r before < "/sys/block/${iolatency_disk}/stat" || fatal "cannot read IOLatency idle disk statistics: ${iolatency_disk}"
		scrape_iolatency "idle-${iolatency_idle_attempt}"
		read -r after < "/sys/block/${iolatency_disk}/stat" || fatal "cannot read IOLatency idle disk statistics after scrape: ${iolatency_disk}"
		printf 'attempt=%s confirmed_windows=%s previous_end=%s\nbefore=%s\nafter=%s\n' \
			"${iolatency_idle_attempt}" "${iolatency_idle_intervals}" "${previous_end}" "${before}" "${after}" \
			> "${IOLATENCY_WORK}/idle-observation.txt"
		if [[ ${before} == "${after}" ]] && iolatency_disk_idle \
			&& awk -v previous="${previous_end}" '
			$1 ~ /^huatuo_bamai_iolatency_interval_end_timestamp_seconds(\{|$)/ && $2 + 0 > previous { fresh = 1 }
			END { exit !fresh }
		' "${iolatency_metrics_file}"; then
			mode=idle
		fi
		assert_iolatency read "${mode}" || status=$?
		((status == 0 || status == 3)) \
			|| fatal "IOLatency ${mode} interval contract failed; inspect ${IOLATENCY_WORK}/idle-observation.txt and ${iolatency_metrics_file}.assertion.json"
		if [[ ${mode} == idle && ${status} == 0 ]]; then iolatency_idle_intervals=$((iolatency_idle_intervals + 1)); fi
		((iolatency_idle_intervals >= 2))
	}

	iolatency_delay_owned() {
		local owner
		((io_test_scsi_owned)) || return 1
		read -r owner < /sys/module/scsi_debug/parameters/inq_vendor || return 1
		[[ ${owner} == "${io_test_scsi_owner}" ]] || return 1
		read -r owner < "/sys/block/${iolatency_disk}/device/vendor" || return 1
		[[ ${owner} == "${io_test_scsi_owner}" ]]
	}

	iolatency_delay_is_set() {
		local parameter=$1 value=$2 actual=unavailable
		if ! iolatency_delay_owned; then
			printf '%s requested=%s state=owner_changed\n' "${parameter}" "${value}"
			return 1
		fi
		if ! iolatency_disk_idle; then
			printf '%s requested=%s state=disk_busy\n' "${parameter}" "${value}"
			return 1
		fi
		# The public interface can reject changes while responses are pending.
		timeout --kill-after=1 1 bash -c 'printf "%s\n" "$2" > "$1"' \
			_ "${IOLATENCY_DELAY_DIR}/${parameter}" "${value}" || return 1
		read -r actual < "${IOLATENCY_DELAY_DIR}/${parameter}" || actual=unavailable
		printf '%s requested=%s actual=%s\n' "${parameter}" "${value}" "${actual}"
		[[ ${actual} == "${value}" ]]
	}

	set_iolatency_delay() {
		wait_until 2 0.1 iolatency_delay_is_set "$1" "$2" >> "${IOLATENCY_WORK}/delay-control.log" 2>&1
	}

	restore_iolatency_delay() {
		local actual_delay actual_ndelay
		((iolatency_delay_restore_pending)) || return 0
		if ((iolatency_original_ndelay > 0)); then
			set_iolatency_delay ndelay "${iolatency_original_ndelay}" || return 1
		else
			set_iolatency_delay ndelay 0 || return 1
			set_iolatency_delay delay "${iolatency_original_delay}" || return 1
		fi
		read -r actual_delay < "${IOLATENCY_DELAY_DIR}/delay" || return 1
		read -r actual_ndelay < "${IOLATENCY_DELAY_DIR}/ndelay" || return 1
		[[ ${actual_delay} == "${iolatency_original_delay}" && ${actual_ndelay} == "${iolatency_original_ndelay}" ]] || return 1
		iolatency_delay_restore_pending=0
		printf 'restored_delay=%s restored_ndelay=%s\n' "${actual_delay}" "${actual_ndelay}" \
			>> "${IOLATENCY_WORK}/delay-resources.txt"
	}

	assert_iolatency_delay_response() {
		local round phase nanoseconds label status confirmed=1
		local -a profiles
		if ((io_test_scsi_owned == 0)); then
			log_info "NOT_RUN L04: response-delay controls require an exclusively owned scsi_debug module"
			return 0
		fi
		# Public driver controls: https://sg.danny.cz/sg/scsi_debug.html
		if [[ ! -r ${IOLATENCY_DELAY_DIR}/ndelay || ! -w ${IOLATENCY_DELAY_DIR}/ndelay ||
			! -r ${IOLATENCY_DELAY_DIR}/delay || ! -w ${IOLATENCY_DELAY_DIR}/delay ]]; then
			log_info "NOT_RUN L04: writable scsi_debug response-delay controls are unavailable"
			return 0
		fi
		iolatency_delay_owned || fatal "scsi_debug ownership changed before the delay comparison"
		read -r iolatency_original_delay < "${IOLATENCY_DELAY_DIR}/delay"
		read -r iolatency_original_ndelay < "${IOLATENCY_DELAY_DIR}/ndelay"
		[[ ${iolatency_original_delay} =~ ^-?[0-9]+$ && ${iolatency_original_ndelay} =~ ^[0-9]+$ ]] \
			|| fatal "invalid public scsi_debug delay values"
		printf 'owner=%s device=%s original_delay=%s original_ndelay=%s\n' \
			"${io_test_scsi_owner}" "${iolatency_device}" "${iolatency_original_delay}" "${iolatency_original_ndelay}" \
			> "${IOLATENCY_WORK}/delay-resources.txt"
		iolatency_delay_restore_pending=1
		for round in 1 2; do
			profiles=()
			for phase in baseline delayed restored; do
				nanoseconds=3000000
				[[ ${phase} != delayed ]] || nanoseconds=30000000
				if ! set_iolatency_delay ndelay "${nanoseconds}"; then
					log_info "NOT_RUN L04: response-delay control could not be applied and read back"
					confirmed=0
					break
				fi
				label="delay-${round}-${phase}"
				scrape_iolatency "${label}-boundary"
				status=0
				assert_iolatency read drain || status=$?
				((status == 0 || status == 3)) || fatal "IOLatency contract failed at a delay-control boundary"
				iolatency_workload read 4096 32 "${IOLATENCY_WORK}/${label}.time.json"
				cp "${IOLATENCY_WORK}/read-4096.log" "${IOLATENCY_WORK}/${label}.log"
				scrape_iolatency "${label}"
				assert_iolatency_active read "L04 ${phase} reads"
				profiles+=("${IOLATENCY_WORK}/${label}")
			done
			((confirmed)) || break
			status=0
			assert_iolatency_delay_comparison "${iolatency_disk}" "${profiles[@]}" \
				"${IOLATENCY_WORK}/delay-${round}-comparison.json" || status=$?
			if ((status == 3)); then
				log_info "NOT_RUN L04: $(< "${IOLATENCY_WORK}/delay-${round}-comparison.json")"
				confirmed=0
				break
			fi
			((status == 0)) || fatal "D2C distribution did not follow the independently confirmed response-delay change"
		done
		restore_iolatency_delay || fatal "cannot restore scsi_debug response delay after L04"
		((confirmed == 0)) || log_info "L04: two externally qualified response-delay rounds shifted and restored the public D2C distribution"
	}

	io_test_start_bamai write_config_bamai_iolatency_metrics \
		--region dev --disable-kubelet --disable-storage --log-debug
	if [[ ${scenario} == host ]]; then
		# Acquire after startup so the daemon cannot keep this phase's lock alive.
		# Udev probes stay outside the configured-disk capture windows.
		exec {iolatency_disk_lock}< "${iolatency_device}"
		flock --exclusive --timeout 5 "${iolatency_disk_lock}" \
			|| fatal "cannot lock owned IOLatency disk ${iolatency_device}"
	fi

	iolatency_warmup_ready() {
		local status=0
		iolatency_warmup_attempt=$((iolatency_warmup_attempt + 1))
		iolatency_workload read 4096 64
		scrape_iolatency "warmup-${iolatency_warmup_attempt}"
		assert_iolatency read warmup || status=$?
		((status != 0)) || return 0
		((status == 3)) || fatal "IOLatency public contract failed during warmup"
		return 1
	}

	assert_iolatency_ready() {
		local iolatency_warmup_attempt=0
		wait_until 30 1 iolatency_warmup_ready \
			|| fatal "completed reads did not produce mandatory IOLatency distributions; inspect warmup metrics and huatuo.log"
	}
	assert_iolatency_ready
	if [[ ${scenario} == partition ]]; then
		assert_iolatency_partition_remap
		exit 0
	fi

	operations=(read write)
	for operation in "${operations[@]}"; do
		for workload in '4096 256' '65536 64' '1048576 16'; do
			read -r size count <<< "${workload}"
			assert_iolatency_boundary "${operation}" "${operation}-${size}-boundary"
			iolatency_workload "${operation}" "${size}" "${count}"
			scrape_iolatency "${operation}-${size}"
			assert_iolatency_active "${operation}" "L01 ${operation}/${size}"
			assert_iolatency_workload "${operation}" "${size}"
		done
	done

	assert_iolatency_boundary read idle-baseline
	iolatency_idle_intervals=0 iolatency_idle_attempt=0
	if wait_until 10 0.1 iolatency_idle_windows_ready; then
		log_info "L02: no old histogram samples repeated in two healthy, independently quiet intervals"
	else
		log_info "NOT_RUN L02 strict idle: two healthy, advancing quiet intervals were not established; last observation: $(< "${IOLATENCY_WORK}/idle-observation.txt")"
		log_info "L02 last interval: $(< "${iolatency_metrics_file}.assertion.json")"
	fi
	iolatency_workload read 4096 64
	scrape_iolatency resumed-read
	assert_iolatency_active read "L02 resumed reads"
	log_info "L02/L03: fixed buckets, public labels, interval timestamps and multiple input sizes verified"
	# Release the configured disk before SCSI preparation drains the udev queue.
	[[ -z ${iolatency_disk_lock} ]] || exec {iolatency_disk_lock}<&-
	iolatency_disk_lock=""
	io_test_prepare_scsi_disks 1 1
	if ((io_test_scsi_owned)); then
		iolatency_device="/dev/${io_test_disks[0]}"
		iolatency_disk=${io_test_disks[0]}
		assert_iolatency_ready
		assert_iolatency_partition_remap
	else
		log_info "NOT_RUN L05 partition remap: no privately owned scsi_debug module"
	fi
	assert_iolatency_delay_response
	exit 0
}

case ${TEST_IOLATENCY_SCOPE:-all} in
all) io_test_run_cases "$0" TEST_IOLATENCY_SCOPE host ;;
host) run_host_latency ;;
partition) run_host_latency partition ;;
*) fatal "TEST_IOLATENCY_SCOPE must be all, host or partition" ;;
esac
