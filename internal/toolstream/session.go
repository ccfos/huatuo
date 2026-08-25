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

package toolstream

import (
	"context"
	"errors"
	"fmt"

	"huatuo-bamai/internal/toolstream/transport"
)

type sessionKey [2]string

type expectedSession struct {
	done   chan struct{}
	err    error
	closed bool
}

// ExpectSession registers a result stream before its process starts.
func (s *Server) ExpectSession(toolName, taskID string) error {
	if s == nil {
		return ErrNotInitialized
	}
	if toolName == "" || taskID == "" {
		return errors.New("toolstream: expected session requires tool name and task ID")
	}
	key := sessionKey{toolName, taskID}
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	if _, exists := s.sessions[key]; exists {
		return fmt.Errorf("toolstream: session %s/%s is already expected", toolName, taskID)
	}
	s.sessions[key] = &expectedSession{done: make(chan struct{})}
	return nil
}

// AwaitSession waits until the stream end marker follows all event handlers.
func (s *Server) AwaitSession(ctx context.Context, toolName, taskID string) error {
	if s == nil {
		return ErrNotInitialized
	}
	key := sessionKey{toolName, taskID}
	s.sessionsMu.Lock()
	session, exists := s.sessions[key]
	s.sessionsMu.Unlock()
	if !exists {
		return fmt.Errorf("toolstream: session %s/%s was not expected", toolName, taskID)
	}

	select {
	case <-ctx.Done():
		s.CancelSession(toolName, taskID)
		return fmt.Errorf("toolstream: wait for session %s/%s: %w", toolName, taskID, ctx.Err())
	case <-session.done:
	}

	s.sessionsMu.Lock()
	if s.sessions[key] == session {
		delete(s.sessions, key)
	}
	err := session.err
	s.sessionsMu.Unlock()
	if err != nil {
		return fmt.Errorf("toolstream: session %s/%s: %w", toolName, taskID, err)
	}
	return nil
}

// CancelSession drops a session expectation that cannot reach a clean end.
func (s *Server) CancelSession(toolName, taskID string) {
	if s == nil {
		return
	}
	key := sessionKey{toolName, taskID}
	s.sessionsMu.Lock()
	delete(s.sessions, key)
	s.sessionsMu.Unlock()
}

func (s *Server) isExpectedSession(session *transport.Session) bool {
	if session == nil {
		return false
	}
	key := sessionKey{session.ToolName, session.TaskID}
	s.sessionsMu.Lock()
	_, exists := s.sessions[key]
	s.sessionsMu.Unlock()
	return exists
}

func (s *Server) recordSessionError(session *Session, err error) {
	if session == nil || err == nil {
		return
	}
	key := sessionKey{session.ToolName, session.TaskID}
	s.sessionsMu.Lock()
	if expected := s.sessions[key]; expected != nil && !expected.closed {
		expected.err = errors.Join(expected.err, err)
	}
	s.sessionsMu.Unlock()
}

func (s *Server) finishSession(session *Session) {
	if session == nil {
		return
	}
	key := sessionKey{session.ToolName, session.TaskID}
	s.sessionsMu.Lock()
	if expected := s.sessions[key]; expected != nil && !expected.closed {
		expected.closed = true
		close(expected.done)
	}
	s.sessionsMu.Unlock()
}
