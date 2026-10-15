// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build integration

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ccfos/huatuo/cmd/huatuo-bamai/config"
	_ "github.com/ccfos/huatuo/cmd/huatuo-bamai/handlers"
	_ "github.com/ccfos/huatuo/core/autotracing"
	_ "github.com/ccfos/huatuo/core/events"
	_ "github.com/ccfos/huatuo/core/metrics"
	"github.com/ccfos/huatuo/internal/tracing"
)

func TestGeneratedElasticsearchConfig(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is required for the shell config helper")
	}
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	// Stub container startup; use the real helper to select the profile and
	// generate the config, then let the production loader validate it.
	cmd := exec.Command("bash", "-c", `
set -euo pipefail
source "$ROOT_DIR/integration/config.sh"
source "$ROOT_DIR/integration/bpf_mock/common/elasticsearch.sh"
require_commands() { :; }
docker() { :; }
storage_start() {
 [[ $1 == elasticsearch_v8 ]]
 STORAGE_ADDR=https://127.0.0.1:9200
 STORAGE_USERNAME=elastic
 STORAGE_PASSWORD=123456
}
storage_curl() { printf '%s' '{"acknowledged":true}'; }
start_bpf_mock_es
write_bpf_mock_es_config memory_oom_kill
`)
	cmd.Env = append(os.Environ(), "ROOT_DIR="+root, "HUATUO_BAMAI_TEST_TMPDIR="+workspace)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate ES config: %v: %s", err, output)
	}
	if err := config.Load(filepath.Join(workspace, "bamai.conf")); err != nil {
		t.Fatalf("production config validation: %v", err)
	}
	cfg := config.Get()
	if !cfg.Storage.Elasticsearch.Enabled() || cfg.Storage.Elasticsearch.Username != "elastic" ||
		cfg.Storage.Elasticsearch.Password != "123456" || cfg.Storage.LocalFile.Path != "" {
		t.Fatal("expected authenticated Elasticsearch with local storage disabled")
	}
}

func TestSingleEventBlacklist(t *testing.T) {
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	writeConfig := func(enabled string) {
		t.Helper()
		cmd := exec.Command("bash", "-c", `source "$ROOT_DIR/integration/config.sh"; write_single_event_config "$1"`, "test", enabled)
		cmd.Env = append(os.Environ(), "ROOT_DIR="+root, "HUATUO_BAMAI_TEST_TMPDIR="+workspace)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("generate config: %v: %s", err, output)
		}
		if err := config.Load(filepath.Join(workspace, "bamai.conf")); err != nil {
			t.Fatal(err)
		}
	}
	// Disable every tracer to inspect registrations without initializing collectors.
	writeConfig("none")
	if _, err := tracing.NewRegister(config.Get().BlackList); err != nil {
		t.Fatal(err)
	}
	registered := tracing.EventTracingStatus()
	for _, name := range []string{"xfs", "tracing_status"} {
		if _, ok := registered[name]; !ok {
			t.Fatalf("production tracer %s is not registered", name)
		}
	}
	for _, enabled := range []string{"memory_oom_kill", "softlockup", "hungtask"} {
		writeConfig(enabled)
		blacklist := config.Get().BlackList
		for name := range registered {
			if slices.Contains(blacklist, name) != (name != enabled) {
				t.Errorf("%s config: incorrect blacklist membership for %s", enabled, name)
			}
		}
	}
}
