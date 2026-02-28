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

set -euo pipefail

# Run the core integration tests.
unshare --uts --mount bash -c '
	mount --make-rprivate /
	echo "huatuo-dev" > /proc/sys/kernel/hostname
	hostname huatuo-dev 2>/dev/null || true

	set -euo pipefail
    source "./integration/env.sh"
	source "${ROOT_DIR}/integration/lib.sh"

	# Always cleanup the tests.
	trap "integration_test_teardown \$?" EXIT

	integration_test_huatuo_bamai_start

    # auto run all test_*.sh scripts in the integration
    for case in "${ROOT_DIR}"/integration/test_*.sh; do
        [[ -f "$case" ]] || continue
        log_info "⬅️⬅️ start: $(basename "$case")"
        
        if ! bash "$case"; then
            fatal "❌ failed: $(basename "$case")"
        fi
        
        log_info "✅✅ passed: $(basename "$case")"
    done
    
    log_info "🎉🎉 all integration tests passed."
'
