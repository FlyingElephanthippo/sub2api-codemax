package service

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const encryptedContextTestMessage = "The upstream could not validate encrypted continuation context. Rebuild the conversation context in the client."

func encryptedContextTestEvent(eventType, code string) string {
	errJSON := fmt.Sprintf(`{"type":"invalid_request_error","code":%q,"message":%q}`, code, encryptedContextTestMessage)
	if eventType == "response.failed" {
		return `{"type":"response.failed","response":{"id":"resp_rejected","status":"failed","error":` + errJSON + `}}`
	}
	return `{"type":"error","error":` + errJSON + `}`
}

func encryptedContextTestSetup(t *testing.T) (*OpenAIGatewayService, *gin.Context, *httptest.ResponseRecorder, *Account) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}}
	return svc, c, rec, &Account{ID: 41, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
}

func encryptedContextTestResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestOpenAIEncryptedContextStreamSignalsOnlyBeforeOutput(t *testing.T) {
	for _, code := range []string{"encrypted_context_invalid", "invalid_encrypted_content"} {
		for _, eventType := range []string{"error", "response.failed"} {
			t.Run(code+"/"+eventType, func(t *testing.T) {
				svc, c, rec, account := encryptedContextTestSetup(t)
				payload := encryptedContextTestEvent(eventType, code)
				body := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_rejected\"}}\n\n" +
					"data: {\"type\":\"keepalive\"}\n\n" + "data: " + payload + "\n\n"
				_, err := svc.handleStreamingResponse(c.Request.Context(), encryptedContextTestResponse(body), c, account, time.Now(), "gpt-6.1-sol", "gpt-6.1-sol")
				signal, ok := asOpenAIEncryptedContextRecoverySignal(err)
				require.True(t, ok, "expected recovery signal: %v", err)
				require.JSONEq(t, payload, string(signal.payload), "the original error code must survive recovery handoff")
				require.False(t, c.Writer.Written())
				require.Empty(t, rec.Body.String())
			})
		}
	}
}

func TestOpenAIEncryptedContextStreamDoesNotReplaySemanticOutput(t *testing.T) {
	for _, output := range []string{
		`{"type":"response.output_text.delta","delta":"already sent"}`,
		`{"type":"response.function_call_arguments.delta","delta":"{}"}`,
		`{"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","arguments":""}}`,
		`{"type":"response.output_item.done","item":{"type":"reasoning","encrypted_content":"new-cipher"}}`,
	} {
		t.Run(output, func(t *testing.T) {
			svc, c, rec, account := encryptedContextTestSetup(t)
			payload := encryptedContextTestEvent("response.failed", "encrypted_context_invalid")
			body := "data: " + output + "\n\ndata: " + payload + "\n\n"
			_, err := svc.handleStreamingResponse(c.Request.Context(), encryptedContextTestResponse(body), c, account, time.Now(), "gpt-6.1-sol", "gpt-6.1-sol")
			_, signal := asOpenAIEncryptedContextRecoverySignal(err)
			require.False(t, signal)
			require.Error(t, err)
			require.True(t, c.Writer.Written())
			require.Contains(t, rec.Body.String(), `"code":"encrypted_context_invalid"`)
		})
	}
}

func TestOpenAIEncryptedContextNonStreamingRespectsUpstreamOutput(t *testing.T) {
	payload := encryptedContextTestEvent("response.failed", "encrypted_context_invalid")
	for _, tc := range []struct {
		name       string
		prefix     string
		committed  bool
		wantSignal bool
	}{
		{name: "metadata_only", prefix: `{"type":"response.created","response":{"id":"resp_rejected"}}`, wantSignal: true},
		{name: "text_output", prefix: `{"type":"response.output_text.delta","delta":"already generated"}`},
		{name: "tool_arguments", prefix: `{"type":"response.function_call_arguments.delta","delta":"{}"}`},
		{name: "tool_completion", prefix: `{"type":"response.output_item.done","item":{"type":"function_call","arguments":""}}`},
		{name: "response_committed", committed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, c, rec, account := encryptedContextTestSetup(t)
			if tc.committed {
				MarkResponseCommitted(c)
			}
			body := "data: " + payload + "\n\ndata: [DONE]\n\n"
			if tc.prefix != "" {
				body = "data: " + tc.prefix + "\n\n" + body
			}
			_, err := svc.handleSSEToJSON(encryptedContextTestResponse(body), c, account, []byte(body), "gpt-6.1-sol", "gpt-6.1-sol")
			signal, ok := asOpenAIEncryptedContextRecoverySignal(err)
			require.Equal(t, tc.wantSignal, ok)
			if tc.wantSignal {
				require.JSONEq(t, payload, string(signal.payload))
				require.False(t, c.Writer.Written())
				require.Empty(t, rec.Body.String())
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestOpenAIEncryptedContextFailedPayloadOutputBlocksRecovery(t *testing.T) {
	payload := `{"type":"response.failed","response":{"output":[{"type":"function_call","call_id":"call_1","arguments":"{}"}],"error":{"type":"invalid_request_error","code":"encrypted_context_invalid","message":"rejected"}}}`
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("streaming=%t", streaming), func(t *testing.T) {
			svc, c, _, account := encryptedContextTestSetup(t)
			body := "data: " + payload + "\n\n"
			var err error
			if streaming {
				_, err = svc.handleStreamingResponse(c.Request.Context(), encryptedContextTestResponse(body), c, account, time.Now(), "model", "model")
			} else {
				_, err = svc.handleSSEToJSON(encryptedContextTestResponse(body), c, account, []byte(body), "model", "model")
			}
			_, signal := asOpenAIEncryptedContextRecoverySignal(err)
			require.False(t, signal, "failed terminal may contain already executed tools")
			require.Error(t, err)
		})
	}
}

func TestOpenAIEncryptedContextCyberPolicyNeverRecovers(t *testing.T) {
	svc, c, _, account := encryptedContextTestSetup(t)
	payload := encryptedContextTestEvent("response.failed", "cyber_policy")
	body := "data: " + payload + "\n\n"
	_, err := svc.handleStreamingResponse(c.Request.Context(), encryptedContextTestResponse(body), c, account, time.Now(), "model", "model")
	_, signal := asOpenAIEncryptedContextRecoverySignal(err)
	require.False(t, signal)
	require.NotNil(t, GetOpsCyberPolicy(c))
}
