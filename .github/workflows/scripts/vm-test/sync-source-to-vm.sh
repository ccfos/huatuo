#!/usr/bin/env bash
#
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
#

# Purpose: Replace /mnt/host in the VM with the current Huatuo source tree via rsync.
# Caller: qemu-1-start-vm.sh invokes this helper after starting the VM.
# Environment:
# - VM_ENV_FILE: Environment-file fallback when the positional parameter is omitted.
# - VM_IP: Required VM address loaded from the environment file.
# - VM_SSH_KEY: Required SSH private key path loaded from the environment file.
# Parameters:
# - VM_ENV_FILE: Optional runner-generated environment file; overrides the variable of the same name.
# Examples:
# - sync-source-to-vm.sh /tmp/run/vm.env  # sync using an explicit environment file

set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd -P)
SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
source "$SCRIPT_DIR/logging.sh"
env_file=${1:-${VM_ENV_FILE:-}}
[[ -n "$env_file" ]] || {
	vm_log_error 'usage: sync-source-to-vm.sh VM_ENV_FILE'
	exit 2
}
[[ -f "$env_file" && ! -L "$env_file" ]] || {
	vm_log_error "sync-source-to-vm: vm.env is missing or unsafe: $env_file"
	exit 1
}
# vm.env is emitted by the checksum-verified packaged runner.
source "$env_file"
: "${VM_IP:?vm.env is missing VM_IP}"
: "${VM_SSH_KEY:?vm.env is missing VM_SSH_KEY}"

ssh_opts=(
	-i "$VM_SSH_KEY"
	-o BatchMode=yes
	-o StrictHostKeyChecking=no
	-o UserKnownHostsFile=/dev/null
)
ssh "${ssh_opts[@]}" "root@$VM_IP" \
	'rm -rf /mnt/host && install -d -m 0755 /mnt/host'
printf -v rsync_ssh 'ssh -i %q -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null' "$VM_SSH_KEY"
rsync -az --delete \
	--exclude '/_output' \
	-e "$rsync_ssh" \
	"$ROOT_DIR/" "root@$VM_IP:/mnt/host/"
vm_log "synced Huatuo source to $VM_IP:/mnt/host"
