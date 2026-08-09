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
	"io"
	"net/http"
	"net/url"
	"strings"

	"huatuo-bamai/internal/storage/driver"
)

func (s *Storage) initIndexLifecycle(ctx context.Context) error {
	policyName := s.index + "-policy"
	templateName := s.index + "-template"
	policy := map[string]any{"policy": map[string]any{"phases": map[string]any{
		"hot":    map[string]any{"actions": map[string]any{"rollover": map[string]any{"max_age": "1d"}}},
		"delete": map[string]any{"min_age": fmt.Sprintf("%dd", s.ilmRetentionDays), "actions": map[string]any{"delete": map[string]any{}}},
	}}}
	if err := s.lifecycleRequest(ctx, http.MethodPut, "/_ilm/policy/"+url.PathEscape(policyName), policy, http.StatusOK); err != nil {
		return fmt.Errorf("configure ILM policy: %w", err)
	}
	template := map[string]any{
		"index_patterns": []string{s.index + "-*"},
		"template": map[string]any{"settings": map[string]any{
			"index.lifecycle.name":           policyName,
			"index.lifecycle.rollover_alias": s.index,
		}},
	}
	if err := s.lifecycleRequest(ctx, http.MethodPut, "/_index_template/"+url.PathEscape(templateName), template, http.StatusOK); err != nil {
		return fmt.Errorf("configure ILM index template: %w", err)
	}

	bootstrap := s.index + "-000001"
	exists, err := s.lifecycleIndexExists(ctx, bootstrap)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	body := map[string]any{"aliases": map[string]any{s.index: map[string]any{"is_write_index": true}}}
	if err := s.lifecycleRequest(ctx, http.MethodPut, "/"+url.PathEscape(bootstrap), body, http.StatusOK); err != nil {
		return fmt.Errorf("create ILM bootstrap index: %w", err)
	}
	return nil
}

func (s *Storage) lifecycleIndexExists(ctx context.Context, index string) (bool, error) {
	req, err := http.NewRequestWithContext(driver.WithContext(ctx), http.MethodHead, "/"+url.PathEscape(index), http.NoBody)
	if err != nil {
		return false, err
	}
	res, err := s.transport.Perform(req)
	if err != nil {
		return false, fmt.Errorf("check ILM bootstrap index: %w", err)
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	if res.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return false, fmt.Errorf("check ILM bootstrap index: status %d", res.StatusCode)
	}
	return true, nil
}

func (s *Storage) lifecycleRequest(ctx context.Context, method, path string, payload any, expected int) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(driver.WithContext(ctx), method, path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := s.transport.Perform(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != expected {
		message, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return fmt.Errorf("status %d: %s", res.StatusCode, strings.TrimSpace(string(message)))
	}
	_, _ = io.Copy(io.Discard, res.Body)
	return nil
}
