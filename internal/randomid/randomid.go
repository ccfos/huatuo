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

// Package randomid creates opaque identifiers for internal execution sessions.
package randomid

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

const (
	charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	length  = 16
)

// New returns a cryptographically random identifier.
func New() (string, error) {
	result := make([]byte, length)
	charsetLength := big.NewInt(int64(len(charset)))

	for i := range result {
		num, err := rand.Int(rand.Reader, charsetLength)
		if err != nil {
			return "", fmt.Errorf("generate random ID: %w", err)
		}
		result[i] = charset[num.Int64()]
	}

	return string(result), nil
}
