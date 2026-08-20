package helper

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStreamScannerHandlerResponsesGraceCapturesTerminalAfterClientCancel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldTimeout := constant.StreamingTimeout
	if oldTimeout == 0 {
		constant.StreamingTimeout = 30
	}
	t.Cleanup(func() {
		constant.StreamingTimeout = oldTimeout
	})

	pr, pw := io.Pipe()
	t.Cleanup(func() {
		_ = pr.Close()
		_ = pw.Close()
	})

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(requestCtx)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       pr,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}
	info := &relaycommon.RelayInfo{
		RelayMode:      relayconstant.RelayModeResponses,
		RequestURLPath: "/v1/responses",
		DisablePing:    true,
	}

	firstHandled := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		StreamScannerHandler(c, resp, info, func(data string, sr *StreamResult) {
			if strings.Contains(data, "response.output_text.delta") {
				select {
				case <-firstHandled:
				default:
					close(firstHandled)
				}
			}
			if strings.Contains(data, "response.completed") {
				sr.Done()
			}
		})
	}()

	_, err := fmt.Fprint(pw, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n")
	require.NoError(t, err)

	select {
	case <-firstHandled:
	case <-time.After(time.Second):
		t.Fatal("first Responses event was not handled")
	}

	cancelRequest()
	// Simulate a terminal event already in flight from a slow-closing upstream.
	time.Sleep(50 * time.Millisecond)
	_, err = fmt.Fprint(pw, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n")
	require.NoError(t, err)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream handler did not finish during Responses disconnect grace")
	}

	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonDone, info.StreamStatus.EndReason)
}
