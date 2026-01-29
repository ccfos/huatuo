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

package elasticsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"huatuo-bamai/internal/storage/types"

	"github.com/elastic/go-elasticsearch/v7"
	"github.com/elastic/go-elasticsearch/v7/esapi"
)

const (
	defaultIndex = "huatuo_bamai"
)

var defaultTransport http.RoundTripper = &http.Transport{
	MaxIdleConnsPerHost:   10,
	ResponseHeaderTimeout: 10 * time.Second,
	DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
}

type StorageClient struct {
	client *elasticsearch.Client
	index  string
}

func NewStorageClient(addr, username, password, index string) (*StorageClient, error) {
	cfg := elasticsearch.Config{
		Addresses: []string{addr},
		Username:  username,
		Password:  password,
		Transport: defaultTransport,
	}

	client, err := elasticsearch.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("new client: %w", err)
	}

	// ping/check es server ...
	res, err := client.Info()
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.IsError() {
		return nil, fmt.Errorf("elasticsearch return statuscode: %d", res.StatusCode)
	}

	if index == "" {
		index = defaultIndex
	}
	return &StorageClient{client: client, index: index}, nil
}

// Write the data into ES.
func (e *StorageClient) Write(doc *types.Document) error {
	data, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("json Marshal: %w", err)
	}
	req := esapi.IndexRequest{
		Index:      e.index,
		DocumentID: "",
		Body:       strings.NewReader(string(data)),
	}

	res, err := req.Do(context.Background(), e.client)
	if err != nil {
		return fmt.Errorf("error getting response: %w", err)
	}
	defer res.Body.Close()

	// Check the response status code
	if res.IsError() {
		body, _ := io.ReadAll(res.Body)
		return fmt.Errorf("index document failed with status: %d, response: %s; error: %s",
			res.StatusCode, res.String(), string(body))
	}

	var r map[string]any
	if err := json.NewDecoder(res.Body).Decode(&r); err != nil {
		return fmt.Errorf("parse response body: %w", err)
	}

	return nil
}
