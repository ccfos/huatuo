#!/usr/bin/env bash
# Copyright 2026 The HuaTuo Authors.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

source "${ROOT_DIR}/integration/apiserver/lib.sh"

# Replace the runner baseline with the kubelet-enabled profiling daemon.
huatuo_bamai_stop

CURRENT_CASE=matrix-environment
require_commands kubectl ip
[[ -n ${APISERVER_TEST_NODE:-} ]] || skip 'matrix requires APISERVER_TEST_NODE (the local Kubernetes node)'
require_readable "$KUBELET_CERT" "$KUBELET_KEY"
kubectl --request-timeout=30s get node "$APISERVER_TEST_NODE" -o json > "${HUATUO_BAMAI_TEST_TMPDIR}/node.json"
ip -j address > "${HUATUO_BAMAI_TEST_TMPDIR}/addresses.json"
python3 - "$HUATUO_BAMAI_TEST_TMPDIR" << 'PY'
import json,sys
from pathlib import Path
root=Path(sys.argv[1])
node=json.loads((root/'node.json').read_text())
local={a['local'] for i in json.loads((root/'addresses.json').read_text()) for a in i['addr_info']}
assert any(a['address'] in local for a in node['status']['addresses'] if a['type']=='InternalIP'), 'APISERVER_TEST_NODE must identify this machine'
PY
require_commands "${PROFILER_TOOL_DIR}/bin/asprof" "${PROFILER_TOOL_DIR}/py-spy"
require_readable "${PROFILER_TOOL_DIR}/lib/libasyncProfiler.so"
POD_NAMESPACE="huatuo-apiserver-$(date +%s)-$$"
kubectl --request-timeout=30s create namespace "$POD_NAMESPACE"
kubectl --request-timeout=30s -n "$POD_NAMESPACE" create configmap workloads \
	--from-file="${ROOT_DIR}/integration/apiserver/testdata/workload.c" \
	--from-file="${ROOT_DIR}/integration/apiserver/testdata/workload.go" \
	--from-file="${ROOT_DIR}/integration/testdata/TestProfilerJavaMemory.java" \
	--from-file="${ROOT_DIR}/integration/testdata/test_profiler_python_cpu.py"

start_test_storage
start_node
start_server

for language in c c++ go java python; do
	case "$language" in
	c)
		image=${APISERVER_TEST_NATIVE_IMAGE:-gcc:14}
		command='gcc -O2 -g -fno-omit-frame-pointer /src/workload.c -o /work/load && touch /work/ready && exec /work/load'
		;;
	c++)
		image=${APISERVER_TEST_NATIVE_IMAGE:-gcc:14}
		command='g++ -O2 -g -fno-omit-frame-pointer -x c++ /src/workload.c -o /work/load && touch /work/ready && exec /work/load'
		;;
	go)
		image=${APISERVER_TEST_GO_IMAGE:-golang:1.24}
		command='go build -o /work/load /src/workload.go && touch /work/ready && exec /work/load'
		;;
	java)
		image=${APISERVER_TEST_JAVA_IMAGE:-eclipse-temurin:17-jdk}
		command='javac -d /work /src/TestProfilerJavaMemory.java && touch /work/ready && exec java -Xmx256m -cp /work TestProfilerJavaMemory'
		;;
	python)
		image=${APISERVER_TEST_PYTHON_IMAGE:-python:3.11-slim}
		command='touch /work/ready && exec python3 /src/test_profiler_python_cpu.py independent'
		;;
	esac
	CURRENT_CASE="matrix-${language}-setup"
	jq -n --arg image "$image" --arg command "$command" --arg node "$APISERVER_TEST_NODE" \
		'{apiVersion:"v1",kind:"Pod",metadata:{name:"workload"},spec:{nodeName:$node,restartPolicy:"Never",
		containers:[{name:"workload",image:$image,command:["sh","-c",$command],
		readinessProbe:{exec:{command:["test","-f","/work/ready"]},periodSeconds:1},
		volumeMounts:[{name:"source",mountPath:"/src",readOnly:true},{name:"work",mountPath:"/work"}]}],
		volumes:[{name:"source",configMap:{name:"workloads"}},{name:"work",emptyDir:{}}]}}' \
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
		verify_result "$JOB_ID" 1
		jq -e --arg id "$container_id" 'all(.data.items[]; .container_id == $id)' \
			"${HUATUO_BAMAI_TEST_TMPDIR}/raw-${JOB_ID}.json" > /dev/null || fatal 'raw result belongs to another container'
		pass
	done
	kubectl --request-timeout=30s -n "$POD_NAMESPACE" logs workload > "${HUATUO_BAMAI_TEST_TMPDIR}/workload-${language}.log"
	kubectl --request-timeout=70s -n "$POD_NAMESPACE" delete pod workload --wait=true --timeout=60s
done

assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.log" apiserver
cat "$SUMMARY"
