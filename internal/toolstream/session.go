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

	"github.com/ccfos/huatuo/internal/toolstream/transport"
)

type sessionKey [2]string

// expectedSession bridges the socket reader that observes the end marker with
// the callers that wait for it.
//
// done is closed exactly once, either by finishSession after the end marker was
// dispatched or by cancelSession when the expectation is dropped before the
// stream ends. closed is written under sessionsMu and guards done against a
// second close, while err accumulates every failure its waiters must observe.
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
//
// It returns nil once the stream ended cleanly and the accumulated tool or
// handler error otherwise. Callers observing ErrSessionCanceled know that
// CancelSession dropped the expectation while this call was waiting.
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
		// Release only the expectation this call was waiting on. A retry may
		// already have registered a fresh expectation under the same key, and
		// canceling that one would fail an operation that never timed out.
		s.cancelSession(key, session)
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
//
// Every goroutine blocked in AwaitSession is woken with ErrSessionCanceled
// instead of sleeping until its context expires, and the expectation is removed
// so a later ExpectSession can register the same tool/task pair again. That
// matters on profiler start failures and FinalizeDiscard, where the discard is
// decisive and a finalize waiter must not linger until the finalization timeout.
func (s *Server) CancelSession(toolName, taskID string) {
	if s == nil {
		return
	}
	key := sessionKey{toolName, taskID}
	s.sessionsMu.Lock()
	session := s.sessions[key]
	s.sessionsMu.Unlock()

	// Canceling a key that was never expected is a no-op: the failure paths call
	// this without knowing whether the expectation was already released.
	s.cancelSession(key, session)
}

// cancelSession releases one specific expectation and wakes its waiters.
//
// It ignores nil, replaced and already finished sessions so a cancel arriving
// late cannot terminate an unrelated expectation, and it closes done outside
// sessionsMu because AwaitSession needs the same mutex to read the result as
// soon as it wakes up.
func (s *Server) cancelSession(key sessionKey, session *expectedSession) {
	if session == nil {
		return
	}

	s.sessionsMu.Lock()
	if s.sessions[key] != session {
		// A newer expectation owns the key now; leave it untouched.
		s.sessionsMu.Unlock()
		return
	}
	if session.closed {
		// The stream already ended, so only the stale expectation is left to
		// drop; done is already closed and no waiter needs another wakeup.
		delete(s.sessions, key)
		s.sessionsMu.Unlock()
		return
	}
	session.closed = true
	session.err = errors.Join(session.err, ErrSessionCanceled)
	delete(s.sessions, key)
	s.sessionsMu.Unlock()

	// Closing outside the lock keeps the broadcast from serializing behind the
	// wakeup bookkeeping that AwaitSession performs right after waking.
	close(session.done)
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
