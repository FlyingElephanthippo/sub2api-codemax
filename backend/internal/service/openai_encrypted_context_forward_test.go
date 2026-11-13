package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func encryptedForwardFixture(responses ...*http.Response) (*OpenAIGatewayService, *Account, *httpUpstreamSequenceRecorder) {
	upstream := &httpUpstreamSequenceRecorder{responses: responses}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{ID: 41, Name: "encrypted-test", Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Concurrency: 1, Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://example.com"},
		Extra: map[string]any{"use_responses_api": true}}
	return svc, account, upstream
}

func encryptedForwardContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
	return c, rec
}

func encryptedForwardJSON(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}

const encryptedForwardInput = `[{"type":"reasoning","id":"rs_old","encrypted_content":"stale-cipher","summary":[{"type":"summary_text","text":"keep summary"}]},{"type":"message","role":"user","content":"keep message"},{"type":"function_call","name":"lookup","call_id":"call_1","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"keep result"}]`

func encryptedForwardBody(stream bool, input string) []byte {
	return []byte(fmt.Sprintf(`{"model":"gpt-6.1-sol","stream":%t,"prompt_cache_key":"encrypted-fixture","previous_response_id":"resp_anchor","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"input":%s}`, stream, input))
}

func TestOpenAIEncryptedContextForwardRecoveryPreservesConversation(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, failureKind := range []string{"http", "wrapped_http", "sse_error", "sse_failed"} {
			t.Run(fmt.Sprintf("%s/stream=%t", failureKind, stream), func(t *testing.T) {
				payload := encryptedContextTestEvent("error", "encrypted_context_invalid")
				first := encryptedForwardJSON(http.StatusBadRequest, payload)
				switch failureKind {
				case "wrapped_http":
					wrapped, err := json.Marshal(map[string]any{"error": map[string]any{"message": payload}})
					require.NoError(t, err)
					first = encryptedForwardJSON(http.StatusBadRequest, string(wrapped))
				case "sse_error", "sse_failed":
					if failureKind == "sse_failed" {
						payload = encryptedContextTestEvent("response.failed", "encrypted_context_invalid")
					}
					first = encryptedContextTestResponse("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_rejected\"}}\n\ndata: " + payload + "\n\n")
				}
				success := encryptedForwardJSON(http.StatusOK, `{"id":"resp_ok","usage":{"input_tokens":1,"output_tokens":2}}`)
				if stream {
					success = encryptedContextTestResponse("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ok\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":2}}}\n\ndata: [DONE]\n\n")
				}
				svc, account, upstream := encryptedForwardFixture(first, success)
				c, rec := encryptedForwardContext()
				result, err := svc.Forward(context.Background(), c, account, encryptedForwardBody(stream, encryptedForwardInput))
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, 2, upstream.callCount)
				require.Contains(t, rec.Body.String(), "resp_ok")
				require.NotContains(t, rec.Body.String(), "resp_rejected", "buffered metadata from a failed attempt must not leak")
				before, after := upstream.bodies[0], upstream.bodies[1]
				require.Equal(t, "stale-cipher", gjson.GetBytes(before, "input.0.encrypted_content").String())
				require.False(t, gjson.GetBytes(after, "input.0.encrypted_content").Exists())
				for _, path := range []string{"previous_response_id", "tools", "input.0.id", "input.0.summary", "input.1", "input.2", "input.3"} {
					require.JSONEq(t, gjson.GetBytes(before, path).Raw, gjson.GetBytes(after, path).Raw, path)
				}
			})
		}
	}
}

func TestOpenAIEncryptedContextForwardFailureDoesNotPoisonLineage(t *testing.T) {
	for _, secondSSE := range []bool{false, true} {
		t.Run(fmt.Sprintf("secondSSE=%t", secondSSE), func(t *testing.T) {
			payload := encryptedContextTestEvent("error", "encrypted_context_invalid")
			second := encryptedForwardJSON(http.StatusBadRequest, payload)
			if secondSSE {
				second = encryptedContextTestResponse("data: " + encryptedContextTestEvent("response.failed", "encrypted_context_invalid") + "\n\n")
			}
			svc, account, upstream := encryptedForwardFixture(encryptedForwardJSON(http.StatusBadRequest, payload), second,
				encryptedForwardJSON(http.StatusOK, `{"id":"resp_next","usage":{"input_tokens":1,"output_tokens":1}}`))
			c, rec := encryptedForwardContext()
			result, err := svc.Forward(context.Background(), c, account, encryptedForwardBody(false, encryptedForwardInput))
			require.Error(t, err)
			require.Nil(t, result)
			require.Equal(t, 2, upstream.callCount, "HTTP and SSE failures share one retry budget")
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Equal(t, "encrypted_context_invalid", gjson.Get(rec.Body.String(), "error.code").String())
			c, _ = encryptedForwardContext()
			result, err = svc.Forward(context.Background(), c, account, encryptedForwardBody(false, encryptedForwardInput))
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, 3, upstream.callCount)
			require.Equal(t, "stale-cipher", gjson.GetBytes(upstream.bodies[2], "input.0.encrypted_content").String(), "failed recovery must not mark future requests for stripping")
		})
	}
}

