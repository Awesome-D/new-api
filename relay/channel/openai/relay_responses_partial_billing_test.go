package openai

import (
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOaiResponsesStreamHandlerChargesPromptForDeliveredPartialOutput(t *testing.T) {
	info := newResponsesStreamTestInfo("gpt-5.1", 1426)
	body := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial output already delivered\"}\n"

	info, prompt, completion, total, _ := runResponsesStreamBody(t, info, body)
	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonEOF, info.StreamStatus.EndReason)
	assert.Equal(t, 1426, prompt)
	assert.Greater(t, completion, 0)
	assert.Equal(t, prompt+completion, total)
}
