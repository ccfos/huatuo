package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type sampleConfig struct {
	Name    string `toml:"name"`
	Count   int    `toml:"count"`
	Enabled bool   `toml:"enabled"`
	Nested  struct {
		Value string `toml:"value"`
	} `toml:"nested"`
}

func writeConfigFile(t *testing.T, dir, name, content string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Errorf("write config file %s: %v", path, err)
		return ""
	}

	return path
}

// TestCoreBinDir 验证内部通用配置工具的基础环境初始化。
// 场景包括：启动时自动识别二进制目录，且该目录不会是空字符串。
func TestCoreBinDir(t *testing.T) {
	if CoreBinDir == "" {
		t.Errorf("CoreBinDir should not be empty")
	}
}

// TestLoad 验证严格 TOML 加载行为。
// 场景包括：正常加载旧格式 TOML、加载嵌套字段、类型不匹配时报错、严格模式下未知字段时报错。
func TestLoad(t *testing.T) {
	tmpDir := t.TempDir()

	cases := []struct {
		name     string
		content  string
		validate func(*testing.T, error, *sampleConfig)
	}{
		{
			name: "valid-basic-config",
			content: `
name = "huatuo-dev"
count = 8
enabled = true
`,
			validate: func(t *testing.T, err error, cfg *sampleConfig) {
				if err != nil {
					t.Errorf("Load returned error: %v", err)
					return
				}
				if cfg.Name != "huatuo-dev" {
					t.Errorf("unexpected Name: %q", cfg.Name)
				}
				if cfg.Count != 8 {
					t.Errorf("unexpected Count: %d", cfg.Count)
				}
				if !cfg.Enabled {
					t.Errorf("Enabled should be true")
				}
			},
		},
		{
			name: "valid-nested-config",
			content: `
name = "huatuo-region"
count = 12
enabled = false

[nested]
value = "kernel_sched_tick"
`,
			validate: func(t *testing.T, err error, cfg *sampleConfig) {
				if err != nil {
					t.Errorf("Load returned error: %v", err)
					return
				}
				if cfg.Nested.Value != "kernel_sched_tick" {
					t.Errorf("unexpected nested value: %q", cfg.Nested.Value)
				}
			},
		},
		{
			name: "type-mismatch",
			content: `
name = "huatuo-dev"
count = "unexpected-string"
enabled = true
`,
			validate: func(t *testing.T, err error, cfg *sampleConfig) {
				if err == nil {
					t.Errorf("Load should fail for type mismatch")
				}
			},
		},
		{
			name: "unknown-field",
			content: `
name = "huatuo-dev"
count = 8
enabled = true
unexpected_key = "strict-mode"
`,
			validate: func(t *testing.T, err error, cfg *sampleConfig) {
				if err == nil {
					t.Errorf("Load should fail for unknown field")
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfigFile(t, tmpDir, tc.name+".toml", tc.content)
			if path == "" {
				return
			}

			cfg := &sampleConfig{}
			err := Load(path, cfg)
			tc.validate(t, err, cfg)
		})
	}
}

// TestSyncAndSet 验证通用配置工具的写回和点路径更新能力。
// 场景包括：先写回配置再重新加载、更新顶层字段、更新嵌套字段，以及写回内容中不丢字段。
func TestSyncAndSet(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "sync-config.toml")

	cfg := &sampleConfig{
		Name:    "huatuo-dev",
		Count:   5,
		Enabled: true,
	}
	cfg.Nested.Value = "trace-2026"

	if err := Sync(path, cfg); err != nil {
		t.Errorf("Sync returned error: %v", err)
		return
	}

	Set(cfg, "Name", "huatuo-region")
	Set(cfg, "Nested.Value", "kernel_sched_tick")

	if err := Sync(path, cfg); err != nil {
		t.Errorf("Sync after Set returned error: %v", err)
		return
	}

	reloaded := &sampleConfig{}
	if err := Load(path, reloaded); err != nil {
		t.Errorf("Load after Sync returned error: %v", err)
		return
	}

	if reloaded.Name != "huatuo-region" {
		t.Errorf("unexpected reloaded Name: %q", reloaded.Name)
	}
	if reloaded.Nested.Value != "kernel_sched_tick" {
		t.Errorf("unexpected reloaded nested value: %q", reloaded.Nested.Value)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("read synced file: %v", err)
		return
	}
	if !strings.Contains(string(raw), "name = \"huatuo-region\"") {
		t.Errorf("synced file should contain updated name, got: %s", string(raw))
	}
}
