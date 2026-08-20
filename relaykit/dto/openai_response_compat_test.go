package dto

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResponsesStreamResponseUnmarshalFloatCreatedAt(t *testing.T) {
	payload := `{"response":{"id":"resp_test","object":"response","created_at":1786588600.0,"model":"test-model","output":[],"status":"completed","usage":{"input_tokens":12,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0},"output_tokens":48,"total_tokens":60}},"type":"response.completed"}`
	var resp ResponsesStreamResponse
	require.NoError(t, json.Unmarshal([]byte(payload), &resp))
	require.NotNil(t, resp.Response)
	assert.Equal(t, "response.completed", resp.Type)
	assert.Equal(t, 1786588600, resp.Response.CreatedAt)
	require.NotNil(t, resp.Response.Usage)
	assert.Equal(t, 12, resp.Response.Usage.InputTokens)
	assert.Equal(t, 48, resp.Response.Usage.OutputTokens)
	assert.Equal(t, 60, resp.Response.Usage.TotalTokens)
}

func TestResponsesResponseCreatedAtIntegerRoundTrip(t *testing.T) {
	var resp OpenAIResponsesResponse
	require.NoError(t, json.Unmarshal([]byte(`{"created_at":1786587534}`), &resp))
	assert.Equal(t, 1786587534, resp.CreatedAt)

	out, err := json.Marshal(OpenAIResponsesResponse{CreatedAt: 1786587534})
	require.NoError(t, err)
	assert.Contains(t, string(out), `"created_at":1786587534`)
}

func TestIntValueUnmarshalFloat(t *testing.T) {
	var v IntValue
	require.NoError(t, json.Unmarshal([]byte(`1786588600.0`), &v))
	assert.Equal(t, 1786588600, int(v))

	require.NoError(t, json.Unmarshal([]byte(`1786588600.9`), &v))
	assert.Equal(t, 1786588600, int(v))

	require.NoError(t, json.Unmarshal([]byte(`42`), &v))
	assert.Equal(t, 42, int(v))

	require.NoError(t, json.Unmarshal([]byte(`"7"`), &v))
	assert.Equal(t, 7, int(v))

	require.NoError(t, json.Unmarshal([]byte(`-9223372036854775808.0`), &v))
	assert.Equal(t, math.MinInt, int(v))

	assert.Error(t, json.Unmarshal([]byte(`9223372036854775808.0`), &v))
	assert.Error(t, json.Unmarshal([]byte(`1e100`), &v))
	assert.Error(t, json.Unmarshal([]byte(`-1e100`), &v))
	assert.Error(t, json.Unmarshal([]byte(`"abc"`), &v))
	assert.Error(t, json.Unmarshal([]byte(`true`), &v))
}
