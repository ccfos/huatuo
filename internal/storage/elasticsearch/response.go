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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/elastic/go-elasticsearch/v8/esapi"
	essearch "github.com/elastic/go-elasticsearch/v8/typedapi/core/search"
	"github.com/elastic/go-elasticsearch/v8/typedapi/types"
)

func checkSearchStatus(payload *essearch.Response) error {
	// HTTP success can still carry incomplete hits or aggregations.
	if payload.TimedOut {
		return errors.New("timed out")
	}

	return checkShardStatus(&payload.Shards_)
}

func checkShardStatus(shards *types.ShardStatistics) error {
	if shards.Failed == 0 && len(shards.Failures) == 0 {
		return nil
	}

	if len(shards.Failures) == 0 {
		return fmt.Errorf("failed on %d shards", shards.Failed)
	}

	failure := &shards.Failures[0]
	index := ""
	if failure.Index != nil {
		index = *failure.Index
	}

	return fmt.Errorf("failed on %d shards: index=%s shard=%d: %s",
		shards.Failed, index, failure.Shard, formatErrorCause(&failure.Reason))
}

func responseError(action, target string, res *esapi.Response) error {
	// Proxies may return large non-JSON bodies; keep diagnostics bounded.
	const maxBodyBytes = 4096
	body, err := io.ReadAll(io.LimitReader(res.Body, maxBodyBytes+1))
	if err != nil {
		return fmt.Errorf("elasticsearch %s %s: status %d: read body: %w", action, target, res.StatusCode, err)
	}

	var payload types.ElasticsearchError
	if len(body) <= maxBodyBytes && json.Unmarshal(body, &payload) == nil &&
		(payload.ErrorCause.Type != "" || payload.ErrorCause.Reason != nil ||
			payload.ErrorCause.CausedBy != nil || len(payload.ErrorCause.RootCause) > 0) {
		return fmt.Errorf("elasticsearch %s %s: status %d: %s",
			action, target, res.StatusCode, formatErrorCause(&payload.ErrorCause))
	}

	if len(body) > maxBodyBytes {
		body = append(body[:maxBodyBytes], "... (truncated)"...)
	}

	return fmt.Errorf("elasticsearch %s %s: status %d: %s", action, target, res.StatusCode, strings.TrimSpace(string(body)))
}

func formatErrorCause(cause *types.ErrorCause) string {
	detail := fmt.Sprintf("type=%s", cause.Type)
	if cause.Reason != nil {
		detail += fmt.Sprintf(" reason=%q", *cause.Reason)
	}
	for i := range cause.RootCause {
		detail += "; root_cause: " + formatErrorCause(&cause.RootCause[i])
	}
	if cause.CausedBy != nil {
		detail += "; caused_by: " + formatErrorCause(cause.CausedBy)
	}

	return detail
}