func TestOpenAIEncryptedContextForwardRequiresSafeCandidate(t *testing.T) {
	for _, input := range []string{
		`[{"type":"message","role":"user","content":"hello"}]`,
		`[{"type":"reasoning","content":null,"summary":[]},{"type":"message","role":"user","content":"hello"}]`,
		`[{"type":"compaction","encrypted_content":"only-history"},{"type":"message","role":"user","content":"hello"}]`,
		`[{"type":"compaction_summary","encrypted_content":"only-history"},{"type":"message","role":"user","content":"hello"}]`,
	} {
		t.Run(input, func(t *testing.T) {
			svc, account, upstream := encryptedForwardFixture(encryptedContextTestResponse("data: " + encryptedContextTestEvent("response.failed", "encrypted_context_invalid") + "\n\n"))
			c, rec := encryptedForwardContext()
			result, err := svc.Forward(context.Background(), c, account, encryptedForwardBody(false, input))
			require.Error(t, err)
			require.Nil(t, result)
			require.Equal(t, 1, upstream.callCount)
			require.JSONEq(t, input, gjson.GetBytes(upstream.bodies[0], "input").Raw)
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Equal(t, "encrypted_context_invalid", gjson.Get(rec.Body.String(), "error.code").String())
		})
	}
}

func TestOpenAIEncryptedContextForwardBoundsStreamRetries(t *testing.T) {
	payload := encryptedContextTestEvent("response.failed", "encrypted_context_invalid")
	svc, account, upstream := encryptedForwardFixture(
		encryptedContextTestResponse("data: "+payload+"\n\n"),
		encryptedContextTestResponse("data: "+payload+"\n\n"),
	)
	c, rec := encryptedForwardContext()
	result, err := svc.Forward(context.Background(), c, account, encryptedForwardBody(true, encryptedForwardInput))
	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, 2, upstream.callCount)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "encrypted_context_invalid", gjson.Get(rec.Body.String(), "error.code").String())
}

func TestOpenAIEncryptedContextForwardPreservesErrorShapes(t *testing.T) {
	for _, payload := range []string{
		`{"error":{"code":"encrypted_context_invalid","message":"rebuild context","param":"input"}}`,
		`{"response":{"error":{"code":"encrypted_context_invalid","message":"rebuild context","param":"input"}}}`,
		`{"code":"encrypted_context_invalid","message":"rebuild context","param":"input"}`,
	} {
		for _, sse := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/sse=%t", payload, sse), func(t *testing.T) {
				first := encryptedForwardJSON(http.StatusBadRequest, payload)
				if sse {
					first = encryptedContextTestResponse("event: error\ndata: " + payload + "\n\n")
				}
				svc, account, upstream := encryptedForwardFixture(first)
				c, rec := encryptedForwardContext()
				result, err := svc.Forward(context.Background(), c, account, encryptedForwardBody(false, `[{"type":"message","role":"user","content":"hello"}]`))
				require.Error(t, err)
				require.Nil(t, result)
				require.Equal(t, 1, upstream.callCount)
				require.Equal(t, http.StatusBadRequest, rec.Code)
				require.Equal(t, "encrypted_context_invalid", gjson.Get(rec.Body.String(), "error.code").String())
				require.Equal(t, "rebuild context", gjson.Get(rec.Body.String(), "error.message").String())
				require.Equal(t, "input", gjson.Get(rec.Body.String(), "error.param").String())
			})
		}
	}
}

func TestOpenAIEncryptedContextCodeClassification(t *testing.T) {
	for _, tc := range []struct {
		code, message string
		want          bool
	}{
		{"invalid_encrypted_content", "invalid", true},
		{"encrypted_context_invalid", "invalid", true},
		{"thinking_signature_invalid", "The encrypted content could not be verified; could not be decrypted or parsed", true},
		{"thinking_signature_invalid", "unrelated signature", false},
		{"invalid_request_error", "The encrypted content could not be verified", false},
		{"", "encrypted_context_invalid", false},
	} {
		t.Run(tc.code+tc.message, func(t *testing.T) { require.Equal(t, tc.want, isOpenAIEncryptedContextError(tc.code, tc.message)) })
	}
}
