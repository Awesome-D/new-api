package common

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStreamStatusCompletionOverridesSpuriousClientGone(t *testing.T) {
	for _, completed := range []StreamEndReason{StreamEndReasonDone, StreamEndReasonEOF} {
		s := NewStreamStatus()
		s.SetEndReason(StreamEndReasonClientGone, context.Canceled)
		s.SetEndReason(completed, nil)

		assert.Equalf(t, completed, s.EndReason, "completion %s must override earlier client_gone", completed)
		assert.Nilf(t, s.EndError, "completion %s must clear the client_gone error", completed)
		assert.Truef(t, s.IsNormalEnd(), "completion %s must read as a normal end", completed)
	}
}

func TestStreamStatusGenuineDisconnectKeepsClientGone(t *testing.T) {
	s := NewStreamStatus()
	s.SetEndReason(StreamEndReasonClientGone, context.Canceled)
	s.SetEndReason(StreamEndReasonScannerErr, io.ErrClosedPipe)

	assert.Equal(t, StreamEndReasonClientGone, s.EndReason)
	assert.False(t, s.IsNormalEnd())
}
