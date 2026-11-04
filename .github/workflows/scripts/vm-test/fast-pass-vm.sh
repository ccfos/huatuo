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

set -euo pipefail
source "${VM_ENV_FILE:?VM_ENV_FILE is required}"
ssh_opts=(-i "$VM_SSH_KEY" -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null)
printf -v transport 'ssh -i %q -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null' "$VM_SSH_KEY"
mode=${1:?expected build, run, or collect}
case "$mode" in
build)
	rsync -az -e "$transport" fast-generated/portable-generated.tar "root@$VM_IP:/mnt/host/_output/portable-generated.tar"
	ssh "${ssh_opts[@]}" "root@$VM_IP" bash /mnt/host/.github/workflows/scripts/vm-test/fast-pass-guest.sh build
	mkdir -p fast-build
	rsync -az -e "$transport" "root@$VM_IP:/tmp/huatuo-fast-build.tar.gz" fast-build/
	jq '{revision, image}' fast-plan/plan.json > fast-build/identity.json
	;;
run)
	jq -e --slurpfile plan fast-plan/plan.json \
		'.revision == $plan[0].revision and .image == $plan[0].image' fast-build/identity.json
	rsync -a -e "$transport" fast-build/huatuo-fast-build.tar.gz "root@$VM_IP:/tmp/huatuo-fast-build.tar.gz"
	rm fast-build/huatuo-fast-build.tar.gz
	shard=$(jq -ce --arg id "${SHARD_ID:?SHARD_ID is required}" '.shards[] | select(.id == $id)' fast-plan/plan.json)
	suite=$(jq -r .suite <<< "$shard")
	rsync -az -e "$transport" "fast-plan/${SHARD_ID}.txt" "root@$VM_IP:/tmp/huatuo-fast-tests.txt"
	mkdir -p fast-result
	status=0
	ssh "${ssh_opts[@]}" "root@$VM_IP" bash /mnt/host/.github/workflows/scripts/vm-test/fast-pass-guest.sh run "$suite" \
		2>&1 | tee fast-result/vm.log || status=$?
	jq --argjson shard "$shard" --argjson code "$status" \
		'{revision, image} + {shard: $shard, exit_code: $code}' fast-plan/plan.json > fast-result/receipt.json
	exit "$status"
	;;
collect)
	mkdir -p fast-result
	{
		df -h "${RUNNER_TEMP:?RUNNER_TEMP is required}"
		ssh "${ssh_opts[@]}" "root@$VM_IP" '
            df -h
            free -h
            systemctl status kubelet containerd --no-pager || true
            journalctl -b -u kubelet -u containerd --no-pager -n 200
            dmesg --level=err,warn | tail -n 100
        '
	} > fast-result/vm-diagnostics.log 2>&1 || true
	# Retain diagnostics, not multi-gigabyte workload files in test workspaces.
	rsync -azm -e "$transport" --include='*/' --include='*.json' --include='*.jsonl' \
		--include='*.log' --include='*.out' --include='*.err' --include='*.txt' \
		--include='*.folded' --include='*.toml' --include='*.conf' --include='*.pprof' \
		--include='unit-coverage.*' --exclude='*' \
		"root@$VM_IP:/tmp/huatuo-fast-result/" fast-result/
	;;
*)
	echo 'usage: fast-pass-vm.sh build|run|collect' >&2
	exit 2
	;;
esac
