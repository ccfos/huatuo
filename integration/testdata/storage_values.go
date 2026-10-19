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
	"os"
	"time"

	"github.com/ccfos/huatuo/internal/storage/driver"
	"github.com/ccfos/huatuo/internal/storage/elasticsearch"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() (err error) {
	address := flag.String("address", "", "Storage server address")
	index := flag.String("index", "", "Index to query")
	field := flag.String("field", "", "Field to aggregate")
	size := flag.Int("size", 10, "Maximum number of values")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	backend, err := elasticsearch.NewBackend(&elasticsearch.Config{
		Addresses: []string{*address},
		Index:     *index,
	})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, backend.Close(ctx)) }()

	values, err := backend.Values(ctx, *field, driver.Query{}, *size)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(values)
}
