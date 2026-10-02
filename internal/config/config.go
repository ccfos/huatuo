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

// Sync atomically replaces path with the TOML encoding of src. Callers must
// prevent concurrent mutation of src until Sync returns.
func Sync(path string, src any) (retErr error) {
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

	mode := os.FileMode(0o600)
	info, err := os.Stat(path)
	switch {
	case err == nil:
		mode = info.Mode().Perm()
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("stating config file: %w", err)
	}
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
