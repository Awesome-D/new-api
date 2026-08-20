package openai

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

func applyResponsesUsage(dst *dto.Usage, src *dto.Usage) {
	if dst == nil || src == nil {
		return
	}

	// Preserve all upstream metadata first, then normalize Responses-native
	// input/output fields into the prompt/completion fields used by settlement.
	*dst = *src
	if dst.PromptTokens == 0 {
		dst.PromptTokens = src.InputTokens
	}
	if dst.CompletionTokens == 0 {
		dst.CompletionTokens = src.OutputTokens
	}
	if src.InputTokensDetails != nil {
		dst.PromptTokensDetails = *src.InputTokensDetails
	}
}

func finalizeResponsesUsage(info *relaycommon.RelayInfo, usage *dto.Usage, fallbackOutput string, allowPromptEstimate bool) {
	if usage == nil {
		return
	}

	// If the upstream omitted output usage, estimate what is observable. This
	// includes normal text, reasoning summaries, and function-call argument
	// deltas collected by the stream handler.
	if usage.CompletionTokens == 0 && fallbackOutput != "" && info != nil {
		usage.CompletionTokens = service.CountTextToken(fallbackOutput, info.UpstreamModelName)
	}

	// Some OpenAI-compatible upstreams only return total_tokens. Reconstruct a
	// usable prompt/completion split when possible instead of discarding a
	// non-zero total and settling the request as free.
	if usage.PromptTokens == 0 && usage.TotalTokens > usage.CompletionTokens {
		usage.PromptTokens = usage.TotalTokens - usage.CompletionTokens
	}
	if usage.CompletionTokens == 0 && usage.TotalTokens > usage.PromptTokens {
		usage.CompletionTokens = usage.TotalTokens - usage.PromptTokens
	}

	// Final billing fallback for a successfully terminated Responses request:
	// even a tool-only/reasoning-only turn may have no visible output text. The
	// locally estimated prompt is still known and must be charged rather than
	// turning a successful request into $0. Do not synthesize prompt usage for a
	// failed/cancelled terminal event that supplied no upstream usage.
	if allowPromptEstimate && usage.PromptTokens == 0 && info != nil {
		usage.PromptTokens = info.GetEstimatePromptTokens()
	}

	if usage.InputTokens == 0 {
		usage.InputTokens = usage.PromptTokens
	}
	if usage.OutputTokens == 0 {
		usage.OutputTokens = usage.CompletionTokens
	}
	if usage.PromptTokens > 0 || usage.CompletionTokens > 0 {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
}

func responsesFallbackOutputText(outputs []dto.ResponsesOutput) string {
	var b strings.Builder
	for _, output := range outputs {
		for _, content := range output.Content {
			if content.Text != "" {
				b.WriteString(content.Text)
			}
		}
		if output.Type == dto.BuildInCallFunctionCall {
			if output.Name != "" {
				b.WriteString(output.Name)
			}
			if arguments := output.ArgumentsString(); arguments != "" {
				b.WriteString(arguments)
			}
		}
	}
	return b.String()
}

func responsesStatusAllowsPromptEstimate(status json.RawMessage) bool {
	if len(status) == 0 {
		return true
	}
	var value string
	if err := common.Unmarshal(status, &value); err != nil {
		// Unknown vendor-specific status should preserve the historical fallback
		// behavior rather than silently making a successful response free.
		return true
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "failed", "cancelled", "canceled":
		return false
	default:
		return true
	}
}

func responsesStreamTerminalError(streamResponse dto.ResponsesStreamResponse) error {
	if streamResponse.Response != nil {
		if oaiErr := streamResponse.Response.GetOpenAIError(); oaiErr != nil && oaiErr.Message != "" {
			return fmt.Errorf("responses stream %s: %s", streamResponse.Type, oaiErr.Message)
		}
	}
	return fmt.Errorf("responses stream ended with %s", streamResponse.Type)
}

func OaiResponsesHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)

	// read response body
	var responsesResponse dto.OpenAIResponsesResponse
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	}
	err = common.Unmarshal(responseBody, &responsesResponse)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	if oaiError := responsesResponse.GetOpenAIError(); oaiError != nil && oaiError.Type != "" {
		return nil, types.WithOpenAIError(*oaiError, resp.StatusCode)
	}

	// 写入新的 response body
	service.IOCopyBytesGracefully(c, resp, responseBody)

	// compute usage
	usage := dto.Usage{}
	applyResponsesUsage(&usage, responsesResponse.Usage)
	finalizeResponsesUsage(
		info,
		&usage,
		responsesFallbackOutputText(responsesResponse.Output),
		responsesStatusAllowsPromptEstimate(responsesResponse.Status),
	)

	// Count actual tool invocations from Output (not tool declarations).
	for _, output := range responsesResponse.Output {
		switch output.Type {
		case dto.BuildInCallWebSearchCall:
			info.CountBillableToolCall(dto.BuildInCallWebSearchCall, "")
		case dto.BuildInCallFileSearchCall:
			info.CountBillableToolCall(dto.BuildInCallFileSearchCall, "")
		case dto.BuildInCallFunctionCall:
			info.CountBillableToolCall(dto.BuildInCallFunctionCall, output.Name)
		}
	}

	imageCounter := &relaycommon.ImageGenerationCallCounter{}
	if !relaycommon.IsNonBillableResponsesStatus(responsesResponse.Status) {
		for i := range responsesResponse.Output {
			idx := i
			imageCounter.Observe(&responsesResponse.Output[i], &idx)
		}
	}
	imageCounter.Commit(info)

	return &usage, nil
}

func OaiResponsesStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		logger.LogError(c, "invalid response or response body")
		return nil, types.NewError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse)
	}

	defer service.CloseResponseBodyGracefully(resp)

	var usage = &dto.Usage{}
	var fallbackOutputBuilder strings.Builder
	imageCounter := &relaycommon.ImageGenerationCallCounter{}
	imageCommitted := false
	allowPromptEstimate := false

	helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {

		// 检查当前数据是否包含 completed 状态和 usage 信息
		var streamResponse dto.ResponsesStreamResponse
		if err := common.UnmarshalJsonStr(data, &streamResponse); err != nil {
			logger.LogError(c, "failed to unmarshal stream response: "+err.Error())
			sr.Error(err)
			return
		}
		sendResponsesStreamData(c, streamResponse, data)
		switch streamResponse.Type {
		case "response.completed", "response.done":
			allowPromptEstimate = true
			if streamResponse.Response != nil {
				applyResponsesUsage(usage, streamResponse.Response.Usage)
				if !imageCommitted {
					if relaycommon.IsNonBillableResponsesStatus(streamResponse.Response.Status) {
						imageCounter.Reset()
						imageCounter.Commit(info)
						imageCommitted = true
					} else {
						for i := range streamResponse.Response.Output {
							idx := i
							imageCounter.Observe(&streamResponse.Response.Output[i], &idx)
						}
						imageCounter.Commit(info)
						imageCommitted = true
					}
				}
			} else if !imageCommitted {
				imageCounter.Commit(info)
				imageCommitted = true
			}
			// Responses SSE does not reliably send a [DONE] sentinel. The protocol
			// terminal event itself is authoritative and must stop the scanner.
			sr.Done()
		case "response.incomplete":
			allowPromptEstimate = true
			if streamResponse.Response != nil {
				applyResponsesUsage(usage, streamResponse.Response.Usage)
			}
			if !imageCommitted {
				imageCounter.Reset()
				imageCounter.Commit(info)
				imageCommitted = true
			}
			// Incomplete is a valid Responses terminal state (for example when
			// max_output_tokens is reached), so terminate normally after forwarding.
			sr.Done()
		case "response.failed", "response.error", "response.cancelled", "response.canceled":
			if streamResponse.Response != nil {
				applyResponsesUsage(usage, streamResponse.Response.Usage)
			}
			if !imageCommitted {
				imageCounter.Reset()
				imageCounter.Commit(info)
				imageCommitted = true
			}
			sr.Stop(responsesStreamTerminalError(streamResponse))
		case "response.output_text.delta",
			"response.function_call_arguments.delta",
			"response.reasoning_summary_text.delta",
			"response.reasoning_text.delta",
			"response.refusal.delta":
			// Accumulate all observable generated text for billing fallback, not
			// only assistant output_text. Tool-only turns otherwise look like zero
			// completion tokens when an upstream drops terminal usage.
			fallbackOutputBuilder.WriteString(streamResponse.Delta)
		case dto.ResponsesOutputTypeItemDone:
			if streamResponse.Item != nil {
				switch streamResponse.Item.Type {
				case dto.BuildInCallWebSearchCall:
					info.CountBillableToolCall(dto.BuildInCallWebSearchCall, "")
				case dto.BuildInCallFileSearchCall:
					info.CountBillableToolCall(dto.BuildInCallFileSearchCall, "")
				case dto.BuildInCallFunctionCall:
					info.CountBillableToolCall(dto.BuildInCallFunctionCall, streamResponse.Item.Name)
				case dto.ResponsesOutputTypeImageGenerationCall:
					if !imageCommitted {
						imageCounter.Observe(streamResponse.Item, streamResponse.OutputIndex)
					}
				}
			}
		}
	})

	finalizeResponsesUsage(info, usage, fallbackOutputBuilder.String(), allowPromptEstimate)
	return usage, nil
}
