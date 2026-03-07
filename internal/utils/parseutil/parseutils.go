// Copyright 2025 The HuaTuo Authors
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

package parseutil

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
)

// ReadUint read single value in file
func ReadUint(path string) (uint64, error) {
	v, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}

	val := strings.TrimSpace(string(v))
	if val == "max" {
		return math.MaxUint64, nil
	}

	return strconv.ParseUint(strings.TrimSpace(string(v)), 10, 64)
}

// ReadInt read single value in file
func ReadInt(path string) (int64, error) {
	v, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}

	val := strings.TrimSpace(string(v))
	if val == "max" {
		return math.MaxInt64, nil
	}

	return strconv.ParseInt(strings.TrimSpace(val), 10, 64)
}

func parseKV(raw string) (string, uint64, error) {
	parts := strings.Fields(raw)
	switch len(parts) {
	case 2:
		v, err := strconv.ParseUint(parts[1], 10, 64)
		if err != nil {
			return "", 0, err
		}
		return parts[0], v, nil
	default:
		return "", 0, fmt.Errorf("invalid format")
	}
}

// RawKV parse the kv cgroup file
func RawKV(path string) (map[string]uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var (
		raw = make(map[string]uint64)
		sc  = bufio.NewScanner(f)
	)

	for sc.Scan() {
		key, v, err := parseKV(sc.Text())
		if err != nil {
			return nil, err
		}
		raw[key] = v
	}

	if err := sc.Err(); err != nil {
		return nil, err
	}

	return raw, nil
}

func KV(path string) (string, uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Scan()
	if err := scanner.Err(); err != nil {
		return "", 0, err
	}

	return parseKV(scanner.Text())
}
