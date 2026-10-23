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

# Pin upstream 0.19.06 so workload behavior stays stable across VM runs.
CPU_LOAD_IMAGE=${CPU_LOAD_IMAGE:-ghcr.io/colinianking/stress-ng@sha256:caf3092d685ed50f1d1e49af29d0ce226ba16b5dfd6175a819fdeefcd16c442f}

cpu_load_prepare_image() {
	require_commands crictl timeout
	if crictl inspecti "${CPU_LOAD_IMAGE}" > /dev/null 2>&1; then
		return
	fi
	log_info "pulling CPU workload image: ${CPU_LOAD_IMAGE}"
	timeout 300 crictl pull "${CPU_LOAD_IMAGE}" \
		|| fatal "cannot pull CPU workload image into the Kubernetes runtime: ${CPU_LOAD_IMAGE}"
}

cpu_load_prepare() {
	local directory=$1 binary=${STRESS_NG_BIN:-stress-ng}
	require_commands "${binary}"
	"${binary}" --version | tee "${directory}/host-stress-ng-version.txt"
	cpu_load_prepare_image
	cp "${ROOT_DIR}/e2e/testdata/test_cpu_load.sh" "${directory}/cpu-load"
	chmod 755 "${directory}/cpu-load"
}

cpu_load_ready() {
	grep -Fxq "ready $2" "$1"
}

cpu_load_worker_pid() {
	awk -v generation="$2" '
		$1 == "workers" && $2 == generation {
			if (NF != 3) exit 1
			print $3
			found=1
			exit
		}
		END { if (!found) exit 1 }
	' "$1"
}
