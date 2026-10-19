#!/usr/bin/env bash
# Copyright 2026 The HuaTuo Authors.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

exec bash "${ROOT_DIR}/integration/apiserver/cases.sh" functional
