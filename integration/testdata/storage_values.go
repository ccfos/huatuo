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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	es "github.com/elastic/go-elasticsearch/v8"
	"github.com/elastic/go-elasticsearch/v8/esapi"

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
	readQuery := flag.Bool("read-query", false, "Verify shared read client, keyword resolution, and value counts")
	value := flag.String("value", "", "Optional exact value filter for read-query")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if *readQuery {
		return readValueCounts(ctx, *address, *index, *field, *value, *size)
	}
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

func readValueCounts(ctx context.Context, address, index, field, value string, size int) (err error) {
	client, err := elasticsearch.NewReadClient(&elasticsearch.ReadClientConfig{
		Config: es.Config{Addresses: []string{address}},
	})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, client.Close()) }()
	includeUnmapped := true
	response, err := (esapi.FieldCapsRequest{
		Index: []string{index}, Fields: []string{field, field + ".*"}, IncludeUnmapped: &includeUnmapped,
	}).Do(ctx, client)
	if err != nil {
		return err
	}
	var capabilities elasticsearch.FieldCapabilities
	if err := decodeReadResponse(response, &capabilities); err != nil {
		return err
	}
	mapping := capabilities.ResolveExactField(field)
	if !mapping.Complete {
		return fmt.Errorf("field %s does not have complete exact mapping coverage", field)
	}
	aggregation, err := elasticsearch.TermsAggregation(&elasticsearch.TermsAggregationOptions{
		Field: field, Variants: mapping.Variants, Limit: size, AllowSourceScript: true,
	})
	if err != nil {
		return err
	}
	var query any = map[string]any{"match_all": map[string]any{}}
	if value != "" {
		query, err = elasticsearch.ExactTerms(mapping.Variants, []string{value})
		if err != nil {
			return err
		}
	}
	body, err := json.Marshal(map[string]any{"size": 0, "query": query, "aggs": map[string]any{"values": aggregation}})
	if err != nil {
		return err
	}
	response, err = (esapi.SearchRequest{Index: []string{index}, Body: bytes.NewReader(body)}).Do(ctx, client)
	if err != nil {
		return err
	}
	var result struct {
		TimedOut bool `json:"timed_out"`
		Shards   struct {
			Failed int `json:"failed"`
		} `json:"_shards"`
		Aggregations map[string]json.RawMessage `json:"aggregations"`
	}
	if err := decodeReadResponse(response, &result); err != nil {
		return err
	}
	if result.TimedOut || result.Shards.Failed != 0 {
		return errors.New("value aggregation timed out or has failed shards")
	}
	raw := result.Aggregations["values"]
	if raw == nil {
		raw = result.Aggregations["sterms#values"]
	}
	values, err := elasticsearch.DecodeValueCounts(raw, size)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(values)
}

func decodeReadResponse(response *esapi.Response, target any) error {
	defer response.Body.Close()
	const limit = 1 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return err
	}
	if len(body) > limit {
		return errors.New("storage test response exceeds 1 MiB")
	}
	if response.IsError() {
		return fmt.Errorf("storage test request: status=%d body=%s", response.StatusCode, body)
	}
	return json.Unmarshal(body, target)
}
