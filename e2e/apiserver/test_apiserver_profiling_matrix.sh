#!/usr/bin/env bash
# Copyright 2026 The HuaTuo Authors.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

source "${ROOT_DIR}/integration/apiserver/lib.sh"

# Replace the runner baseline with the kubelet-enabled profiling daemon.
huatuo_bamai_stop

CURRENT_CASE=matrix-environment
require_commands kubectl ip crictl
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
require_commands "${PROFILER_TOOL_DIR}/bin/asprof" "${PROFILER_TOOL_DIR}/py-spy"
require_readable "${PROFILER_TOOL_DIR}/lib/libasyncProfiler.so"
POD_NAMESPACE="huatuo-apiserver-$(date +%s)-$$"
kubectl --request-timeout=30s create namespace "$POD_NAMESPACE"
kubectl --request-timeout=30s -n "$POD_NAMESPACE" create configmap workloads \
	--from-file="workload.c=${ROOT_DIR}/integration/testdata/test_apiserver_profiling_workload.c" \
	--from-file="${ROOT_DIR}/integration/testdata/TestProfilerJavaMemory.java" \
	--from-file="${ROOT_DIR}/integration/testdata/test_profiler_python_cpu.py"

start_test_storage
start_node
start_server

for language in c c++ go java python; do
	case "$language" in
	c)
		image=$native_image
		command='gcc -O2 -g -fno-omit-frame-pointer /src/workload.c -o /work/load && touch /work/ready && exec /work/load'
		;;
	c++)
		image=$native_image
		command='g++ -O2 -g -fno-omit-frame-pointer -x c++ /src/workload.c -o /work/load && touch /work/ready && exec /work/load'
		;;
	go)
		image=$go_image
		command='touch /work/ready && exec /fixtures/apiserver-go-workload'
		;;
	java)
		image=$java_image
		command='javac -d /work /src/TestProfilerJavaMemory.java && touch /work/ready && exec java -Xmx256m -cp /work TestProfilerJavaMemory'
		;;
	python)
		image=$python_image
		command='touch /work/ready && exec python3 /src/test_profiler_python_cpu.py independent'
		;;
	esac
	CURRENT_CASE="matrix-${language}-setup"
	jq -n --arg image "$image" --arg command "$command" --arg node "$APISERVER_TEST_NODE" --arg fixtures "$(dirname "$GO_WORKLOAD")" \
		'{apiVersion:"v1",kind:"Pod",metadata:{name:"workload"},spec:{nodeName:$node,restartPolicy:"Never",
		containers:[{name:"workload",image:$image,imagePullPolicy:"Never",command:["sh","-c",$command],
		readinessProbe:{exec:{command:["test","-f","/work/ready"]},periodSeconds:1},
		volumeMounts:[{name:"source",mountPath:"/src",readOnly:true},{name:"work",mountPath:"/work"},{name:"fixtures",mountPath:"/fixtures",readOnly:true}]}],
		volumes:[{name:"source",configMap:{name:"workloads"}},{name:"work",emptyDir:{}},{name:"fixtures",hostPath:{path:$fixtures,type:"Directory"}}]}}' \
		> "${HUATUO_BAMAI_TEST_TMPDIR}/pod.json"
	kubectl --request-timeout=30s -n "$POD_NAMESPACE" create -f "${HUATUO_BAMAI_TEST_TMPDIR}/pod.json"
	kubectl --request-timeout=310s -n "$POD_NAMESPACE" wait pod/workload --for=condition=Ready --timeout=300s
	container_id=$(kubectl --request-timeout=30s -n "$POD_NAMESPACE" get pod workload -o jsonpath='{.status.containerStatuses[0].containerID}')
	container_id=${container_id#*://}
	container_visible() {
		curl -fsS "${CURL_TIMEOUT[@]}" -H 'Authorization: Bearer integration-node-token' \
			"${HUATUO_BAMAI_ADDR}/v1/containers/$container_id" \
			> "${HUATUO_BAMAI_TEST_TMPDIR}/container-${language}.json"
	}
	wait_until 90 2 container_visible || fatal "bamai did not discover container $container_id"
	case "$language" in
	c | c++ | go) combinations=('cpu oncpu' 'cpu offcpu' 'memory virtual_alloc' 'memory physical_alloc' 'memory physical_usage') ;;
	java) combinations=('cpu oncpu' 'memory object_alloc' 'memory object_usage') ;;
	python) combinations=('cpu oncpu') ;;
	esac
	for combination in "${combinations[@]}"; do
		read -r typ mode <<< "$combination"
		CURRENT_CASE="matrix-${language}-${typ}-${mode}"
		extra=$(jq -nc --arg language "$language" --arg type "$typ" --arg mode "$mode" --arg id "$container_id" \
			'{language:$language,type:$type,mode:$mode,scope:"container",container_id:$id}')
		create 15 "$extra"
		case "$typ/$mode" in
		cpu/oncpu) expected_type=process_cpu:cpu:nanoseconds:cpu:nanoseconds ;;
		cpu/offcpu) expected_type=process_offcpu:offcpu:nanoseconds:offcpu:nanoseconds ;;
		memory/physical_usage) expected_type=memory:physical_usage:bytes:space:bytes ;;
		memory/*) expected_type=memory:alloc_space:bytes:space:bytes ;;
		esac
		verify_result "$JOB_ID" 1 "$expected_type"
		jq -e --arg id "$container_id" 'all(.data.items[]; .container_id == $id)' \
			"${HUATUO_BAMAI_TEST_TMPDIR}/raw-${JOB_ID}.json" > /dev/null || fatal 'raw result belongs to another container'
		pass
	done
	kubectl --request-timeout=30s -n "$POD_NAMESPACE" logs workload > "${HUATUO_BAMAI_TEST_TMPDIR}/workload-${language}.log"
	kubectl --request-timeout=70s -n "$POD_NAMESPACE" delete pod workload --wait=true --timeout=60s
done

assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.log" apiserver
cat "$SUMMARY"
