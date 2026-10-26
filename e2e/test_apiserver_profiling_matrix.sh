#!/usr/bin/env bash
# Copyright 2026 The HuaTuo Authors.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

APISERVER_PROFILE_INTERVAL=2
source "${ROOT_DIR}/integration/lib_apiserver.sh"

# Replace the runner baseline with the kubelet-enabled profiling daemon.
huatuo_bamai_stop

CURRENT_CASE=matrix-environment
require_commands kubectl ip crictl python3
require_readable "$KUBELET_CERT" "$KUBELET_KEY"
ip -j address > "${HUATUO_BAMAI_TEST_TMPDIR}/addresses.json"
kubectl --request-timeout=30s get nodes -o json > "${HUATUO_BAMAI_TEST_TMPDIR}/nodes.json"
APISERVER_TEST_NODE=$(
	python3 - "$HUATUO_BAMAI_TEST_TMPDIR" "${APISERVER_TEST_NODE:-}" << 'PYTHON'
import json, sys
from pathlib import Path
root = Path(sys.argv[1])
nodes = json.loads((root / 'nodes.json').read_text())['items']
local = {a['local'] for i in json.loads((root / 'addresses.json').read_text()) for a in i['addr_info']}
matches = [n['metadata']['name'] for n in nodes
           if any(a['address'] in local for a in n['status'].get('addresses', []) if a['type'] == 'InternalIP')
           and (not sys.argv[2] or n['metadata']['name'] == sys.argv[2])]
if len(matches) != 1:
    sys.exit('expected exactly one local Kubernetes node; check APISERVER_TEST_NODE and node InternalIP')
print(matches[0])
PYTHON
) || fatal 'cannot identify the local Kubernetes node'
log_info "profiling matrix node: $APISERVER_TEST_NODE"

native_image=${APISERVER_TEST_NATIVE_IMAGE:-gcc:14}
go_image=${APISERVER_TEST_GO_IMAGE:-golang:1.24}
java_image=${APISERVER_TEST_JAVA_IMAGE:-eclipse-temurin:17-jdk}
python_image=${APISERVER_TEST_PYTHON_IMAGE:-python:3.11-slim}
mapfile -t images < <(printf '%s\n' "$native_image" "$go_image" "$java_image" "$python_image" | sort -u)
for i in "${!images[@]}"; do
	image=${images[$i]}
	if ! crictl inspecti "$image" > /dev/null 2>&1; then
		log_info "pulling matrix image: $image"
		timeout 5m crictl pull "$image" > "${HUATUO_BAMAI_TEST_TMPDIR}/image-pull-${i}.log" 2>&1 || {
			cat "${HUATUO_BAMAI_TEST_TMPDIR}/image-pull-${i}.log" >&2
			fatal "cannot prepare matrix image: $image"
		}
	fi
	crictl inspecti "$image" > /dev/null || fatal "matrix image unavailable through CRI: $image"
done
GO_WORKLOAD="$(cd "${ROOT_DIR}/_output/tests" && pwd)/apiserver-go-workload"
[[ -x $GO_WORKLOAD ]] || fatal 'missing Go workload; run make build'
for tool in "${PROFILER_TOOL_DIR}/bin/asprof" "${PROFILER_TOOL_DIR}/py-spy"; do
	[[ -f $tool && -x $tool ]] || fatal "profiling tool missing or not executable: $tool; run make build"
done
[[ -f "${PROFILER_TOOL_DIR}/lib/libasyncProfiler.so" && -r "${PROFILER_TOOL_DIR}/lib/libasyncProfiler.so" ]] \
	|| fatal "profiling library missing or not readable: ${PROFILER_TOOL_DIR}/lib/libasyncProfiler.so; run make build"
POD_NAMESPACE="huatuo-apiserver-$(date +%s)-$$"
kubectl --request-timeout=30s create namespace "$POD_NAMESPACE"
kubectl --request-timeout=30s -n "$POD_NAMESPACE" create configmap workloads \
	--from-file="workload.c=${ROOT_DIR}/integration/testdata/test_apiserver_profiling_workload.c" \
	--from-file="${ROOT_DIR}/integration/testdata/TestProfilerJavaMemory.java" \
	--from-file="${ROOT_DIR}/integration/testdata/test_profiler_python_cpu.py"

