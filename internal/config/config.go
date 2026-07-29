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
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	"github.com/pelletier/go-toml"
)

var (
	// CoreBinDir is the directory where cmd binaries are stored (including bamai, profiler etc.).
	CoreBinDir = ""
	// CoreBpfDir is the directory where BPF object files are stored.
	CoreBpfDir = ""

	lock = sync.Mutex{}
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

// Sync encodes src as toml and writes it to path.
func Sync(path string, src any) error {
	lock.Lock()
	defer lock.Unlock()

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	return toml.NewEncoder(f).Encode(src)
}

// Set modifies a field in cfg (a pointer to a struct) by dot-separated key.
func Set(cfg any, key string, val any) error {
	lock.Lock()
	defer lock.Unlock()

	c := reflect.ValueOf(cfg)
	for _, k := range strings.Split(key, ".") {
		elem := c.Elem().FieldByName(k)
		if !elem.IsValid() || !elem.CanAddr() {
			return fmt.Errorf("invalid elem %s: %v", key, elem)
		}
		c = elem.Addr()
	}

	rc := reflect.Indirect(c)
	rval := reflect.ValueOf(val)
	if rc.Kind() != rval.Kind() {
		converted, ok := convertJSONNumber(rc, rval)
		if !ok {
			return fmt.Errorf("%s type %s is not assignable to type %s", key, rval.Kind(), rc.Kind())
		}
		rval = converted
	}

	rc.Set(rval)
	return nil
}

// convertJSONNumber converts the float64 representation produced by encoding/json
// to the destination's numeric type without truncation or overflow.
func convertJSONNumber(dst, src reflect.Value) (reflect.Value, bool) {
	if src.Kind() != reflect.Float64 {
		return reflect.Value{}, false
	}

	f := src.Float()
	converted := reflect.New(dst.Type()).Elem()
	switch dst.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if math.Trunc(f) != f ||
			f < float64(math.MinInt64) ||
			f >= -float64(math.MinInt64) {
			return reflect.Value{}, false
		}
		value := int64(f)
		if dst.OverflowInt(value) {
			return reflect.Value{}, false
		}
		converted.SetInt(value)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if math.Trunc(f) != f || f < 0 || f >= math.Ldexp(1, 64) {
			return reflect.Value{}, false
		}
		value := uint64(f)
		if dst.OverflowUint(value) {
			return reflect.Value{}, false
		}
		converted.SetUint(value)
	case reflect.Float32, reflect.Float64:
		if dst.OverflowFloat(f) {
			return reflect.Value{}, false
		}
		converted.SetFloat(f)
	default:
		return reflect.Value{}, false
	}

	return converted, true
}
