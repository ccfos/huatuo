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

package doris

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"huatuo-bamai/internal/log"
)

const (
	// requestTimeout bounds one Stream Load. Batches are capped well below
	// what a healthy cluster ingests in this long.
	requestTimeout = 2 * time.Minute
	maxRedirects   = 10
)

// Backoff bounds; variables so tests can shrink the schedule.
var (
	retryInitialInterval = time.Second
	retryMaxInterval     = 30 * time.Second
)

// streamLoadResponse is the subset of the Doris Stream Load reply we act on.
type streamLoadResponse struct {
	Status          string `json:"Status"`
	Message         string `json:"Message"`
	NumberTotalRows int64  `json:"NumberTotalRows"`
	ErrorURL        string `json:"ErrorURL"`
}

// streamLoader writes batches through the Doris Stream Load HTTP endpoint.
//
// Stream Load is used rather than INSERT because each row carries a
// multi-megabyte profile payload, and because every load is one transaction:
// batching keeps Doris from accumulating versions faster than compaction
// retires them.
//
// No load label is sent. Doris deduplicates by label, but the tables this
// backend creates are unique-key models and a retry re-sends byte-identical
// rows, so the key already collapses a batch that landed twice. Skipping the
// label also keeps group commit usable, which rejects a caller-supplied one.
type streamLoader struct {
	client      *http.Client
	feAddr      string
	database    string
	table       string
	username    string
	password    string
	maxRetries  int
	groupCommit string
}

func newStreamLoader(feAddr, database, table, username, password string, maxRetries int, groupCommit string) *streamLoader {
	return &streamLoader{
		client: &http.Client{
			Timeout: requestTimeout,
			// The FE answers with a 307 pointing at a BE, usually on a
			// different host. Go strips Authorization across hosts, so the
			// BE would see an unauthenticated request; re-attach it.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) == 0 {
					return nil
				}
				if auth := via[0].Header.Get("Authorization"); auth != "" {
					req.Header.Set("Authorization", auth)
				}
				if len(via) >= maxRedirects {
					return fmt.Errorf("doris stream load: too many redirects")
				}
				return nil
			},
		},
		feAddr:      feAddr,
		database:    database,
		table:       table,
		username:    username,
		password:    password,
		maxRetries:  maxRetries,
		groupCommit: groupCommit,
	}
}

// permanentError marks a failure that retrying cannot fix: the request or the
// data is wrong, so every attempt fails the same way.
type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

func newPermanentError(format string, args ...any) error {
	return &permanentError{err: fmt.Errorf(format, args...)}
}

func isPermanentError(err error) bool {
	var permanent *permanentError
	return errors.As(err, &permanent)
}

// load sends rows as a JSON array, retrying transient failures.
// Callers must not pass an empty slice.
func (l *streamLoader) load(ctx context.Context, rows []map[string]any) error {
	payload, err := json.Marshal(rows)
	if err != nil {
		return fmt.Errorf("doris stream load: marshal rows: %w", err)
	}

	backoff := retryInitialInterval
	for attempt := 0; ; attempt++ {
		// Every attempt sends the same bytes, which is what makes a retry
		// safe against the unique key.
		err := l.send(ctx, payload)
		if err == nil {
			return nil
		}
		if isPermanentError(err) || attempt >= l.maxRetries {
			return err
		}

		log.Warnf("doris stream load: attempt %d failed, retrying in %s: %v", attempt+1, backoff, err)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(ctx.Err(), err)
		case <-timer.C:
		}

		if backoff *= 2; backoff > retryMaxInterval {
			backoff = retryMaxInterval
		}
	}
}

func (l *streamLoader) send(ctx context.Context, payload []byte) error {
	url := fmt.Sprintf("http://%s/api/%s/%s/_stream_load", l.feAddr, l.database, l.table)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(payload))
	if err != nil {
		return newPermanentError("doris stream load: new request: %w", err)
	}

	req.SetBasicAuth(l.username, l.password)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Expect", "100-continue")
	req.Header.Set("format", "json")
	req.Header.Set("strip_outer_array", "true")
	req.Header.Set("fuzzy_parse", "false")
	if l.groupCommit != "" && l.groupCommit != GroupCommitOff {
		req.Header.Set("group_commit", l.groupCommit)
	}
	req.ContentLength = int64(len(payload))

	resp, err := l.client.Do(req)
	if err != nil {
		return fmt.Errorf("doris stream load: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("doris stream load: read response: %w", err)
	}
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return newPermanentError("doris stream load: http %d: %s", resp.StatusCode, truncate(string(body)))
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("doris stream load: http %d: %s", resp.StatusCode, truncate(string(body)))
	}

	var result streamLoadResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("doris stream load: decode response %q: %w", truncate(string(body)), err)
	}

	return classifyStreamLoadResult(&result)
}

// classifyStreamLoadResult maps a Stream Load reply onto success, a permanent
// failure, or a retryable one.
func classifyStreamLoadResult(result *streamLoadResponse) error {
	switch result.Status {
	case "Success":
		return nil
	// The transaction committed; only visibility is still settling, so the
	// rows are not lost and re-sending would be wasted work.
	case "Publish Timeout":
		return nil
	}

	if isPermanentStreamLoadMessage(result.Message) {
		return newPermanentError("doris stream load: status=%s message=%s error_url=%s",
			result.Status, result.Message, result.ErrorURL)
	}

	return fmt.Errorf("doris stream load: status=%s message=%s error_url=%s",
		result.Status, result.Message, result.ErrorURL)
}

// isPermanentStreamLoadMessage recognises the Doris error families that describe
// bad data or a bad request rather than a transient server condition.
func isPermanentStreamLoadMessage(message string) bool {
	upper := strings.ToUpper(message)
	for _, marker := range []string{"[DATA_QUALITY_ERROR]", "[INVALID_ARGUMENT]", "[NOT_AUTHORIZED]", "[ANALYSIS_ERROR]"} {
		if strings.Contains(upper, marker) {
			return true
		}
	}

	return false
}

func truncate(s string) string {
	const max = 512
	if len(s) <= max {
		return s
	}

	return s[:max] + "..."
}