for language in c c++ go java python; do
	case "$language" in
	c)
		image=$native_image
		matrix_prepare='gcc -O2 -g -fno-omit-frame-pointer /src/workload.c -o /work/load'
		command='exec /work/load'
		;;
	c++)
		image=$native_image
		matrix_prepare='g++ -O2 -g -fno-omit-frame-pointer -x c++ /src/workload.c -o /work/load'
		command='exec /work/load'
		;;
	go)
		image=$go_image
		matrix_prepare='test -x /fixtures/apiserver-go-workload'
		command='exec /fixtures/apiserver-go-workload'
		;;
	java)
		image=$java_image
		matrix_prepare='javac -d /work /src/TestProfilerJavaMemory.java'
		command='exec java -Xmx256m -cp /work TestProfilerJavaMemory'
		;;
	python)
		image=$python_image
		matrix_prepare='command -v python3'
		command='exec python3 /src/test_profiler_python_cpu.py independent'
		;;
	esac
	CURRENT_CASE="matrix-${language}-prepare"
	matrix_pod="workload-${language//+/p}"
	# Preparation may overlap storage startup; only one workload is activated at a time.
	command="$matrix_prepare && mkfifo /work/start && touch /work/prepared && read -r start < /work/start && $command"
	jq -n --arg pod "$matrix_pod" --arg image "$image" --arg command "$command" --arg node "$APISERVER_TEST_NODE" --arg fixtures "$(dirname "$GO_WORKLOAD")" \
		'{apiVersion:"v1",kind:"Pod",metadata:{name:$pod,labels:{app:"apiserver-profiling"}},spec:{nodeName:$node,restartPolicy:"Never",terminationGracePeriodSeconds:2,
		containers:[{name:"workload",image:$image,imagePullPolicy:"Never",command:["sh","-c",$command],
		readinessProbe:{exec:{command:["test","-f","/work/prepared"]},periodSeconds:1},
		volumeMounts:[{name:"source",mountPath:"/src",readOnly:true},{name:"work",mountPath:"/work"},{name:"fixtures",mountPath:"/fixtures",readOnly:true}]}],
		volumes:[{name:"source",configMap:{name:"workloads"}},{name:"work",emptyDir:{}},{name:"fixtures",hostPath:{path:$fixtures,type:"Directory"}}]}}' \
		> "${HUATUO_BAMAI_TEST_TMPDIR}/pod-${language}.json"
	kubectl --request-timeout=30s -n "$POD_NAMESPACE" create -f "${HUATUO_BAMAI_TEST_TMPDIR}/pod-${language}.json"
done

start_test_storage
start_node
start_server
kubectl --request-timeout=310s -n "$POD_NAMESPACE" wait pod -l app=apiserver-profiling --for=condition=Ready --timeout=300s

for language in c c++ go java python; do
	CURRENT_CASE="matrix-${language}-setup"
	matrix_pod="workload-${language//+/p}"
	container_id=$(kubectl --request-timeout=30s -n "$POD_NAMESPACE" get pod "$matrix_pod" -o jsonpath='{.status.containerStatuses[0].containerID}')
	container_id=${container_id#*://}
	container_visible() {
		curl -fsS "${CURL_TIMEOUT[@]}" -H 'Authorization: Bearer integration-node-token' \
			"${HUATUO_BAMAI_ADDR}/v1/containers/$container_id" \
			> "${HUATUO_BAMAI_TEST_TMPDIR}/container-${language}.json"
	}
	wait_until 90 2 container_visible || fatal "bamai did not discover container $container_id"
	# Each idle container must be discovered before its target process is released.
	kubectl --request-timeout=30s -n "$POD_NAMESPACE" exec "$matrix_pod" -- sh -c 'printf "start\n" > /work/start'
	workload_ready() {
		kubectl --request-timeout=5s -n "$POD_NAMESPACE" logs "$matrix_pod" > "${HUATUO_BAMAI_TEST_TMPDIR}/workload-${language}.log" \
			&& grep -qx ready "${HUATUO_BAMAI_TEST_TMPDIR}/workload-${language}.log"
	}
	wait_until 30 0.2 workload_ready || fatal "$language workload did not start"
	matrix_minimum_windows=3
	case "$language" in
	c | c++ | go)
		matrix_duration=6
		combinations=('cpu oncpu' 'cpu offcpu' 'memory virtual_alloc' 'memory physical_alloc' 'memory physical_usage')
		;;
	java)
		matrix_duration=8
		combinations=('cpu oncpu' 'memory object_alloc' 'memory object_usage')
		;;
	python)
		matrix_duration=5
		matrix_minimum_windows=1 # Python publishes a single profile at natural completion.
		combinations=('cpu oncpu')
		;;
	esac
	for combination in "${combinations[@]}"; do
		read -r typ mode <<< "$combination"
		CURRENT_CASE="matrix-${language}-${typ}-${mode}"
		extra=$(jq -nc --arg language "$language" --arg type "$typ" --arg mode "$mode" --arg id "$container_id" \
			'{language:$language,type:$type,mode:$mode,scope:"container",container_id:$id}')
		create "$matrix_duration" "$extra"
		case "$typ/$mode" in
		cpu/oncpu) expected_type=process_cpu:cpu:nanoseconds:cpu:nanoseconds ;;
		cpu/offcpu) expected_type=process_offcpu:offcpu:nanoseconds:offcpu:nanoseconds ;;
		memory/physical_usage) expected_type=memory:physical_usage:bytes:space:bytes ;;
		memory/*) expected_type=memory:alloc_space:bytes:space:bytes ;;
		esac
		verify_result "$JOB_ID" "$matrix_minimum_windows" "$expected_type"
		jq -e --arg id "$container_id" 'all(.data.items[]; .container_id == $id)' \
			"${HUATUO_BAMAI_TEST_TMPDIR}/raw-${JOB_ID}.json" > /dev/null || fatal 'raw result belongs to another container'
		pass
	done
	kubectl --request-timeout=30s -n "$POD_NAMESPACE" logs "$matrix_pod" > "${HUATUO_BAMAI_TEST_TMPDIR}/workload-${language}.log"
	kubectl --request-timeout=70s -n "$POD_NAMESPACE" delete pod "$matrix_pod" --wait=true --timeout=60s
done

assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.log" apiserver
cat "$SUMMARY"
