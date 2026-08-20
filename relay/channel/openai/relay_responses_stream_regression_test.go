package openai

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newResponsesStreamTestInfo(model string, estimatePromptTokens int) *relaycommon.RelayInfo {
	info := &relaycommon.RelayInfo{
		OriginModelName: model,
		DisablePing:     true,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: model,
		},
	}
	info.SetEstimatePromptTokens(estimatePromptTokens)
	return info
}

func runResponsesStreamBody(t *testing.T, info *relaycommon.RelayInfo, body string) (*relaycommon.RelayInfo, int, int, int, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	service.InitTokenEncoders()
	oldTimeout := constant.StreamingTimeout
	if oldTimeout == 0 {
		constant.StreamingTimeout = 30
	}
	t.Cleanup(func() {
		constant.StreamingTimeout = oldTimeout
	})

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}
	usage, apiErr := OaiResponsesStreamHandler(c, info, resp)
	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	return info, usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens, recorder.Body.String()
}

func TestOaiResponsesStreamHandlerMarksDoneOnCompleted(t *testing.T) {
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
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       pr,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}
	info := newResponsesStreamTestInfo("gpt-5.1", 11)

	done := make(chan struct{})
	var promptTokens, completionTokens int
	go func() {
		defer close(done)
		usage, apiErr := OaiResponsesStreamHandler(c, info, resp)
		assert.Nil(t, apiErr)
		if assert.NotNil(t, usage) {
			promptTokens = usage.PromptTokens
			completionTokens = usage.CompletionTokens
		}
	}()

	_, err := fmt.Fprint(pw, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n")
	require.NoError(t, err)
	_, err = fmt.Fprint(pw, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":5,\"output_tokens\":7,\"total_tokens\":12}}}\n")
	require.NoError(t, err)

	// Keep the pipe open. The handler must finish on response.completed itself
	// instead of waiting for upstream EOF or a non-existent [DONE] sentinel.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return after response.completed")
	}

	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonDone, info.StreamStatus.EndReason)
	assert.Equal(t, 5, promptTokens)
	assert.Equal(t, 7, completionTokens)
	assert.Contains(t, recorder.Body.String(), "response.completed")
}

func TestOaiResponsesStreamHandlerChargesEstimatedPromptWhenUsageMissing(t *testing.T) {
	info := newResponsesStreamTestInfo("gpt-5.1", 1234)
	body := "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n"

	info, prompt, completion, total, _ := runResponsesStreamBody(t, info, body)
	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonDone, info.StreamStatus.EndReason)
	assert.Equal(t, 1234, prompt)
	assert.Equal(t, 0, completion)
	assert.Equal(t, 1234, total)
}

func TestOaiResponsesStreamHandlerCountsToolArgumentsForFallback(t *testing.T) {
	info := newResponsesStreamTestInfo("gpt-5.1", 321)
	body := strings.Join([]string{
		"data: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"{\\\"path\\\":\\\"/tmp/a\\\"}\"}",
		"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}",
		"",
	}, "\n")

	_, prompt, completion, total, _ := runResponsesStreamBody(t, info, body)
	assert.Equal(t, 321, prompt)
	assert.Greater(t, completion, 0)
	assert.Equal(t, prompt+completion, total)
}

func TestOaiResponsesStreamHandlerUsesTotalOnlyUsageInsteadOfZero(t *testing.T) {
	info := newResponsesStreamTestInfo("gpt-5.1", 111)
	body := "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"total_tokens\":5000}}}\n"

	_, prompt, completion, total, _ := runResponsesStreamBody(t, info, body)
	assert.Equal(t, 5000, prompt)
	assert.Equal(t, 0, completion)
	assert.Equal(t, 5000, total)
}

func TestOaiResponsesStreamHandlerIncompleteStillUsesPromptFallback(t *testing.T) {
	info := newResponsesStreamTestInfo("gpt-5.1", 777)
	body := "data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\"}}\n"

	info, prompt, completion, total, _ := runResponsesStreamBody(t, info, body)
	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonDone, info.StreamStatus.EndReason)
	assert.Equal(t, 777, prompt)
	assert.Equal(t, 0, completion)
	assert.Equal(t, 777, total)
}

func TestOaiResponsesStreamHandlerFailedDoesNotInventPromptUsage(t *testing.T) {
	info := newResponsesStreamTestInfo("gpt-5.1", 999)
	body := "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\"}}\n"

	info, prompt, completion, total, _ := runResponsesStreamBody(t, info, body)
	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonHandlerStop, info.StreamStatus.EndReason)
	assert.True(t, info.StreamStatus.HasErrors())
	assert.Equal(t, 0, prompt)
	assert.Equal(t, 0, completion)
	assert.Equal(t, 0, total)
}
