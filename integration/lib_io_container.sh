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

# IO cases use the existing Kubernetes node and kubelet container discovery.
# The caller owns disk setup and installs cleanup before preparing containers.

CONTAINER_WORK="${HUATUO_BAMAI_TEST_TMPDIR}/io-container"
CONTAINER_IMAGE="${TEST_IO_CONTAINER_IMAGE:-${BUSINESS_POD_IMAGE}}"
container_ids=()
container_pods=(reader-0 reader-1)
container_pids=()
container_labels=()
container_node=""
container_namespace_created=0

io_container_prepare() {
	local owner
	require_commands kubectl crictl jq ip curl timeout
	require_readable "${KUBELET_CERT}" "${KUBELET_KEY}"
	[[ ${EUID} -eq 0 ]] || skip "container IO metrics require root"
	mkdir -p "${CONTAINER_WORK}"
	kubectl --request-timeout=10s get nodes -o json > "${CONTAINER_WORK}/nodes.json" \
		|| skip "Kubernetes API is unavailable"
	ip -j address > "${CONTAINER_WORK}/addresses.json"
	container_node=$(jq -er --slurpfile interfaces "${CONTAINER_WORK}/addresses.json" '
		[$interfaces[0][].addr_info[].local] as $addresses |
		[.items[] | select(any(.status.addresses[];
			.type == "InternalIP" and (.address as $ip | $addresses | index($ip) != null))) |
			.metadata.name] | if length == 1 then .[0] else error("expected one local node") end
	' "${CONTAINER_WORK}/nodes.json") || skip "container IO requires exactly one local Kubernetes node"
	crictl inspecti "${CONTAINER_IMAGE}" > /dev/null 2>&1 \
		|| skip "container IO image is unavailable through CRI: ${CONTAINER_IMAGE}"
	curl -fsSk "${CURL_TIMEOUT[@]}" --cert "${KUBELET_CERT}" --key "${KUBELET_KEY}" \
		"${KUBELET_PODS_API}" -o /dev/null || skip "authenticated local kubelet discovery is unavailable"
	read -r owner < /proc/sys/kernel/random/uuid
	POD_NAMESPACE="huatuo-io-${owner%%-*}"
	printf 'namespace=%s\nnode=%s\n' "${POD_NAMESPACE}" "${container_node}" > "${CONTAINER_WORK}/resources.txt"
	kubectl --request-timeout=10s create namespace "${POD_NAMESPACE}"
	container_namespace_created=1
	kubectl --request-timeout=10s -n "${POD_NAMESPACE}" create serviceaccount workload
}

io_container_cleanup() {
	if ((container_namespace_created)); then
		if ! kubectl --request-timeout=40s delete namespace "${POD_NAMESPACE}" \
			--wait=true --timeout=30s >> "${CONTAINER_WORK}/cleanup.log" 2>&1; then
			log_error "cannot reclaim namespace ${POD_NAMESPACE}; inspect ${CONTAINER_WORK}/cleanup.log and resources.txt"
			return 1
		fi
		container_namespace_created=0
	fi
}

start_io_container() {
	local index=$1 owner=${2:-$1} runtime_id
	jq -n --arg pod "${container_pods[index]}" --arg node "${container_node}" \
		--arg image "${CONTAINER_IMAGE}" --arg device "${device}" --arg owner "${owner}" '
		{apiVersion: "v1", kind: "Pod", metadata: {name: $pod}, spec: {
			nodeName: $node, hostname: ("huatuo-io-" + $owner), restartPolicy: "Never",
			terminationGracePeriodSeconds: 1, serviceAccountName: "workload",
			automountServiceAccountToken: false,
			containers: [{name: ("reader-" + $owner), image: $image, imagePullPolicy: "Never",
				command: ["/bin/sleep", "360"], securityContext: {privileged: true},
				volumeMounts: [{name: "device", mountPath: "/io-device", readOnly: true}]}],
			volumes: [{name: "device", hostPath: {path: $device, type: "BlockDevice"}}]}}
	' > "${CONTAINER_WORK}/pod-${index}.json"
	kubectl --request-timeout=10s -n "${POD_NAMESPACE}" create -f "${CONTAINER_WORK}/pod-${index}.json" \
		> "${CONTAINER_WORK}/start-${index}.log" 2>&1 || fatal "cannot create IO Pod ${container_pods[index]}"
	kubectl --request-timeout=70s -n "${POD_NAMESPACE}" wait "pod/${container_pods[index]}" \
		--for=condition=Ready --timeout=60s || fatal "IO Pod did not become ready: ${container_pods[index]}"
	runtime_id=$(kubectl --request-timeout=10s -n "${POD_NAMESPACE}" get pod "${container_pods[index]}" \
		-o jsonpath='{.status.containerStatuses[0].containerID}')
	container_ids[index]=${runtime_id#*://}
	[[ ${container_ids[index]} =~ ^[0-9a-f]{64}$ ]] || fatal "invalid IO Pod container ID: ${runtime_id}"
	timeout --kill-after=1 10 crictl inspect "${container_ids[index]}" > "${CONTAINER_WORK}/container-${index}.json" \
		|| fatal "cannot inspect IO Pod container ${container_ids[index]}"
	container_pids[index]=$(jq -er '.info.pid | tonumber | select(. > 0)' "${CONTAINER_WORK}/container-${index}.json") \
		|| fatal "IO Pod container has no running PID: ${container_ids[index]}"
	container_labels[index]=${owner}
	printf 'container_%s=%s\npid_%s=%s\ndevice=%s\n' "${index}" "${container_ids[index]}" \
		"${index}" "${container_pids[index]}" "${device}" >> "${CONTAINER_WORK}/resources.txt"
}

io_container_set_labels() {
	local owner
	case $1 in
	distinct) owner=1 ;;
	shared) owner=0 ;;
	*) fatal "unknown container label mode: $1" ;;
	esac
	[[ ${container_labels[1]} != "${owner}" ]] || return 0
	# Pod hostname and container name are immutable; recreate only this reader.
	kubectl --request-timeout=40s -n "${POD_NAMESPACE}" delete pod "${container_pods[1]}" \
		--wait=true --timeout=30s || fatal "cannot replace IO Pod ${container_pods[1]}"
	start_io_container 1 "${owner}"
}

