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

# Verify huatuo-bamai startup and distinguish failures from ordinary log text.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"

integration_huatuo_bamai_start

if ! huatuo_bamai_log_check; then
	fatal "startup log contains an error, panic, or fatal message"
fi

readonly STARTUP_LOG_FIXTURE="${HUATUO_BAMAI_TEST_TMPDIR}/log-check"
mkdir -p "${STARTUP_LOG_FIXTURE}"
for startup_log_line in 'level=error msg="failure"' 'level="error" msg="failure"' \
	'level=panic msg="failure"' 'level=fatal msg="failure"' 'panic: runtime failure'; do
	printf '%s\n' "${startup_log_line}" > "${STARTUP_LOG_FIXTURE}/huatuo.log"
	if HUATUO_BAMAI_TEST_TMPDIR="${STARTUP_LOG_FIXTURE}" huatuo_bamai_log_check > /dev/null 2>&1; then
		fatal "log checker accepted a failure: ${startup_log_line}"
	fi
done
printf '%s\n' 'level=info msg="error and panic fields are available"' > "${STARTUP_LOG_FIXTURE}/huatuo.log"
HUATUO_BAMAI_TEST_TMPDIR="${STARTUP_LOG_FIXTURE}" huatuo_bamai_log_check \
	|| fatal "log checker rejected an ordinary info message"
