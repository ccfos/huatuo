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

# Verify one batch of containers establishes and releases bamai memory watches.
set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_cgroup.sh"
source "${ROOT_DIR}/e2e/lib.sh"

# Bash cannot export env.sh's array into the runner's test subprocess.
readonly CURL_TIMEOUT=(--connect-timeout 2 --max-time 3)

readonly CONTAINER_BATCH_COUNT=50
readonly CONTAINER_BATCH_NAME="container-batch-$(date +%s)-$$"
readonly CONTAINER_BATCH_LABEL="app=${CONTAINER_BATCH_NAME}"
readonly CONTAINER_BATCH_REGEX="^${CONTAINER_BATCH_NAME}-[0-9]+$"
readonly CONTAINER_FD_SAMPLES=5
readonly CONTAINER_FD_LOG="${HUATUO_BAMAI_TEST_TMPDIR}/container-fds.tsv"
readonly CONTAINER_WATCH_TARGETS="${HUATUO_BAMAI_TEST_TMPDIR}/container-watch-targets.tsv"

container_batch_ids=()
container_node=""
container_memory_root=""
container_bamai_pid=""
container_bamai_start_time_ticks=""
container_fd_baseline=0
container_fd_baseline_samples=0
container_fd_count=0
container_fd_stable=0

container_create_pods() {
	local name=$1 label=$2 count=$3
	local manifest="${HUATUO_BAMAI_TEST_TMPDIR}/${name}.json"

	# Submit the batch together on the node observed by the local kubelet.
	jq -n --arg ns "$BUSINESS_POD_NS" --arg name "$name" --arg image "$BUSINESS_POD_IMAGE" \
		--arg key "${label%%=*}" --arg value "${label#*=}" \
		--arg node "$container_node" --argjson count "$count" '
		{apiVersion: "v1", kind: "List", items: [range(1; $count + 1) |
			{apiVersion: "v1", kind: "Pod",
			 metadata: {namespace: $ns, name: ($name + "-" + tostring), labels: {($key): $value}},
			 spec: {restartPolicy: "Never", terminationGracePeriodSeconds: 1, nodeName: $node,
				containers: [{name: "busybox", image: $image, imagePullPolicy: "Never",
					resources: {requests: {memory: "1Mi"}, limits: {memory: "128Mi"}},
					command: ["sleep", "infinity"]}]}}]}
	' > "$manifest"
	timeout 120 kubectl --request-timeout=30s create -f "$manifest"
}

container_fetch_pods() {
	local response_file=$1 http_status curl_status=0
	# Preserve HTTP failures without accepting them as completed cleanup.
	: > "$response_file"
	http_status=$(curl -sSk "${CURL_TIMEOUT[@]}" \
		--cert "$KUBELET_CERT" --key "$KUBELET_KEY" \
		-o "$response_file" -w '%{http_code}' "$KUBELET_PODS_API" \
		2> "${response_file}.err") || curl_status=$?
	printf 'curl_exit=%s\nhttp_status=%s\n' "$curl_status" "$http_status" \
		> "${response_file}.status"
	((curl_status == 0)) && [[ "$http_status" == "200" ]]
}

container_read_bamai_start_time_ticks() {
	# Drop comm before reading starttime; comm can contain spaces.
	awk '{ sub(/^.*\) /, ""); print $20 }' "/proc/${container_bamai_pid}/stat"
}

