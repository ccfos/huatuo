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

# Verify real loopback IPv6 traffic changes the exported interface counter.

set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"
require_commands ping curl awk
require_readable /proc/1/net/dev_snmp6/lo

cleanup() { huatuo_bamai_stop; }
trap cleanup EXIT

write_ipv6_metrics_config() {
	write_default_config
	cat >> "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" << 'EOF'

[MetricCollector.NetdevStats]
    DeviceIncluded = "^lo$"
    DeviceExcluded = ""
EOF
}

read_loopback_counter() {
	curl -sf "${CURL_TIMEOUT[@]}" "${HUATUO_BAMAI_METRICS_API}" \
		| awk '/^huatuo_bamai_netdev_ipv6_Ip6InReceives_total\{/ && /device="lo"/ { print $NF; found=1 } END { if (!found) exit 1 }'
}

integration_huatuo_bamai_start write_ipv6_metrics_config --region dev --disable-storage --disable-kubelet --log-debug
before=$(read_loopback_counter) || fatal "missing IPv6 loopback counter"
ping -6 -n -c 3 -W 2 ::1 > "${HUATUO_BAMAI_TEST_TMPDIR}/ping.log" 2>&1 \
	|| fatal "IPv6 loopback traffic failed"
counter_increased() {
	local after
	after=$(read_loopback_counter) || return 1
	awk -v before="${before}" -v after="${after}" 'BEGIN { exit !(after > before) }'
}
wait_until 10 0.2 counter_increased || fatal "IPv6 traffic did not increase the exported counter"
log_info "IPv6 loopback traffic increased the per-interface Prometheus counter"
