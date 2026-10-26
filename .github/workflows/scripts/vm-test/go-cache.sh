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
mode=${1:?expected key, restore, or save}
ssh_opts=(-i "$VM_SSH_KEY" -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null)
printf -v transport 'ssh -i %q -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null' "$VM_SSH_KEY"
case "$mode" in
key)
	# The image also scopes cached Cgo output to its headers and system libraries.
	metadata=$(ssh "${ssh_opts[@]}" "root@$VM_IP" '
        set -eu
        export PATH=/usr/local/go/bin:$PATH
        cd /mnt/host
        go env GOVERSION GOOS GOARCH CGO_ENABLED CC GOEXPERIMENT GOFLAGS
        gcc -dumpfullversion -dumpversion
        getconf GNU_LIBC_VERSION
        # Package revisions also distinguish C headers and library patch updates.
        if command -v dpkg-query >/dev/null 2>&1; then
            packages=$(dpkg-query -W)
        else
            packages=$(rpm -qa)
        fi
        printf "%s\n" "$packages" | LC_ALL=C sort
    ')
	key=$(printf '%s\n%s\n' "${VM_IMAGE_REF:?VM_IMAGE_REF is required}" "$metadata" | sha256sum)
	printf 'key=%s\n' "${key%% *}"
	;;
restore | save)
	# This directory belongs only to this job; it is outside the source tree.
	cache_dir="${RUNNER_TEMP:?RUNNER_TEMP is required}/huatuo-go-build"
	mkdir -p "$cache_dir"
	if [[ "$mode" == restore ]]; then
		ssh "${ssh_opts[@]}" "root@$VM_IP" 'mkdir -p /root/.cache/go-build'
		rsync -rtz -e "$transport" "$cache_dir/" "root@$VM_IP:/root/.cache/go-build/"
	else
		rsync -rtz --delete -e "$transport" "root@$VM_IP:/root/.cache/go-build/" "$cache_dir/"
	fi
	du -sh "$cache_dir"
	;;
*)
	echo 'usage: go-cache.sh key|restore|save' >&2
	exit 2
	;;
esac