container_sample_fds() {
	local phase=$1 current_start_time_ticks
	current_start_time_ticks=$(container_read_bamai_start_time_ticks) || fatal "could not read bamai process identity"
	assert_eq "$current_start_time_ticks" "$container_bamai_start_time_ticks" "bamai process starttime" \
		|| fatal "bamai process changed during batch container lifecycle"
	local -a fds=(/proc/"${container_bamai_pid}"/fd/*)
	[[ -d "/proc/${container_bamai_pid}/fd" && ${fds[0]} != *'*'* ]] \
		|| fatal "could not enumerate bamai file descriptors"
	container_fd_count=${#fds[@]}
	printf '%s\t%s\t%s\n' "$(date +%s)" "$phase" "$container_fd_count" >> "$CONTAINER_FD_LOG" \
		|| fatal "could not record bamai FD sample in ${CONTAINER_FD_LOG}"
}

container_dump_fds() {
	local phase=$1 fd target
	for fd in /proc/"${container_bamai_pid}"/fd/[0-9]*; do
		target=$(readlink "$fd") || continue
		printf '%s\t%s\n' "${fd##*/}" "$target"
	done > "${HUATUO_BAMAI_TEST_TMPDIR}/container-fds-${phase}.txt"
}

container_sample_fd_baseline() {
	huatuo_bamai_metrics > /dev/null || fatal "bamai metrics request failed before batch container lifecycle"
	container_sample_fds baseline
	if ((container_fd_count == container_fd_baseline)); then
		container_fd_baseline_samples=$((container_fd_baseline_samples + 1))
	else
		container_fd_baseline=$container_fd_count
		container_fd_baseline_samples=1
	fi
	((container_fd_baseline_samples >= CONTAINER_FD_SAMPLES))
}

container_fds_recovered() {
	container_sample_fds recovery
	if ((container_fd_count <= container_fd_baseline)); then
		container_fd_stable=$((container_fd_stable + 1))
	else
		container_fd_stable=0
	fi
	((container_fd_stable >= CONTAINER_FD_SAMPLES))
}

assert_container_fd_recovery() {
	if ! wait_until "$WAIT_HUATUO_BAMAI_TIMEOUT" 1 container_fds_recovered; then
		container_dump_fds failed
		fatal "bamai FD count did not recover: baseline=${container_fd_baseline}, last=${container_fd_count}; see ${CONTAINER_FD_LOG}"
	fi
	container_dump_fds recovered
}

assert_container_lifecycle_healthy() {
	huatuo_bamai_ready || fatal "bamai is not ready after batch container lifecycle"
	huatuo_bamai_log_check || fatal "bamai logged an unexpected error during batch container lifecycle"
}

