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

export BASEDIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${BASEDIR}/utils.sh"

# Always cleanup the tests.
trap 'test_teardown $?' EXIT

# Run the core integration tests.
test_run() {
	unshare --uts --mount bash -c '
		mount --make-rprivate /
		echo "huatuo-dev" > /proc/sys/kernel/hostname
		hostname huatuo-dev 2>/dev/null || true

		source "${BASEDIR}/utils.sh"
		test_setup
		test_metrics
		# more tests ...
	'
}

# Run integration test and preserve exit code even under `set -e`.
test_run
