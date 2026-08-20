package common

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

type StreamEndReason string

const (
	StreamEndReasonNone        StreamEndReason = ""
	StreamEndReasonDone        StreamEndReason = "done"
	StreamEndReasonTimeout     StreamEndReason = "timeout"
	StreamEndReasonClientGone  StreamEndReason = "client_gone"
	StreamEndReasonScannerErr  StreamEndReason = "scanner_error"
	StreamEndReasonHandlerStop StreamEndReason = "handler_stop"
	StreamEndReasonEOF         StreamEndReason = "eof"
	StreamEndReasonPanic       StreamEndReason = "panic"
	StreamEndReasonPingFail    StreamEndReason = "ping_fail"
)

const maxStreamErrorEntries = 20

type StreamErrorEntry struct {
	Message   string
	Timestamp time.Time
}

type StreamStatus struct {
	EndReason StreamEndReason
	EndError  error

	mu         sync.Mutex
	reasonSet  bool
	Errors     []StreamErrorEntry
	ErrorCount int
}

func NewStreamStatus() *StreamStatus {
	return &StreamStatus{}
}

func isHandlerTerminalReason(reason StreamEndReason) bool {
	return reason == StreamEndReasonDone ||
		reason == StreamEndReasonHandlerStop ||
		reason == StreamEndReasonPanic
}

// SetEndReason records why the stream ended. The first reason normally wins,
// but later protocol-level terminal information is allowed to correct an
// earlier transport-level observation.
//
// Two races are intentionally handled here:
//   1. Codex can close immediately after a final Responses event, so a later
//      Done/EOF may correct an earlier client_gone.
//   2. The scanner can enqueue the final SSE event and reach EOF before the
//      handler goroutine processes that queued event. A later Done/HandlerStop
//      is more specific than that transport EOF and must replace it.
//
// A genuine mid-stream client disconnect still stays client_gone because the
// forced body close surfaces as scanner_error rather than a clean protocol
// terminal event.
func (s *StreamStatus) SetEndReason(reason StreamEndReason, err error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.reasonSet {
		switch s.EndReason {
		case StreamEndReasonClientGone:
			if reason == StreamEndReasonDone || reason == StreamEndReasonEOF || reason == StreamEndReasonHandlerStop || reason == StreamEndReasonPanic {
				s.EndReason = reason
				s.EndError = err
			}
		case StreamEndReasonEOF:
			if isHandlerTerminalReason(reason) {
				s.EndReason = reason
				s.EndError = err
			}
		}
		return
	}

	s.reasonSet = true
	s.EndReason = reason
	s.EndError = err
}

func (s *StreamStatus) RecordError(msg string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ErrorCount++
	if len(s.Errors) < maxStreamErrorEntries {
		s.Errors = append(s.Errors, StreamErrorEntry{
			Message:   msg,
			Timestamp: time.Now(),
		})
	}
}

func (s *StreamStatus) HasErrors() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ErrorCount > 0
}

func (s *StreamStatus) TotalErrorCount() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ErrorCount
}

func (s *StreamStatus) IsNormalEnd() bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.EndReason == StreamEndReasonDone ||
		s.EndReason == StreamEndReasonEOF ||
		s.EndReason == StreamEndReasonHandlerStop
}

func (s *StreamStatus) Summary() string {
	if s == nil {
		return "StreamStatus<nil>"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b := &strings.Builder{}
	fmt.Fprintf(b, "reason=%s", s.EndReason)
	if s.EndError != nil {
		fmt.Fprintf(b, " end_error=%q", s.EndError.Error())
	}
	if s.ErrorCount > 0 {
		fmt.Fprintf(b, " soft_errors=%d", s.ErrorCount)
	}
	return b.String()
}