assert_container_ids() {
	local expected=$1 id
	shift
	(($# == expected)) || fatal "expected ${expected} container IDs, found $#"
	for id in "$@"; do
		[[ "$id" =~ ^[0-9a-f]{64}$ ]] || fatal "expected a full container ID, found: ${id}"
	done
}

container_batch_is_running() {
	local pods="${HUATUO_BAMAI_TEST_TMPDIR}/container-batch-pods.json"
	container_fetch_pods "$pods" || return 1
	jq -r --arg ns "$BUSINESS_POD_NS" --arg re "$CONTAINER_BATCH_REGEX" '
		.items[] | select(.metadata.namespace == $ns)
		| select(.metadata.name | test($re))
		| .status.containerStatuses[]?
		| select(.state.running != null and .containerID != null and .containerID != "")
		| .containerID | sub("^[^:]+://"; "")
	' "$pods" | sort -u > "${HUATUO_BAMAI_TEST_TMPDIR}/container-batch-ids.txt" || return 1
	mapfile -t container_batch_ids < "${HUATUO_BAMAI_TEST_TMPDIR}/container-batch-ids.txt"
	[[ ${#container_batch_ids[@]} -eq $CONTAINER_BATCH_COUNT ]]
}

container_observe_batch_startup() {
	local status=0
	container_batch_is_running || status=$?
	container_sample_fds creating
	# Scraping exercises container collection while runtime state is changing.
	huatuo_bamai_metrics > "${HUATUO_BAMAI_TEST_TMPDIR}/container-batch-metrics.txt" \
		|| fatal "bamai metrics request failed during batch container lifecycle"
	return "$status"
}

container_delete_pods() {
	local label=$1
	timeout 120 kubectl --request-timeout=30s delete pod --namespace "$BUSINESS_POD_NS" \
		-l "$label" --ignore-not-found --timeout=120s
}

container_batch_pods_absent() {
	local pods="${HUATUO_BAMAI_TEST_TMPDIR}/container-batch-pods.json"
	container_fetch_pods "$pods" || return 1
	# Pending and terminal Pods must disappear too before checking FD recovery.
	jq -ne --arg ns "$BUSINESS_POD_NS" --arg re "$CONTAINER_BATCH_REGEX" '
		input |
		[.items[] | select(.metadata.namespace == $ns)
		 | select(.metadata.name | test($re))] | length == 0
	' "$pods" > /dev/null
}

assert_container_fd_baseline() {
	# HTTP readiness can precede asynchronous tracer resource initialization.
	wait_until "$WAIT_HUATUO_BAMAI_TIMEOUT" 2 container_sample_fd_baseline \
		|| fatal "bamai FD baseline did not stabilize for ${CONTAINER_FD_SAMPLES} samples; see ${CONTAINER_FD_LOG}"
	container_dump_fds baseline
}

assert_container_batch_running() {
	wait_until "$WAIT_HUATUO_BAMAI_TIMEOUT" "$WAIT_HUATUO_BAMAI_INTERVAL" container_observe_batch_startup \
		|| fatal "expected ${CONTAINER_BATCH_COUNT} running containers, found ${#container_batch_ids[@]}"
	assert_container_ids "$CONTAINER_BATCH_COUNT" "${container_batch_ids[@]}"
}

assert_container_batch_pods_absent() {
	wait_until "$WAIT_HUATUO_BAMAI_TIMEOUT" "$WAIT_HUATUO_BAMAI_INTERVAL" container_batch_pods_absent \
		|| fatal "batch pods remain in kubelet; see ${HUATUO_BAMAI_TEST_TMPDIR}/container-batch-pods.json"
}

test_huatuo_bamai_batch_container_lifecycle() {
	container_bamai_pid=$(< "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")
	[[ "$container_bamai_pid" =~ ^[1-9][0-9]*$ ]] || fatal "invalid bamai PID"
	container_bamai_start_time_ticks=$(container_read_bamai_start_time_ticks) || fatal "could not read bamai starttime"
	printf 'timestamp\tphase\tfds\n' > "$CONTAINER_FD_LOG"
	assert_container_fd_baseline

	container_create_pods "$CONTAINER_BATCH_NAME" "$CONTAINER_BATCH_LABEL" "$CONTAINER_BATCH_COUNT"
	assert_container_batch_running
	container_record_watch_targets
	assert_container_watches present
	container_dump_fds running
	# Delete once bamai has established the target watches, without a dwell period.
	container_delete_pods "$CONTAINER_BATCH_LABEL"
	assert_container_batch_pods_absent
	assert_huatuo_bamai_containers_absent "batch container metadata removed" "${container_batch_ids[@]}"
	assert_container_watches absent
	assert_container_fd_recovery
	assert_container_lifecycle_healthy
	log_info "PASS: batch container lifecycle: containers=${CONTAINER_BATCH_COUNT}, baseline FDs=${container_fd_baseline}, recovered FDs=${container_fd_count}"
}

test_huatuo_bamai_discovers_existing_containers() {
	local existing namespace pod_name pod_name_regex
	log_info "checking discovery of an existing Kubernetes container"

	existing=$(kubelet_pods_json | jq -er '
			first(.items[]
				| select(.metadata.namespace == "kube-system")
				| select(.status.phase == "Running")
				| select(.spec.hostNetwork != true)
				| select(.status.containerStatuses | length == 1)
				| select(all(.status.containerStatuses[]; .ready == true))
				| select(.spec.containers | length == 1)
				| [.metadata.namespace, .metadata.name]
				| @tsv)') || fatal "no running single-container Kubernetes component pod is available"
	IFS=$'\t' read -r namespace pod_name <<< "$existing"
	pod_name_regex="^${pod_name//./\\.}$"

	assert_kubelet_pod_count \
		"$namespace" \
		"$pod_name_regex" \
		"1" \
		"existing Kubernetes component exists in kubelet"

	assert_huatuo_bamai_containers_present \
		"$namespace" \
		"$pod_name_regex" \
		"1" \
		"existing Kubernetes component exists in huatuo-bamai"

	log_info "PASS: existing Kubernetes container discovered: ${namespace}/${pod_name}"
}

container_record_watch_targets() {
	local id directory file identity
	local -a files
	: > "$CONTAINER_WATCH_TARGETS"
	for id in "${container_batch_ids[@]}"; do
		directory=$(cgroup_find_container "$container_memory_root" "$id") \
			|| fatal "memory cgroup not found for container ${id}"
		files=("${directory}/memory.limit_in_bytes")
		if [[ -f "${directory}/memory.max" ]]; then
			files=("${directory}/memory.max")
			if [[ -f "${directory}/memory.events.local" ]]; then
				files+=("${directory}/memory.events.local")
			else
				files+=("${directory}/memory.events")
			fi
		fi
		for file in "${files[@]}"; do
			identity=$(cgroup_watch_identity "$file") \
				|| fatal "could not identify watched file: ${file}"
			printf '%s\t%s\t%s\n' "$id" "$identity" "$file" >> "$CONTAINER_WATCH_TARGETS"
		done
	done
}

container_watches_match() {
	local expected=$1 id identity file actual status
	local snapshot="${HUATUO_BAMAI_TEST_TMPDIR}/container-watches-${expected}.txt"
	local mismatches="${HUATUO_BAMAI_TEST_TMPDIR}/container-watches-${expected}-mismatches.tsv"
	[[ -s "$CONTAINER_WATCH_TARGETS" ]] || return 1
	container_sample_fds "watches-${expected}"
	: > "$mismatches" || return 1
	cgroup_read_inotify_watches "$container_bamai_pid" "$snapshot" 2> "${snapshot}.err" || return 1
	while IFS=$'\t' read -r id identity file; do
		if grep -qFx -- "$identity" "$snapshot"; then
			actual=present
		else
			status=$?
			((status == 1)) || return 1
			actual=absent
		fi
		if [[ "$actual" != "$expected" ]]; then
			printf '%s\t%s\t%s\n' "$id" "$identity" "$file" >> "$mismatches" || return 1
		fi
	done < "$CONTAINER_WATCH_TARGETS"
	[[ ! -s "$mismatches" ]]
}

assert_container_watches() {
	local expected=$1
	wait_until "$WAIT_HUATUO_BAMAI_TIMEOUT" "$WAIT_HUATUO_BAMAI_INTERVAL" \
		container_watches_match "$expected" \
		|| fatal "container watches did not become ${expected}; see container-watch-targets.tsv and container-watches-${expected} artifacts"
	log_info "PASS: container watches ${expected}: containers=${CONTAINER_BATCH_COUNT}"
}

cleanup_container_batch() {
	local status=$?
	if ((status != 0)); then
		# The runner prints only the beginning of each artifact.
		tail -n 160 "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
			> "${HUATUO_BAMAI_TEST_TMPDIR}/container-batch-bamai-tail.log" || true
		kubectl --request-timeout=10s get pods -n "$BUSINESS_POD_NS" \
			-l "$CONTAINER_BATCH_LABEL" -o json \
			> "${HUATUO_BAMAI_TEST_TMPDIR}/container-batch-failed-pods.json" || true
	fi
	container_delete_pods "$CONTAINER_BATCH_LABEL" || status=1
	exit "$status"
}

require_commands kubectl jq timeout readlink stat findmnt find awk grep
container_memory_root=$(cgroup_memory_root) || fatal "memory cgroup mount not found"
container_node=$(kubelet_pods_json | jq -er '[.items[].spec.nodeName | select(. != null)] | unique | if length == 1 then .[0] else error("expected one local kubelet node") end') \
	|| fatal "could not determine the node served by the local kubelet"
trap cleanup_container_batch EXIT
test_huatuo_bamai_discovers_existing_containers
test_huatuo_bamai_batch_container_lifecycle