io_container_metadata_ready() {
	: > "${CONTAINER_WORK}/metadata-$1.json"
	curl -sS "${CURL_TIMEOUT[@]}" -w '%{http_code}\n' \
		-o "${CONTAINER_WORK}/metadata-$1.json" "${HUATUO_BAMAI_ADDR}/v1/containers/${container_ids[$1]}" \
		> "${CONTAINER_WORK}/metadata-$1.http" 2> "${CONTAINER_WORK}/metadata-$1.err" || return 1
	[[ $(< "${CONTAINER_WORK}/metadata-$1.http") == 200 ]] \
		&& jq -e --arg id "${container_ids[$1]}" '.data.id == $id' "${CONTAINER_WORK}/metadata-$1.json" > /dev/null
}

io_container_css_ready() {
	local index
	io_container_missing=()
	for index in "$@"; do
		if ! io_container_metadata_ready "${index}"; then
			io_container_missing+=("container=${index}:not_discovered")
		elif ! jq -e '(.data.cgroup_css.blkio // "") | if type == "string"
			then test("^0x[0-9a-f]*[1-9a-f][0-9a-f]*$") else false end' \
			"${CONTAINER_WORK}/metadata-${index}.json" > /dev/null; then
			io_container_missing+=("container=${index}:blkio_css_missing")
		fi
	done
	((${#io_container_missing[@]} == 0))
}

assert_container_io_ready() {
	local phase=$1 index
	local -a io_container_missing=()
	shift
	io_container_phase=${phase}
	io_container_stage=metadata
	wait_until 30 0.2 io_container_css_ready "$@" \
		|| fatal "IO container phase=${phase} startup deadline: ${io_container_missing[*]}"
	io_container_stage=metrics
	for index in "$@"; do
		log_info "IO container phase=${phase} ready: $(jq -c '{id: .data.id, blkio_css: .data.cgroup_css.blkio}' "${CONTAINER_WORK}/metadata-${index}.json")"
	done
}

io_container_report_failure() {
	local index id pid daemon_pid
	daemon_pid=$(cat "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid" 2> /dev/null || echo unknown)
	log_error "IO container failure phase=${io_container_phase:-setup} stage=${io_container_stage:-setup} daemon_pid=${daemon_pid} address=${HUATUO_BAMAI_ADDR}"
	if ((container_namespace_created)); then
		kubectl --request-timeout=5s -n "${POD_NAMESPACE}" get pods -o yaml \
			> "${CONTAINER_WORK}/failure-pods.yaml" 2>&1 || true
		kubectl --request-timeout=5s -n "${POD_NAMESPACE}" get events \
			> "${CONTAINER_WORK}/failure-events.txt" 2>&1 || true
	fi
	for index in "${!container_ids[@]}"; do
		id=${container_ids[index]}
		# Metadata-stage failures already retain the final poll at its deadline.
		if [[ ${io_container_stage:-} != metadata ]]; then io_container_metadata_ready "${index}" || true; fi
		log_error "container=${id} HTTP=$(cat "${CONTAINER_WORK}/metadata-${index}.http" 2> /dev/null || echo unknown)"
		cat "${CONTAINER_WORK}/metadata-${index}.err" >&2 2> /dev/null || true
		jq -c '{id: .data.id, cgroup_css: .data.cgroup_css}' "${CONTAINER_WORK}/metadata-${index}.json" >&2 || true
		pid=${container_pids[index]:-}
		log_error "container=${id} task_pid=${pid:-unknown}"
		if [[ ${pid} =~ ^[1-9][0-9]*$ ]]; then cat "/proc/${pid}/cgroup" >&2; fi
	done
	io_test_report_daemon_logs "${container_ids[@]}"
}
