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
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "metrics reader:", err)
		os.Exit(1)
	}
}

func run() error {
	url := flag.String("url", "", "Prometheus metrics URL")
	raw := flag.String("raw-output", "", "raw response and HTTP diagnostic path")
	flag.Parse()
	families, err := fetch(&http.Client{Timeout: 5 * time.Second}, *url, *raw)
	if err != nil {
		return err
	}

	return json.NewEncoder(os.Stdout).Encode(metricJSON(families))
}
