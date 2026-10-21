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

package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/pelletier/go-toml"
)

var (
	// CoreBinDir is the directory where cmd binaries are stored (including bamai, profiler etc.).
	CoreBinDir = ""
	// CoreBpfDir is the directory where BPF object files are stored.
	CoreBpfDir = ""
)

func init() {
	if exePath, err := os.Executable(); err == nil {
		CoreBinDir = filepath.Dir(exePath)
		CoreBpfDir = filepath.Join(filepath.Dir(CoreBinDir), "bpf")
	}
}

// Load decodes a toml file into dst using strict mode.
func Load(path string, dst any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	return toml.NewDecoder(f).Strict(true).Decode(dst)
}

// maxSymlinkDepth bounds symlink chain resolution. Linux caps a single lookup
// at 40 links; a wider limit keeps long deployment chains working while still
// stopping cycles.
const maxSymlinkDepth = 255

// resolveConfigPath follows the symlink chain at path and returns the file a
// write should replace. Relative targets are resolved against the directory
// holding the link, matching kernel lookup. A missing final target is returned
// unchanged so the first write creates the file the link points at, which is
// how a deployment bootstraps "active.toml -> target.toml" before target.toml
// exists. Cycles fail with the configured path in the error.
func resolveConfigPath(path string) (string, error) {
	configured := path

	for range maxSymlinkDepth {
		info, err := os.Lstat(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return path, nil
			}
			return "", fmt.Errorf("inspecting config path %q: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			return path, nil
		}

		target, err := os.Readlink(path)
		if err != nil {
			return "", fmt.Errorf("reading config symlink %q: %w", path, err)
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		path = target
	}

	return "", fmt.Errorf(
		"resolving config path %q: too many levels of symbolic links (at %q)",
		configured, path,
	)
}

// Sync atomically replaces the configuration target with the TOML encoding of
// src, following existing symlinks. A dangling link is created through: its
// target is written as a new file. Callers must prevent concurrent mutation of
// src until Sync returns. Sync does not coordinate concurrent filesystem
// changes by other processes.
func Sync(path string, src any) (retErr error) {
	// Rename must replace the target rather than the configured link.
	resolved, err := resolveConfigPath(path)
	if err != nil {
		return err
	}
	path = resolved

	mode := os.FileMode(0o600)
	if info, statErr := os.Stat(path); statErr == nil {
		mode = info.Mode().Perm()
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("inspecting config target %q: %w", path, statErr)
	}

	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("creating temporary config file: %w", err)
	}
	tempPath := f.Name()
	defer func() {
		if retErr == nil {
			return
		}
		if err := f.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			retErr = errors.Join(retErr, fmt.Errorf("closing temporary config file: %w", err))
		}
		if err := os.Remove(tempPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			retErr = errors.Join(retErr, fmt.Errorf("removing temporary config file: %w", err))
		}
	}()

	if err := f.Chmod(mode); err != nil {
		return fmt.Errorf("setting temporary config file permissions: %w", err)
	}

	if err := toml.NewEncoder(f).Encode(src); err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("syncing temporary config file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing temporary config file: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replacing config file: %w", err)
	}

	return nil
}

// Set modifies an unpublished config value by dot-separated key. Callers must
// provide exclusive access to cfg until Set returns.
func Set(cfg any, key string, val any) error {
	c := reflect.ValueOf(cfg)
	if c.Kind() != reflect.Pointer || c.IsNil() {
		return errors.New("config must be a non-nil pointer")
	}

	parts := strings.Split(key, ".")
	for i, part := range parts {
		c = reflect.Indirect(c)
		if c.Kind() != reflect.Struct {
			return fmt.Errorf("config path %q does not refer to a struct", strings.Join(parts[:i], "."))
		}

		field := c.FieldByName(part)
		if !field.IsValid() || !field.CanSet() {
			return fmt.Errorf("config field %q does not exist or is not settable", key)
		}
		c = field
	}

	value := reflect.ValueOf(val)
	if !value.IsValid() {
		return fmt.Errorf("config field %q cannot be assigned nil", key)
	}
	if raw, ok := val.(json.RawMessage); ok {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fmt.Errorf("config field %q cannot be assigned null", key)
		}
		decoded := reflect.New(c.Type())
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(decoded.Interface()); err != nil {
			return fmt.Errorf("decoding config field %q as %s: %w", key, c.Type(), err)
		}
		c.Set(decoded.Elem())
		return nil
	}
	if value.Type().AssignableTo(c.Type()) {
		c.Set(value)
		return nil
	}

	return fmt.Errorf("config field %q requires %s, got %s", key, c.Type(), value.Type())
}
