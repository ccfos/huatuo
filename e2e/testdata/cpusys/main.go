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
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "cpusys reference:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("use sample, thresholds, status, concurrent, or events")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	duration := flags.Duration("duration", 4*time.Second, "reference or concurrent query duration")
	interval := flags.Duration("interval", time.Second, "reference sampling interval")
	baseline := flags.String("baseline", "", "idle reference JSONL file")
	busy := flags.String("busy", "", "system workload reference JSONL file")
	url := flags.String("url", "", "bamai metrics URL")
	disabled := flags.Bool("disabled", false, "require cpusys to be absent")
	file := flags.String("file", "", "events JSON array")
	comm := flags.String("comm", "cpusys-load", "expected workload comm")
	usage := flags.Int64("usage", 0, "configured system percentage threshold")
	delta := flags.Int64("delta", 0, "configured system percentage delta threshold")
	cooldown := flags.Duration("cooldown", 0, "minimum interval between events")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	switch args[0] {
	case "sample":
		return sampleUsage(*duration, *interval, os.Stdout)
	case "thresholds":
		thresholds, err := chooseThresholds(*baseline, *busy)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(thresholds)
	case "status":
		return fetchStatus(&http.Client{Timeout: 3 * time.Second}, *url, *disabled)
	case "concurrent":
		if *duration <= 0 {
			return errors.New("concurrent queries require a positive duration")
		}
		ctx, cancel := context.WithTimeout(context.Background(), *duration)
		defer cancel()
		var queries sync.WaitGroup
		errors := make(chan error, 3)
		for range 3 {
			queries.Add(1)
			go func() {
				defer queries.Done()
				client := &http.Client{Timeout: 3 * time.Second}
				ticker := time.NewTicker(250 * time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						if err := fetchStatus(client, *url, false); err != nil {
							errors <- err
							return
						}
					}
				}
			}()
		}
		queries.Wait()
		close(errors)
		for err := range errors {
			return err
		}
		return nil
	case "events":
		data, err := os.ReadFile(*file)
		if err != nil {
			return err
		}
		return checkEvents(data, *comm, *usage, *delta, *cooldown)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func fetchStatus(client *http.Client, url string, disabled bool) error {
	response, err := client.Get(url)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("metrics HTTP status %d", response.StatusCode)
	}
	return checkStatus(response.Body, disabled)
}
