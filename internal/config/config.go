package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	"github.com/pelletier/go-toml"
)

var (
	// CoreBinDir is the directory where cmd binaries are stored.
	CoreBinDir = ""

	lock = sync.Mutex{}
)

func init() {
	if exePath, err := os.Executable(); err == nil {
		CoreBinDir = filepath.Dir(exePath)
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

// Set modifies a field in cfg by dot-separated key.
func Set(cfg any, key string, val any) {
	lock.Lock()
	defer lock.Unlock()

	c := reflect.ValueOf(cfg)
	for _, k := range strings.Split(key, ".") {
		elem := c.Elem().FieldByName(k)
		if !elem.IsValid() || !elem.CanAddr() {
			panic(fmt.Errorf("invalid elem %s: %v", key, elem))
		}
		c = elem.Addr()
	}

	rc := reflect.Indirect(c)
	rval := reflect.ValueOf(val)
	if rc.Kind() != rval.Kind() {
		panic(fmt.Errorf("%s type %s is not assignable to type %s", key, rc.Kind(), rval.Kind()))
	}

	rc.Set(rval)
}
