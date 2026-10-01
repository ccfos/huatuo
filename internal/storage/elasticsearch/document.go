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
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ccfos/huatuo/internal/storage/driver"
)

func encodeDocument(record driver.Record) ([]byte, error) {
	if len(record.Fields) == 0 {
		return driver.CloneBytes(record.Data), nil
	}

	var document map[string]any
	decoder := json.NewDecoder(bytes.NewReader(record.Data))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode document: %w", err)
	}
	if document == nil {
		document = make(map[string]any)
	}

	for field, value := range record.Fields {
		if err := setDocumentField(document, field, driver.NormalizeValue(value)); err != nil {
			return nil, err
		}
	}
	return json.Marshal(document)
}

func setDocumentField(document map[string]any, field string, value any) error {
	parts := strings.Split(field, ".")
	if field == "" {
		return fmt.Errorf("empty document field")
	}
	for _, part := range parts {
		if part == "" {
			return fmt.Errorf("invalid document field %q", field)
		}
	}

	current := document
	for _, part := range parts[:len(parts)-1] {
		next, ok := current[part].(map[string]any)
		if !ok {
			next = make(map[string]any)
			current[part] = next
		}
		current = next
	}
	current[parts[len(parts)-1]] = value
	return nil
}
