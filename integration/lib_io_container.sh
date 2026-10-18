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

# IO cases share real containerd tasks and externally served kubelet metadata.
# The caller owns disk setup and installs cleanup before preparing containers.

CONTAINER_WORK="${HUATUO_BAMAI_TEST_TMPDIR}/io-container"
CONTAINER_IMAGE="${TEST_IO_CONTAINER_IMAGE:-docker.io/library/busybox:1.36.1}"
CONTAINER_STATE="${TEST_IO_CONTAINERD_STATE_DIR:-/run/containerd}"
container_ids=()
created_containers=()
runtime_work=""
runtime_mounted=0
kubelet_pid=""
kubelet_port=""
container_command_timeout=15

container_ctr() {
	TMPDIR="${runtime_work:-${HUATUO_BAMAI_TEST_TMPDIR}}" \
		timeout --kill-after=1 "${container_command_timeout}" \
		ctr --address /run/containerd/containerd.sock --namespace k8s.io "$@"
}

io_container_prepare() {
	local owner seed images index
	require_commands ctr jq python3 openssl timeout mount umount readlink
	[[ ${EUID} -eq 0 ]] || skip "container IO metrics require root"
	[[ $(readlink /proc/self/ns/mnt) != "$(readlink /proc/1/ns/mnt)" ]] \
		|| fatal "container IO metrics require the integration runner's private mount namespace"
	mkdir -p "${CONTAINER_WORK}"
	[[ -S ${CONTAINER_STATE}/containerd.sock ]] \
		|| skip "container IO metrics require a preserved host socket: ${CONTAINER_STATE}/containerd.sock"
	# An outer /run isolation must preserve this directory before hiding /run.
	# /proc/1/root magic links cannot replace that mount-namespace handoff.
	if [[ ! ${CONTAINER_STATE} -ef /run/containerd ]]; then
		mkdir -p /run/containerd
		mount --bind "${CONTAINER_STATE}" /run/containerd
		runtime_mounted=1
		mount -o remount,bind,ro /run/containerd
	fi
	[[ -S /run/containerd/containerd.sock ]] || fatal "preserved containerd mount has no socket"
	container_ctr version > "${CONTAINER_WORK}/runtime-version.txt" \
		|| fatal "host containerd is not reachable through the preserved socket"
	images=$(container_ctr images list --quiet) || fatal "cannot query containerd image cache"
	grep -Fxq "${CONTAINER_IMAGE}" <<< "${images}" || skip "container IO image is not cached: ${CONTAINER_IMAGE}"
	runtime_work=$(mktemp -d /var/tmp/huatuo-io-container.XXXXXX)
	[[ ${runtime_work} -ef /proc/1/root${runtime_work} ]] \
		|| fatal "containerd FIFO directory is not visible from the host: ${runtime_work}"
	mkdir "${runtime_work}/fifos"
	read -r owner < /proc/sys/kernel/random/uuid
	POD_NAMESPACE="huatuo-io-${owner%%-*}"
	printf 'runtime_state=%s\nfifos=%s\nnamespace=%s\n' \
		"${CONTAINER_STATE}" "${runtime_work}" "${POD_NAMESPACE}" > "${CONTAINER_WORK}/resources.txt"
	for index in 0 1; do
		read -r seed < /proc/sys/kernel/random/uuid
		seed=${seed//-/}
		container_ids+=("${seed}${seed}")
	done
}

io_container_cleanup() {
	local id containers="" status=0
	container_command_timeout=5
	if ((${#created_containers[@]})) && ! containers=$(container_ctr containers list --quiet); then
		log_error "cannot query owned containers; retaining resources listed in ${CONTAINER_WORK}/resources.txt"
		status=1
	fi
	for id in "${created_containers[@]}"; do
		if grep -Fxq "${id}" <<< "${containers}"; then
			container_ctr tasks delete --force "${id}" >> "${CONTAINER_WORK}/cleanup.log" 2>&1 || true
			if ! container_ctr containers delete "${id}" >> "${CONTAINER_WORK}/cleanup.log" 2>&1; then
				log_error "cannot reclaim container ${id}; check resources.txt before manual cleanup"
				status=1
			fi
		fi
	done
	if [[ -n ${kubelet_pid} ]]; then
		stop_by_pid "${kubelet_pid}" 2
		if wait_until 2 0.1 test ! -e "/proc/${kubelet_pid}/exe"; then
			wait "${kubelet_pid}" 2> /dev/null || true
			kubelet_pid=""
		else
			log_error "kubelet fixture remains: pid=${kubelet_pid}; retaining ${runtime_work}"
			status=1
		fi
	fi
	if ((status == 0)); then
		[[ -z ${runtime_work} ]] || rm -rf -- "${runtime_work}"
		runtime_work=""
	else
		log_error "retaining FIFO resources until container IO exits: ${runtime_work}"
	fi
	if ((runtime_mounted)); then
		umount /run/containerd || status=1
		runtime_mounted=0
	fi
	return "${status}"
}

start_io_container() {
	local index=$1 id=${container_ids[$1]}
	local -a cgroup_args=()
	[[ -z ${2:-} ]] || cgroup_args=(--cgroup "$2")
	[[ ${device} -ef /proc/1/root${device} ]] || fatal "test device is not visible to host containerd: ${device}"
	created_containers+=("${id}")
	printf 'container_%s=%s\ndevice=%s\n' "${index}" "${id}" "${device}" >> "${CONTAINER_WORK}/resources.txt"
	container_ctr run --detach --fifo-dir "${runtime_work}/fifos" \
		"${cgroup_args[@]}" --device "${device}" "${CONTAINER_IMAGE}" "${id}" /bin/sleep 360 \
		> "${CONTAINER_WORK}/start-${index}.log" 2>&1 || fatal "cannot start IO container ${id}"
	container_ctr containers info "${id}" > "${CONTAINER_WORK}/container-${index}.json"
}

write_container_pods() {
	local label_mode=${2:-distinct}
	[[ ${label_mode} == distinct || ${label_mode} == shared ]] || fatal "unknown container label mode: ${label_mode}"
	jq -n --argjson count "$1" --arg a "${container_ids[0]}" --arg b "${container_ids[1]}" \
		--arg ns "${POD_NAMESPACE}" --arg image "${CONTAINER_IMAGE}" --arg mode "${label_mode}" '
		{apiVersion: "v1", kind: "PodList", items: [range($count) as $i |
			([ $a, $b ][$i]) as $id | (if $mode == "shared" then 0 else $i end) as $pod_index |
			("huatuo-io-" + ($pod_index | tostring)) as $pod | ("reader-" + ($pod_index | tostring)) as $name |
			{metadata: {name: $pod, namespace: $ns, uid: $id,
				ownerReferences: [{apiVersion: "apps/v1", kind: "ReplicaSet", name: "io-blackbox", uid: "io-blackbox"}]},
			 spec: {hostname: $pod, containers: [{name: $name, image: $image}]},
			 status: {phase: "Running", qosClass: "BestEffort", containerStatuses: [
				{name: $name, containerID: ("containerd://" + $id), ready: true,
				 restartCount: 0, state: {running: {}}}]}}]}
	' > "${CONTAINER_WORK}/pods.next"
	mv "${CONTAINER_WORK}/pods.next" "${CONTAINER_WORK}/pods.json"
}

io_container_start_kubelet() {
	# The authorized kubelet port serves both /pods and /configz. Test-local
	# credentials avoid dependence on an installed cluster or its private keys.
	(
		umask 077
		openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
			-subj /CN=localhost -addext 'subjectAltName=IP:127.0.0.1,DNS:localhost' \
			-keyout "${runtime_work}/kubelet.key" -out "${CONTAINER_WORK}/kubelet.crt"
	) > "${CONTAINER_WORK}/certificate.log" 2>&1 || fatal "cannot create test kubelet credentials"
	python3 "${ROOT_DIR}/integration/testdata/test_io_kubelet.py" \
		--pods-file "${CONTAINER_WORK}/pods.json" --port-file "${CONTAINER_WORK}/kubelet.port" \
		--cert-file "${CONTAINER_WORK}/kubelet.crt" --key-file "${runtime_work}/kubelet.key" \
		> "${CONTAINER_WORK}/kubelet.log" 2>&1 &
	kubelet_pid=$!
	wait_until 5 0.1 test -s "${CONTAINER_WORK}/kubelet.port" || fatal "kubelet HTTPS fixture did not listen"
	kubelet_port=$(< "${CONTAINER_WORK}/kubelet.port")
}

io_container_write_pod_config() {
	cat >> "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" << EOF

[Pod]
    KubeletReadOnlyPort = 0
    KubeletAuthorizedPort = ${kubelet_port}
    KubeletClientCertPath = "${CONTAINER_WORK}/kubelet.crt,${runtime_work}/kubelet.key"
EOF
}

assert_container_metadata_ready() {
	curl -sf "${CURL_TIMEOUT[@]}" "${HUATUO_BAMAI_ADDR}/v1/containers/${container_ids[$1]}" \
		> "${CONTAINER_WORK}/metadata-$1.json" \
		&& jq -e --arg id "${container_ids[$1]}" '.data.id == $id' \
			"${CONTAINER_WORK}/metadata-$1.json" > /dev/null
}
