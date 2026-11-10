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
archive=${1:?generated source archive is required}
ssh_opts=(-i "$VM_SSH_KEY" -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null)
printf -v transport 'ssh -i %q -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null' "$VM_SSH_KEY"
rsync -az -e "$transport" "$archive" "root@$VM_IP:/tmp/huatuo-generated.tar"
ssh "${ssh_opts[@]}" "root@$VM_IP" \
	'cd /mnt/host && tar -xf /tmp/huatuo-generated.tar && rm /tmp/huatuo-generated.tar'
