#!/usr/bin/env bash

# Copyright 2026 The HuaTuo Authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Exercise real cgroup taskstats, proc stacks and both tracing storage backends.
set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"
source "${ROOT_DIR}/integration/lib_cgroup.sh"
source "${ROOT_DIR}/integration/lib_storage.sh"
source "${ROOT_DIR}/e2e/lib.sh"
source "${ROOT_DIR}/e2e/lib_cpu_load.sh"

readonly CURL_TIMEOUT=(--connect-timeout 2 --max-time 5)
readonly DLOAD_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/dload"
readonly DLOAD_BIN="${DLOAD_DIR}/reference"
readonly DLOAD_NS="${BUSINESS_POD_NS}"
readonly DLOAD_PREFIX="dload-${BASHPID}-${RANDOM}"
readonly DLOAD_LABEL="app=${DLOAD_PREFIX}"
readonly DLOAD_INDEX="huatuo_e2e_${DLOAD_PREFIX}"
readonly DLOAD_DM="huatuo-${DLOAD_PREFIX}"
readonly DLOAD_SECTORS=65536
DLOAD_INTERVAL=1
DLOAD_THRESHOLD=1
DLOAD_COOLDOWN=2
DLOAD_DEBUG=false
DLOAD_DISABLED=false
DLOAD_ISSUES='[]'
dload_loop=""
dload_device_dir=""
dload_mapper_created=false
dload_mounted=false
dload_module_loaded=false

require_commands kubectl jq ss timeout findmnt
require_readable "${KUBELET_CERT}" "${KUBELET_KEY}"
[[ ${EUID} -eq 0 ]] || skip "requires root for taskstats and test block devices"
mkdir -p "${DLOAD_DIR}"
chmod 755 "${DLOAD_DIR}"
uname -a > "${DLOAD_DIR}/kernel.txt"
"${HUATUO_BAMAI_BIN}" --version > "${DLOAD_DIR}/bamai-version.txt"

dload_delay() {
	# Suspend flushes pending delayed reads before replacing the test-only table.
	log_info "dload device: read_delay_ms=$1"
	timeout 15 dmsetup --noudevsync suspend --noflush --nolockfs "${DLOAD_DM}" \
		|| {
			log_error "cannot suspend test mapper ${DLOAD_DM}"
			return 1
		}
	timeout 15 dmsetup --noudevsync reload "${DLOAD_DM}" \
		--table "0 ${DLOAD_SECTORS} delay ${dload_loop} 0 $1 ${dload_loop} 0 0" \
		|| {
			log_error "cannot reload test mapper ${DLOAD_DM}"
			return 1
		}
	timeout 15 dmsetup --noudevsync resume "${DLOAD_DM}" \
		|| {
			log_error "cannot resume test mapper ${DLOAD_DM}"
			return 1
		}
}

dload_remove_mapper() {
	# Pod deletion can precede kubelet's release of the filesystem mount.
	timeout 3 dmsetup --noudevsync remove "${DLOAD_DM}" > "${DLOAD_DIR}/dm-remove.log" 2>&1
}

dload_cleanup() {
	local status=$?
	trap - EXIT
	if [[ ${dload_mapper_created} == true ]]; then
		dload_delay 0 || status=1
	fi
	kubectl --request-timeout=30s delete pod -n "${DLOAD_NS}" -l "${DLOAD_LABEL}" \
		--ignore-not-found --timeout=30s || status=1
	huatuo_bamai_stop || status=1
	storage_dump_logs || status=1
	storage_stop || status=1
	if [[ ${dload_mounted} == true ]]; then
		nsenter --mount=/proc/1/ns/mnt -- timeout 15 umount "${dload_device_dir}/disk" || status=1
	fi
	if [[ ${dload_mapper_created} == true ]]; then
		if wait_until 30 1 dload_remove_mapper; then
			dload_mapper_created=false
		else
			log_error "test mapper ${DLOAD_DM} remains busy; inspect ${DLOAD_DIR}/dm-remove.log and ${dload_device_dir}/disk"
			status=1
		fi
	fi
	if [[ -n ${dload_loop} && ${dload_mapper_created} == false ]]; then
		losetup --detach "${dload_loop}" || status=1
	fi
	if [[ ${dload_module_loaded} == true && ${dload_mapper_created} == false ]]; then
		modprobe -r dm_delay || status=1
	fi
	log_info "dload artifacts: ${DLOAD_DIR}; cleanup status=${status}"
	exit "${status}"
}
trap dload_cleanup EXIT

