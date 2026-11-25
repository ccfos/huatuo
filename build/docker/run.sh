#!/bin/sh
#
# Copyright 2025 The HuaTuo Authors
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

ELASTICSEARCH_HOST=${ELASTICSEARCH_HOST:-localhost}
ELASTIC_PASSWORD=${ELASTIC_PASSWORD:-huatuo-bamai}

RUN_PATH=${RUN_PATH:-/home/huatuo-bamai}

elasticsearch_wait_http_ready() (
	target_url="http://${ELASTICSEARCH_HOST}:9200/"
	if [ -f huatuo-bamai.conf ]; then
		conf_addr=$(awk -F '"' '
 /^[[:space:]]*\[Storage\.Elasticsearch\][[:space:]]*(#.*)?$/ { in_storage=1; next }
 /^[[:space:]]*\[/ { in_storage=0 }
 in_storage && /^[[:space:]]*Address[[:space:]]*=/ { print $2; exit }
' huatuo-bamai.conf) || exit 1
		if [ -n "$conf_addr" ]; then
			target_url="${conf_addr%/}/"
		fi
	fi

	set -- --silent --show-error --connect-timeout 2 --max-time 15 \
		--write-out '\nHTTP %{http_code}'
	if [ -n "$ELASTIC_PASSWORD" ]; then
		set -- "$@" --user "elastic:${ELASTIC_PASSWORD}"
	fi
	# curl retries do not cover every non-200 response on the supported baseline.
	timeout 180 sh -c '
 while :; do
  response=$(curl "$@") && [ "${response##* }" = 200 ] && exit 0
  printf "%s\n" "$response" >&2
  sleep 1
 done
' sh "$@" "$target_url" || {
		result=$?
		printf 'Elasticsearch at %s did not return HTTP 200 within 180s (exit %s).\n' \
			"$target_url" "$result" >&2
		exit "$result"
	}
)

elasticsearch_wait_http_ready || exit $?
echo "Elasticsearch is ready."

# Run huatuo-bamai
cd "$RUN_PATH" || exit 1
exec ./bin/huatuo-bamai --region pre-test --config huatuo-bamai.conf
