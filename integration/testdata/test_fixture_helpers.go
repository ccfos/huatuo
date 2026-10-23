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

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

func readJSON(path string, dst any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	return json.Unmarshal(data, dst)
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}

	if path == "" {
		fmt.Println(string(data))
		return nil
	}

	return os.WriteFile(path, data, 0o600)
}

func runConcurrent(workers, rounds int, interval time.Duration, check func() error) error {
	var wg sync.WaitGroup
	// Each worker can fail once; sends must not block before Wait completes.
	errs := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range rounds {
				if err := check(); err != nil {
					errs <- err
					return
				}

				time.Sleep(interval)
			}
		}()
	}

	wg.Wait()
	close(errs)

	return <-errs
}

func collectConcurrent[T any](
	workers, rounds int,
	interval time.Duration,
	collect func(index int) (T, error),
) ([]T, error) {
	var mu sync.Mutex
	var index atomic.Uint64
	samples := make([]T, 0, workers*rounds)
	err := runConcurrent(
		workers,
		rounds,
		interval,
		func() error {
			sample, err := collect(int(index.Add(1)) - 1)
			if err != nil {
				return err
			}

			mu.Lock()
			samples = append(samples, sample)
			mu.Unlock()

			return nil
		},
	)

	return samples, err
}
