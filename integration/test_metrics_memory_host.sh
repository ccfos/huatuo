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

# Mutate private procfs fixtures and verify fresh, complete public memory samples.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"
source "${ROOT_DIR}/integration/lib_memory_metrics.sh"
require_commands awk sort diff

fixture_root="${HUATUO_BAMAI_TEST_TMPDIR}/memory-fixtures"
expected_buddy="${HUATUO_BAMAI_TEST_TMPDIR}/buddy-expected.txt"
expected_vmstat="${HUATUO_BAMAI_TEST_TMPDIR}/vmstat-expected.txt"
cp -a "${HUATUO_BAMAI_TEST_FIXTURES}" "${fixture_root}"

write_expected_buddyinfo() {
	local node zone order value
	while read -r node zone order value; do
		printf 'huatuo_bamai_memory_buddyinfo_blocks{host="huatuo-dev",node="%s",order="%s",region="dev",zone="%s"} %s\n' \
			"${node}" "${order}" "${zone}" "${value}"
	done > "${expected_buddy}"
}

write_expected_host_vmstat() {
	local field value
	while read -r field value; do
		printf 'huatuo_bamai_memory_vmstat_%s{host="huatuo-dev",region="dev"} %s\n' "${field}" "${value}"
	done > "${expected_vmstat}"
}

cat > "${fixture_root}/proc/buddyinfo" << 'INPUT'
Node 0, zone DMA 0 7
Node 0, zone Normal 11 13
Node 1, zone Normal 17 0
INPUT
printf 'pgfault 23\npgmajfault 0\nnr_free_pages 29\n' > "${fixture_root}/proc/vmstat"
write_expected_buddyinfo << 'EXPECTED'
0 DMA 0 0
0 DMA 1 7
0 Normal 0 11
0 Normal 1 13
1 Normal 0 17
1 Normal 1 0
EXPECTED
write_expected_host_vmstat < "${fixture_root}/proc/vmstat"
integration_huatuo_bamai_start write_config_bamai_memory_metrics \
	--region dev --procfs-prefix "${fixture_root}" --disable-storage --disable-kubelet --log-debug
assert_memory_metric_samples huatuo_bamai_memory_buddyinfo_ "${expected_buddy}"
assert_memory_metric_samples huatuo_bamai_memory_vmstat_ "${expected_vmstat}"

# Removing nodes, zones, orders and fields must not leave stale metric series.
printf 'Node 1, zone Normal 31\n' > "${fixture_root}/proc/buddyinfo.next"
mv "${fixture_root}/proc/buddyinfo.next" "${fixture_root}/proc/buddyinfo"
printf 'pgfault 0\nnr_free_pages 37\n' > "${fixture_root}/proc/vmstat.next"
mv "${fixture_root}/proc/vmstat.next" "${fixture_root}/proc/vmstat"
write_expected_buddyinfo << 'EXPECTED'
1 Normal 0 31
EXPECTED
write_expected_host_vmstat < "${fixture_root}/proc/vmstat"
assert_memory_metric_samples huatuo_bamai_memory_buddyinfo_ "${expected_buddy}"
assert_memory_metric_samples huatuo_bamai_memory_vmstat_ "${expected_vmstat}"
huatuo_bamai_stop
assert_huatuo_bamai_log_clean
