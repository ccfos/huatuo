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
)

const (
	charset  = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	length   = 16
	maxByte  = 256 / len(charset) * len(charset) // largest multiple of len(charset) in a byte
	rejectAt = 256 % len(charset)                // bytes >= rejectAt would bias the charset
)

// New returns a cryptographically random identifier.
//
// It samples raw bytes in one crypto/rand read and maps them onto charset
// with rejection sampling, instead of one big.Int allocation per character,
// so generating an ID stays cheap on event-path callers.
func New() (string, error) {
	buf := make([]byte, length)
	needed := length
	for needed > 0 {
		raw := make([]byte, needed)
		if _, err := rand.Read(raw); err != nil {
			return "", fmt.Errorf("generate random ID: %w", err)
		}
		for _, b := range raw {
			if int(b) >= maxByte {
				continue
			}
			buf[length-needed] = charset[b%byte(len(charset))]
			needed--
			if needed == 0 {
				break
			}
		}
	}
	return string(buf), nil
}
