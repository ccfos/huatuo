#!/bin/sh
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

set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
work_dir=$(mktemp -d "${TMPDIR:-/tmp}/huatuo-run-test.XXXXXX")
mkdir -p "$work_dir/helpers" "$work_dir/run path/bin"

cat > "$work_dir/helpers/curl" << 'EOF'
#!/bin/sh
printf '%s\n' "$@" > "$CAPTURE_ARGS"
printf '200'
EOF
cat > "$work_dir/helpers/sleep" << 'EOF'
#!/bin/sh
exit 0
EOF
cat > "$work_dir/run path/bin/huatuo-bamai" << 'EOF'
#!/bin/sh
printf '%s\n' "$PWD" > "$CAPTURE_START"
EOF
chmod +x "$work_dir/helpers/curl" "$work_dir/helpers/sleep" \
	"$work_dir/run path/bin/huatuo-bamai"

password='space ";touch injected;# & $HOME'
export CAPTURE_ARGS="$work_dir/curl-args" CAPTURE_START="$work_dir/started"
export PATH="$work_dir/helpers:$PATH"
export ELASTICSEARCH_HOST=localhost ELASTIC_PASSWORD="$password"
export RUN_PATH="$work_dir/run path"

(cd "$work_dir" && sh "$script_dir/run.sh") > "$work_dir/output" 2>&1 || {
	printf 'run.sh failed; inspect %s\n' "$work_dir/output" >&2
	exit 1
}

if [ -e "$work_dir/injected" ]; then
	printf 'password was evaluated as shell code\n' >&2
	exit 1
fi
if ! grep -Fqx "elastic:$password" "$CAPTURE_ARGS"; then
	printf 'curl did not receive the literal password as one argument\n' >&2
	exit 1
fi
if [ "$(cat "$CAPTURE_START")" != "$RUN_PATH" ]; then
	printf 'agent did not start in RUN_PATH\n' >&2
	exit 1
fi
printf 'run.sh literal credentials and spaced RUN_PATH: PASS\n'
