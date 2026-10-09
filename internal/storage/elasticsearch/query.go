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

package elasticsearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/elastic/go-elasticsearch/v8/esapi"
	esclearscroll "github.com/elastic/go-elasticsearch/v8/typedapi/core/clearscroll"
	essearch "github.com/elastic/go-elasticsearch/v8/typedapi/core/search"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/storage/driver"
)

// Query consumes a bounded snapshot in batches. Scroll keeps the same protocol
// across ES 7, ES 8, and OpenSearch without exposing server cursors to callers.
func (s *Storage) Query(ctx context.Context, q driver.Query, consume func([]driver.Record) error) error {
	body, err := buildSearchRequest(q)
	if err != nil {
		return err
	}

	const keepAlive = time.Minute
	var scrollID string
	defer func() {
		if scrollID == "" {
			return
		}

		// Cleanup must survive cancellation without keeping the caller blocked indefinitely.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()

		// Scroll IDs can exceed URL limits and contain path separators.
		clearBody, _ := json.Marshal(map[string][]string{"scroll_id": {scrollID}})
		req := esapi.ClearScrollRequest{Body: bytes.NewReader(clearBody)}
		res, clearErr := req.Do(cleanupCtx, s.queryTransport)
		if clearErr != nil {
			log.WithError(clearErr).Warn("failed to clear Elasticsearch query scroll")
			return
		}
		defer res.Body.Close()
		if res.IsError() {
			log.WithError(responseError("clear query scroll", s.index, res)).Warn("failed to release Elasticsearch query scroll")
			return
		}

		// Cleanup failures must not replace the query result or the consumer's error.
		var payload esclearscroll.Response
		if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
			log.WithError(err).WithField("index", s.index).Warn("failed to decode Elasticsearch clear scroll response")
			return
		}

		if !payload.Succeeded {
			log.WithField("index", s.index).Warn("Elasticsearch query scroll cleanup returned succeeded=false")
		}
	}()

	remaining, skip := q.Limit, q.Offset
	for remaining > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}

		var res *esapi.Response
		if scrollID == "" {
			req := esapi.SearchRequest{Index: []string{s.index}, Body: bytes.NewReader(body), Scroll: keepAlive}
			res, err = req.Do(ctx, s.queryTransport)
		} else {
			// Keep long cursor IDs out of the URL, as with the cleanup request.
			scrollBody, _ := json.Marshal(map[string]string{"scroll_id": scrollID})
			req := esapi.ScrollRequest{Body: bytes.NewReader(scrollBody), Scroll: keepAlive}
			res, err = req.Do(ctx, s.queryTransport)
		}
		if err != nil {
			return fmt.Errorf("elasticsearch backend query %s: %w", s.index, err)
		}

		if scrollID == "" && res.StatusCode == http.StatusNotFound {
			res.Body.Close()
			return nil
		}

		if res.IsError() {
			err = responseError("query documents", s.index, res)
			res.Body.Close()
			return err
		}

		var payload essearch.Response
		err = json.NewDecoder(res.Body).Decode(&payload)
		res.Body.Close()
		if payload.ScrollId_ != nil && *payload.ScrollId_ != "" {
			scrollID = *payload.ScrollId_
		}
		if err != nil {
			return fmt.Errorf("elasticsearch backend query %s: decode: %w", s.index, err)
		}
		if err := checkSearchStatus(&payload); err != nil {
			return fmt.Errorf("elasticsearch backend query %s: %w", s.index, err)
		}

		hits := payload.Hits.Hits
		if len(hits) == 0 {
			return nil
		}

		start := min(skip, len(hits))
		skip -= start
		end := start + min(remaining, len(hits)-start)
		if start < end {
			records := make([]driver.Record, 0, end-start)
			for i := start; i < end; i++ {
				hit := &hits[i]
				var id string
				if hit.Id_ != nil {
					id = *hit.Id_
				}

				// Each decoded RawMessage owns its bytes; transfer them to the consumer.
				records = append(records, driver.Record{ID: id, Data: hit.Source_})
			}

			if err := ctx.Err(); err != nil {
				return err
			}
			if err := consume(records); err != nil {
				return err
			}

			remaining -= len(records)
		}

		if remaining > 0 && (payload.ScrollId_ == nil || *payload.ScrollId_ == "") {
			return fmt.Errorf("elasticsearch backend query %s: missing scroll ID before results are exhausted", s.index)
		}
	}

	return ctx.Err()
}