if [[ -n ${DLOAD_HELPER_BIN:-} ]]; then
	require_commands "${DLOAD_HELPER_BIN}"
	cp "${DLOAD_HELPER_BIN}" "${DLOAD_BIN}"
else
	require_commands go
	CGO_ENABLED=0 go build -mod=vendor -o "${DLOAD_BIN}" "${ROOT_DIR}/e2e/testdata/test_autotracing_dload.go" \
		"${ROOT_DIR}/e2e/testdata/metrics_helpers.go"
fi
DLOAD_MODE=$("${DLOAD_BIN}" mode)
DLOAD_FIXTURE_ISSUES=$("${DLOAD_BIN}" issues)
DLOAD_PORT=$(allocate_available_port) || fatal "cannot allocate dload metrics port"
DLOAD_KUBELET_PORT=${KUBELET_PODS_API##*:}
DLOAD_KUBELET_PORT=${DLOAD_KUBELET_PORT%%/*}
readonly DLOAD_MODE DLOAD_FIXTURE_ISSUES DLOAD_PORT DLOAD_KUBELET_PORT

if [[ -f ${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid ]]; then
	huatuo_bamai_stop
	huatuo_bamai_log_check || fatal "runner baseline daemon has unexpected errors"
	cp "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${DLOAD_DIR}/baseline.log"
fi
HUATUO_BAMAI_ADDR="http://127.0.0.1:${DLOAD_PORT}"
HUATUO_BAMAI_METRICS_API="${HUATUO_BAMAI_ADDR}/metrics"

dload_status() {
	local args=()
	[[ ${DLOAD_DISABLED} != true ]] || args+=(--disabled)
	"${DLOAD_BIN}" status --url "${HUATUO_BAMAI_METRICS_API}" "${args[@]}"
}

dload_concurrent_status() {
	local query pid status=0 pids=()
	for query in 1 2 3; do
		(
			for _ in 1 2 3; do dload_status; done
		) > "${dload_work}/metrics-query-${query}.log" 2>&1 &
		pids+=("$!")
	done
	for pid in "${pids[@]}"; do wait "${pid}" || status=1; done
	[[ ${status} -eq 0 ]] || fatal "concurrent metrics queries failed; inspect metrics-query logs"
	huatuo_bamai_metrics > "${dload_work}/metrics.prom"
}

dload_log_check() {
	[[ -r ${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log ]] \
		|| fatal "dload daemon log is not readable: ${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log"
	HUATUO_BAMAI_MATCH_KEYWORDS="${HUATUO_BAMAI_MATCH_KEYWORDS}|\"level\":\"(error|panic|fatal)\"" \
		huatuo_bamai_log_check || fatal "dload daemon logged an unexpected failure"
	# Kernel creation hints can precede kubelet metadata; discovery is awaited.
	# CSS sample loss requests a full refresh; discovery and dload data are checked.
	! grep -iE 'level="?warn|"level":"warn"' "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
		| grep -v 'creation is not yet reflected by kubelet' \
		| grep -vE 'msg="lost BPF perf event samples".*func="internal/pod\.cgroupCssEventSyncHandler\.func1"' \
		| grep -q . \
		|| fatal "dload daemon logged an unexpected warning"
}

dload_start() {
	integration_huatuo_bamai_start write_dload_autotracing_config --region e2e
	wait_until 10 0.5 dload_status || fatal "dload lifecycle metrics are incorrect"
}

dload_stop() {
	local pid
	pid=$(< "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")
	huatuo_bamai_stop
	wait "${pid}" || fatal "bamai did not exit successfully and flush storage"
	dload_log_check
}

# Constructor validation must fail before any Pod or IO workload is created.
dload_invalid_config() (
	local name=$1 expected=$2 status=0
	HUATUO_BAMAI_TEST_TMPDIR="${DLOAD_DIR}/invalid-${name}"
	mkdir -p "${HUATUO_BAMAI_TEST_TMPDIR}"
	case ${name} in
	interval) DLOAD_INTERVAL=-1 ;;
	threshold) DLOAD_THRESHOLD=-1 ;;
	cooldown) DLOAD_COOLDOWN=-1 ;;
	esac
	write_dload_autotracing_config
	timeout 15 "${HUATUO_BAMAI_BIN}" --region e2e --disable-kubelet \
		--config-dir "${HUATUO_BAMAI_TEST_TMPDIR}" --config bamai.conf \
		> "${HUATUO_BAMAI_TEST_TMPDIR}/expected-error.log" 2>&1 || status=$?
	((status != 0 && status != 124 && status != 137)) || fatal "${name}: invalid config did not fail promptly"
	grep -Fq "${expected}" "${HUATUO_BAMAI_TEST_TMPDIR}/expected-error.log" \
		|| fatal "${name}: missing expected configuration error: ${expected}"
	log_info "dload: rejected negative ${name}"
)

dload_invalid_config interval 'dload sampling interval must be positive'
dload_invalid_config threshold 'dload threshold must be non-negative'
dload_invalid_config cooldown 'dload tracing interval must be non-negative'

if [[ ${DLOAD_MODE} != legacy ]]; then
	HUATUO_BAMAI_TEST_TMPDIR="${DLOAD_DIR}/unsupported"
	mkdir -p "${HUATUO_BAMAI_TEST_TMPDIR}"
	dload_start
	sleep 2
	grep -Fq 'skipping dload tracing because cgroup v2 lacks load statistics' \
		"${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" || fatal "${DLOAD_MODE}: missing unsupported-mode log"
	[[ ! -s ${HUATUO_BAMAI_TEST_TMPDIR}/events/dload ]] || fatal "${DLOAD_MODE}: unsupported mode saved dload events"
	dload_stop
	skip "dload unsupported-mode behavior verified; functional/ES cases require Legacy cgroup (actual=${DLOAD_MODE})"
fi

require_commands docker crictl nsenter dmsetup losetup mkfs.ext4 modprobe truncate mount umount
docker info > "${DLOAD_DIR}/docker-info.log" 2>&1 || skip "Docker is unavailable for the ES upload test"
if [[ ! -d /sys/module/dm_delay ]]; then
	modprobe dm_delay > "${DLOAD_DIR}/dm-delay-module.log" 2>&1 || skip "dm-delay target is unavailable"
	dload_module_loaded=true
fi
dmsetup targets > "${DLOAD_DIR}/dm-targets.txt"
grep -q '^delay ' "${DLOAD_DIR}/dm-targets.txt" || skip "device-mapper delay target is unavailable"

cpu_load_prepare_image
DLOAD_CPU_ROOT=$(findmnt -rn -t cgroup -O cpu -o TARGET) || skip "v1 CPU cgroup controller is not mounted"
DLOAD_NODE=$(kubelet_pods_json | jq -er '[.items[].spec.nodeName] | unique | if length == 1 then .[0] else error("cannot identify local node") end')
readonly DLOAD_CPU_ROOT DLOAD_NODE

dload_device_dir=$(mktemp -d "$(cd "${ROOT_DIR}/_output" && pwd -P)/dload-e2e.XXXXXX")
mkdir "${dload_device_dir}/disk"
truncate -s 32M "${dload_device_dir}/disk.img"
dload_loop=$(losetup --find --show "${dload_device_dir}/disk.img") || skip "no usable loop device"
dmsetup --noudevsync create "${DLOAD_DM}" --table "0 ${DLOAD_SECTORS} delay ${dload_loop} 0 0 ${dload_loop} 0 0"
dload_mapper_created=true
dmsetup mknodes "${DLOAD_DM}"
mkfs.ext4 -q -F "/dev/mapper/${DLOAD_DM}"
# The runner has a private mount namespace; kubelet must see the same filesystem.
nsenter --mount=/proc/1/ns/mnt -- mount -t ext4 -o nosuid,nodev,noatime \
	"/dev/mapper/${DLOAD_DM}" "${dload_device_dir}/disk"
dload_mounted=true
printf 'mapper=%s loop=%s mount=%s cgroup_mode=%s\n' "${DLOAD_DM}" "${dload_loop}" \
	"${dload_device_dir}/disk" "${DLOAD_MODE}" > "${DLOAD_DIR}/device.txt"

storage_start elasticsearch_v8
storage_curl --fail -X PUT "${STORAGE_ADDR}/${DLOAD_INDEX}" -H 'Content-Type: application/json' \
	--data '{"settings":{"number_of_shards":1,"number_of_replicas":0}}' \
	> "${DLOAD_DIR}/index-created.json"
jq -e '.acknowledged == true' "${DLOAD_DIR}/index-created.json" > /dev/null || fatal "ES did not create the test index"

dload_pod_ready() {
	kubectl --request-timeout=5s logs -n "${DLOAD_NS}" "${dload_pod}" \
		| grep -Fxq 'ready'
}

dload_create_pod() {
	local suffix=$1
	dload_pod="${DLOAD_PREFIX}-${suffix}"
	dload_work="${HUATUO_BAMAI_TEST_TMPDIR}/${suffix}"
	mkdir -p "${dload_work}"
	chmod 755 "${HUATUO_BAMAI_TEST_TMPDIR}" "${dload_work}"
	jq -n --arg name "${dload_pod}" --arg ns "${DLOAD_NS}" --arg app "${DLOAD_PREFIX}" \
		--arg node "${DLOAD_NODE}" --arg image "${CPU_LOAD_IMAGE}" --arg work "${dload_work}" \
		--arg disk "${dload_device_dir}/disk" '
		{apiVersion: "v1", kind: "Pod", metadata: {name: $name, namespace: $ns, labels: {app: $app}},
		 spec: {nodeName: $node, restartPolicy: "Never", terminationGracePeriodSeconds: 2,
		   containers: [{name: "worker", image: $image, imagePullPolicy: "Never",
		     command: ["sh", "-ec", "
		       trap \"exit 0\" TERM INT
		       stress-ng --version
		       echo ready
		       while [ ! -e /work/go ]; do sleep 0.1; done
		       stress-ng --hdd 4 --hdd-bytes 4M --hdd-write-size 4K --hdd-opts direct \\
		         --temp-path /delayed --timeout 120s --abort --metrics-brief &
		       pid=$!
		       trap \"kill -TERM $pid; wait $pid; exit 0\" TERM INT
		       while [ ! -e /work/stop ]; do
		         kill -0 \"$pid\" || { wait \"$pid\"; echo \"stress-ng exited before stop\" >&2; exit 1; }
		         sleep 0.1
		       done
		       kill -TERM \"$pid\"
		       wait \"$pid\"
		       trap \"exit 0\" TERM INT
		       echo complete
		       sleep 600
		     "],
		     resources: {requests: {cpu: "100m", memory: "16Mi"}, limits: {cpu: "500m", memory: "64Mi"}},
		     volumeMounts: [{name: "work", mountPath: "/work", readOnly: true},
		       {name: "disk", mountPath: "/delayed"}]}],
		   volumes: [{name: "work", hostPath: {path: $work, type: "Directory"}},
		     {name: "disk", hostPath: {path: $disk, type: "Directory"}}]}}
	' > "${dload_work}/pod.json"
	kubectl --request-timeout=20s create -f "${dload_work}/pod.json"
	kubectl wait --for=condition=Ready pod -n "${DLOAD_NS}" "${dload_pod}" --timeout=90s
	wait_until 15 0.5 dload_pod_ready || fatal "stress-ng controller did not initialize in ${dload_pod}"
	assert_huatuo_bamai_containers_present "${DLOAD_NS}" "^${dload_pod}$" 1
	dload_id=$(kubelet_container_ids "${DLOAD_NS}" "^${dload_pod}$")
	dload_cgroup=$(cgroup_find_container "${DLOAD_CPU_ROOT}" "${dload_id}") || fatal "cannot find Pod CPU cgroup"
	printf 'pod=%s id=%s cgroup=%s\n' "${dload_pod}" "${dload_id}" "${dload_cgroup}" \
		| tee "${dload_work}/target.txt"
}

dload_snapshot() {
	"${DLOAD_BIN}" sample --cgroup "${dload_cgroup}" > "$1"
}

dload_local_events() {
	if [[ -s ${HUATUO_BAMAI_TEST_TMPDIR}/events/dload ]]; then
		jq -s --arg id "${dload_id}" --argjson debug "${DLOAD_DEBUG}" \
			'[.[] | select($debug or .container_id == $id)]' \
			"${HUATUO_BAMAI_TEST_TMPDIR}/events/dload"
	else
		printf '[]\n'
	fi
}

dload_local_ready() {
	dload_local_events > "${dload_work}/local.json" || return 1
	jq -e --argjson count "${1:-1}" 'length >= $count' "${dload_work}/local.json" > /dev/null
}

dload_debug_ready() {
	dload_local_events > "${dload_work}/local.json" || return 1
	"${DLOAD_BIN}" debug --cooldown "${DLOAD_COOLDOWN}s" < "${dload_work}/local.json" \
		2> "${dload_work}/debug-check.err"
}

dload_remote_events() {
	local query
	if [[ ${DLOAD_DEBUG} == true ]]; then
		# Other phases share the index; only this instance's local IDs belong here.
		query=$(dload_local_events | jq '
			{size: 1000, query: {bool: {filter: [{term: {"tracer_name.keyword": "dload"}}, {ids: {values: map(.tracer_id)}}]}}}')
	else
		query=$(jq -n --arg id "${dload_id}" \
			'{size: 1000, query: {bool: {filter: [{term: {"tracer_name.keyword": "dload"}}, {term: {"container_id.keyword": $id}}]}}}')
	fi
	storage_curl --fail -X POST "${STORAGE_ADDR}/${DLOAD_INDEX}/_search" \
		-H 'Content-Type: application/json' --data "${query}" \
		> "${dload_work}/search.json" 2> "${dload_work}/search.err" || return 1
	jq -e '._shards.failed == 0 and .timed_out == false and .hits.total.relation == "eq"
		and .hits.total.value == (.hits.hits | length)
		and all(.hits.hits[]; ._id == ._source.tracer_id)' "${dload_work}/search.json" > /dev/null || return 1
	jq '[.hits.hits[]._source]' "${dload_work}/search.json" > "${dload_work}/remote.json"
}

dload_remote_ready() {
	dload_remote_events || return 1
	jq -e 'length > 0' "${dload_work}/remote.json" > /dev/null
}

dload_log_events() {
	local phase=$1 line
	jq -r --arg phase "${phase}" '
		sort_by(.started_timestamp)[] |
		"dload event: phase=\($phase) pod=\(.container_hostname) tracer_id=\(.tracer_id) " +
		"started_at=\(.started_timestamp) nr_uninterruptible=\(.tracer_data.nr_uninterruptible) " +
		"dload_avg=\(.tracer_data.dload_avg) threshold=\(.tracer_data.threshold) " +
		"known_issue=\(.tracer_data.known_issue)"
	' "${dload_work}/local.json" | while IFS= read -r line; do
		log_info "${line}"
	done
}

dload_log_reference() {
	log_info "$(jq -r --arg phase "$1" --arg pod "${dload_pod}" '
		"dload reference: phase=\($phase) pod=\($pod) " +
		"nr_uninterruptible=\(.stats.nr_uninterruptible) " +
		"proc_D=\([.tasks[] | select(.state == "D")] | length)"
	' "${dload_work}/blocked.json")"
}

dload_compare_storage() {
	local phase=$1
	dload_local_events > "${dload_work}/local.json"
	dload_log_events "${phase}"
	storage_curl --fail -X POST "${STORAGE_ADDR}/${DLOAD_INDEX}/_refresh" > "${dload_work}/refresh.json"
	jq -e '._shards.failed == 0' "${dload_work}/refresh.json" > /dev/null || fatal "ES refresh failed"
	dload_remote_events || fatal "cannot query dload events from ES; inspect search.json/search.err"
	jq -e --slurpfile remote "${dload_work}/remote.json" '
		(length == (map(.tracer_id) | unique | length))
		and (sort_by(.tracer_id) == ($remote[0] | sort_by(.tracer_id)))
	' "${dload_work}/local.json" > /dev/null || fatal "local/ES dload event IDs or contents differ"
	log_info "dload storage: phase=${phase} pods=$(jq -c 'map(.container_hostname) | unique' "${dload_work}/local.json") local=$(jq length "${dload_work}/local.json") ES=$(jq length "${dload_work}/remote.json")"
}

dload_assert_events() {
	local issue=$1
	jq -e --arg id "${dload_id}" --arg pod "${dload_pod}" --arg ns "${DLOAD_NS}" \
		--arg host "$(hostname)" --arg issue "${issue}" --argjson threshold "${DLOAD_THRESHOLD}" \
		--argjson debug "${DLOAD_DEBUG}" '
		def count: type == "number" and . >= 0 and floor == .;
		def text: type == "string" and length > 0;
		def timestamp: type == "string" and test("^[0-9]{4}-.*Z$");
		length > 0 and all(.[];
			.tracer_name == "dload" and .tracer_type == "autotracing"
			and (if $debug then
				[.container_id, .container_hostname, .container_host_namespace] | all(.[]; text)
			else .container_id == $id and .container_hostname == $pod and .container_host_namespace == $ns end)
			and .hostname == $host and .region == "e2e"
			and (.tracer_id | type == "string" and length > 0)
			and (.container_type | type == "string" and length > 0)
			and (.container_qos | type == "string" and length > 0)
			and (.started_timestamp | timestamp) and (.uploaded_timestamp | timestamp)
			and .uploaded_timestamp >= .started_timestamp
			and (.tracer_data |
				.threshold == $threshold
				and (if $debug then .known_issue | text else .known_issue == $issue end)
				and ([.nr_sleeping, .nr_running, .nr_stopped, .nr_uninterruptible, .nr_iowait] | all(.[]; count))
				and (.load_avg | type == "number" and . >= 0)
				and (.dload_avg | type == "number" and . >= 0)
				and (if $debug then true else .dload_avg > $threshold and .nr_uninterruptible > 0 end)
				and (.stack | if $debug then type == "string"
					else contains("stacktrace of D task in cgroup:") and contains("Comm: stress-ng-hdd") end)))
	' "${dload_work}/local.json" > /dev/null || fatal "invalid dload event metadata or data; inspect local.json"
	if [[ ${DLOAD_DEBUG} != true ]]; then
		jq -e --slurpfile blocked "${dload_work}/blocked.json" '
			any(.[]; .tracer_data.stack | split("stacktrace of D task in host:") |
				length == 2 and (.[1] as $host |
					any($blocked[0].tasks[] | select(.state == "D");
						.tid as $tid | $host | contains("Comm: stress-ng-hdd\tPid: \($tid)\n"))))
		' "${dload_work}/local.json" > /dev/null || fatal "no stable event captured a stress-ng worker in host stacks"
	fi
}

dload_blocked() {
	dload_snapshot "${dload_work}/blocked.json" || return 1
	jq -e '.stats.nr_uninterruptible == 4
		and ([.tasks[] | select(.state == "D")] | length == 4 and all(.[]; .stack | length > 0))' \
		"${dload_work}/blocked.json" > /dev/null
}

dload_complete() {
	kubectl --request-timeout=5s logs -n "${DLOAD_NS}" "${dload_pod}" > "${dload_work}/pod.log" || return 1
	grep -Fxq 'complete' "${dload_work}/pod.log"
}

dload_workers_gone() {
	local tid
	while read -r tid; do
		[[ ! -d /proc/${tid} ]] || return 1
	done < <(jq -r '.tasks[] | select(.state == "D") | .tid' "${dload_work}/blocked.json")
}

dload_reference_stop() {
	if [[ -n ${dload_reference_pid} ]]; then
		kill -TERM "${dload_reference_pid}" 2> /dev/null || true
		wait "${dload_reference_pid}" 2> /dev/null || true
		dload_reference_pid=""
	fi
}

dload_case_cleanup() {
	local status=$?
	trap - EXIT
	dload_delay 0 || status=1
	dload_reference_stop
	kubectl --request-timeout=30s delete pod -n "${DLOAD_NS}" -l "${DLOAD_LABEL}" \
		--ignore-not-found --timeout=30s || status=1
	huatuo_bamai_stop || status=1
	if [[ ${status} -eq 0 ]]; then
		dload_log_check
	else
		kubectl --request-timeout=10s describe pod -n "${DLOAD_NS}" -l "${DLOAD_LABEL}" \
			> "${HUATUO_BAMAI_TEST_TMPDIR}/pods-failure.txt" 2>&1 || true
	fi
	exit "${status}"
}

dload_new_remote_event() {
	dload_remote_events || return 1
	jq -e --slurpfile before "${dload_work}/before-restart.json" '
		map(.tracer_id) - ($before[0] | map(.tracer_id)) | length > 0
	' "${dload_work}/remote.json" > /dev/null
}

dload_run_case() (
	local name=$1 issue=none pid started_ms old_id
	HUATUO_BAMAI_TEST_TMPDIR="${DLOAD_DIR}/${name}"
	mkdir -p "${HUATUO_BAMAI_TEST_TMPDIR}"
	dload_reference_pid=""
	trap dload_case_cleanup EXIT
	DLOAD_THRESHOLD=1 DLOAD_COOLDOWN=2 DLOAD_DEBUG=false DLOAD_DISABLED=false DLOAD_ISSUES='[]'
	case ${name} in
	high-threshold) DLOAD_THRESHOLD=8 ;;
	cooldown | restart | kill-recovery) DLOAD_COOLDOWN=60 ;;
	debug)
		DLOAD_THRESHOLD=8 DLOAD_COOLDOWN=60 DLOAD_DEBUG=true
		DLOAD_ISSUES=${DLOAD_FIXTURE_ISSUES}
		;;
	classified)
		DLOAD_THRESHOLD=0 DLOAD_COOLDOWN=0
		DLOAD_ISSUES=${DLOAD_FIXTURE_ISSUES}
		issue=e2e-dload
		;;
	disabled) DLOAD_DISABLED=true ;;
	esac
	log_info "dload phase=${name} threshold=${DLOAD_THRESHOLD} cooldown=${DLOAD_COOLDOWN} debug=${DLOAD_DEBUG}"
	dload_start
	dload_create_pod "${name}"
	dload_snapshot "${dload_work}/baseline.json"
	jq -e '.stats.nr_uninterruptible == 0 and (.tasks | length > 0)
		and all(.tasks[]; .state != "D")' "${dload_work}/baseline.json" > /dev/null \
		|| fatal "baseline container is not idle or has no tasks"
	if [[ ${name} == debug ]]; then
		wait_until 30 0.5 dload_local_ready || fatal "debug mode did not save any container event"
		wait_until 30 0.5 dload_remote_ready || fatal "debug events were not uploaded while bamai was running"
		wait_until 20 0.5 dload_debug_ready \
			|| fatal "debug mode did not bypass threshold/cooldown; inspect ${dload_work}/debug-check.err"
		dload_snapshot "${dload_work}/idle-after.json"
		jq -e '.stats.nr_uninterruptible == 0' "${dload_work}/idle-after.json" > /dev/null || fatal "debug test unexpectedly generated D tasks"
		dload_concurrent_status
		dload_status
		dload_stop
		dload_compare_storage "${name}"
		dload_assert_events none
		dload_debug_ready || fatal "saved debug events did not bypass threshold/cooldown; inspect ${dload_work}/debug-check.err"
		return
	fi
	sleep 2
	dload_local_events > "${dload_work}/baseline-events.json"
	jq -e 'length == 0' "${dload_work}/baseline-events.json" > /dev/null || fatal "idle container unexpectedly triggered dload"
	dload_remote_events || fatal "cannot query baseline events from ES"
	jq -e 'length == 0' "${dload_work}/remote.json" > /dev/null || fatal "idle container has ES events"
	timeout 25 "${DLOAD_BIN}" sample --cgroup "${dload_cgroup}" --duration 18s \
		> "${dload_work}/samples.jsonl" 2> "${dload_work}/reference.err" &
	dload_reference_pid=$!
	dload_delay 1000
	dmsetup table "${DLOAD_DM}" > "${dload_work}/dm-table.txt"
	date -u '+%Y-%m-%dT%H:%M:%S.%NZ' > "${dload_work}/triggered-at.txt"
	touch "${dload_work}/go"
	wait_until 8 0.5 dload_blocked || fatal "direct IO did not produce four netlink-counted D tasks"
	dload_log_reference "${name}"
	if [[ ${name} != high-threshold && ${name} != disabled ]]; then
		wait_until 20 0.5 dload_local_ready || fatal "D tasks did not trigger dload"
		wait_until 30 0.5 dload_remote_ready || fatal "dload events were not uploaded while bamai was running"
	fi
	dload_concurrent_status
	if [[ ${name} == restart || ${name} == kill-recovery ]]; then
		cp "${dload_work}/remote.json" "${dload_work}/before-restart.json"
		pid=$(< "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")
		if [[ ${name} == kill-recovery ]]; then
			kill -KILL "${pid}"
			if wait "${pid}"; then fatal "SIGKILL did not terminate bamai"; else
				[[ $? -eq 137 ]] || fatal "bamai did not exit from SIGKILL"
			fi
			huatuo_bamai_stop
			dload_log_check
		else
			dload_stop
		fi
		cp "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${HUATUO_BAMAI_TEST_TMPDIR}/before-restart.log"
		dload_start
		wait_until 20 0.5 dload_new_remote_event || fatal "restarted bamai did not resume dload uploads"
		jq -e --slurpfile before "${dload_work}/before-restart.json" \
			'($before[0] | map(.tracer_id)) - map(.tracer_id) | length == 0' \
			"${dload_work}/remote.json" > /dev/null || fatal "restart lost previously uploaded events"
	fi
	if [[ ${name} == pod-lifecycle ]]; then
		dload_reference_stop
		dload_blocked || fatal "stress-ng workers were no longer blocked before Pod deletion"
		old_id=${dload_id}
		cp "${dload_work}/remote.json" "${dload_work}/before-delete.json"
		started_ms=$(date +%s%3N)
		kubectl --request-timeout=30s delete pod -n "${DLOAD_NS}" "${dload_pod}" --timeout=20s \
			> "${dload_work}/delete.log" 2>&1
		printf 'deletion_elapsed_ms=%s\n' "$(($(date +%s%3N) - started_ms))" | tee "${dload_work}/deletion.txt"
		dload_workers_gone || fatal "stress-ng worker survived Pod deletion"
		assert_huatuo_bamai_containers_absent 'deleted dload container' "${old_id}"
		dload_delay 0
		dload_compare_storage "${name}"
		dload_assert_events none
		dload_create_pod replacement
		[[ ${dload_id} != "${old_id}" ]] || fatal "replacement Pod reused the old container ID"
		dload_delay 1000
		touch "${dload_work}/go"
		wait_until 8 0.5 dload_blocked || fatal "replacement stress-ng workers did not block"
		dload_log_reference "${name}"
		wait_until 20 0.5 dload_local_ready || fatal "replacement container did not trigger dload"
		wait_until 30 0.5 dload_remote_ready || fatal "replacement event did not reach ES"
	fi
	if [[ -n ${dload_reference_pid} ]]; then
		wait "${dload_reference_pid}" || fatal "reference sampling failed; inspect reference.err"
		dload_reference_pid=""
		jq -se 'any(.[]; .stats.nr_uninterruptible == 4 and ([.tasks[] | select(.state == "D")] | length == 4))' \
			"${dload_work}/samples.jsonl" > /dev/null || fatal "reference never observed four D tasks"
	fi
	touch "${dload_work}/stop"
	wait_until 10 0.5 dload_complete || fatal "stress-ng workload did not stop successfully; inspect pod.log"
	dload_workers_gone || fatal "stress-ng worker remains after workload completion"
	dload_delay 0
	dload_status
	dload_stop
	dload_compare_storage "${name}"
	case ${name} in
	high-threshold | disabled)
		jq -e 'length == 0' "${dload_work}/local.json" > /dev/null || fatal "${name} saved unexpected events"
		;;
	cooldown)
		jq -e 'length == 1' "${dload_work}/local.json" > /dev/null || fatal "cooldown did not restrict tracing to one event"
		dload_assert_events "${issue}"
		;;
	*) dload_assert_events "${issue}" ;;
	esac
)

for dload_case in positive high-threshold cooldown debug classified disabled restart kill-recovery pod-lifecycle; do
	dload_run_case "${dload_case}"
	log_info "PASS: dload ${dload_case}"
done
