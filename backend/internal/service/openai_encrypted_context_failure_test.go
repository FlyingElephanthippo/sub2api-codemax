package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func commitEncryptedFailureKeepalive(t *testing.T, c *gin.Context, compact bool) {
	t.Helper()
	if compact {
		MarkOpenAICompactClientStream(c)
		keepalive := &openAICompactSSEKeepalive{writer: c.Writer, stop: make(chan struct{})}
		c.Set(openAICompactSSEKeepaliveKey, keepalive)
		c.Writer = &openAICompactKeepaliveWriter{ResponseWriter: c.Writer, k: keepalive}
		require.True(t, keepalive.beat())
		t.Cleanup(keepalive.Stop)
	} else {
		c.Header("Content-Type", "text/event-stream")
		n, err := c.Writer.Write([]byte(": keepalive\n\n"))
		require.NoError(t, err)
		recordOpenAIStreamKeepaliveBytes(c, n)
		c.Writer.Flush()
	}
	require.True(t, c.Writer.Written())
	require.False(t, openAIStreamClientOutputStarted(c, false))
}

func TestOpenAIEncryptedContextFailureAfterKeepalive(t *testing.T) {
	for _, compact := range []bool{false, true} {
		for _, recoveryFails := range []bool{false, true} {
			name := "ordinary/no_candidate"
			if compact {
				name = "compact/no_candidate"
			}
			if recoveryFails {
				name += "/retry_failed"
			}
			t.Run(name, func(t *testing.T) {
				payload := encryptedContextTestEvent("response.failed", "encrypted_context_invalid")
				responses := []*http.Response{encryptedContextTestResponse("data: " + payload + "\n\n")}
				input := `[{"type":"compaction","encrypted_content":"only-history"},{"type":"message","role":"user","content":"hello"}]`
				wantCalls := 1
				if recoveryFails {
					input = encryptedForwardInput
					responses = append(responses, encryptedContextTestResponse("data: "+payload+"\n\n"))
					wantCalls = 2
				}
				svc, account, upstream := encryptedForwardFixture(responses...)
				c, rec := encryptedForwardContext()
				commitEncryptedFailureKeepalive(t, c, compact)
				result, err := svc.Forward(context.Background(), c, account, encryptedForwardBody(!compact, input))
				require.Error(t, err)
				require.Nil(t, result)
				require.Equal(t, wantCalls, upstream.callCount, "final failure must not cause another replay")
				require.Equal(t, http.StatusOK, rec.Code, "the keepalive already committed HTTP 200")
				require.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
				events := parseCompactBridgeSSE(t, stripKeepaliveComments(rec.Body.String()))
				require.Len(t, events, 1, "failure must be framed SSE, never appended bare JSON")
				require.Equal(t, "response.failed", events[0][0])
				failed := gjson.Parse(events[0][1])
				require.Equal(t, "failed", failed.Get("response.status").String())
				require.Equal(t, "encrypted_context_invalid", failed.Get("response.error.code").String())
				require.Equal(t, encryptedContextTestMessage, failed.Get("response.error.message").String())
				require.NotEmpty(t, failed.Get("response.id").String())
				require.Positive(t, failed.Get("response.created_at").Int())
				require.True(t, failed.Get("sequence_number").Exists())
				require.True(t, IsResponseCommitted(c))
				streamError, ok := GetOpsStreamError(c)
				require.True(t, ok)
				require.Equal(t, http.StatusBadRequest, streamError.IntendedStatus)
				require.Equal(t, http.StatusBadRequest, streamError.UpstreamStatus)
				require.Equal(t, "encrypted_context_invalid", streamError.Code)
				require.Len(t, streamError.UpstreamErrors, 1)
				require.Equal(t, http.StatusBadRequest, streamError.UpstreamErrors[0].UpstreamStatusCode)
			})
		}
	}
}

func TestOpenAIEncryptedContextFailureBeforeKeepaliveUsesHTTPError(t *testing.T) {
	svc, account, upstream := encryptedForwardFixture()
	c, rec := encryptedForwardContext()
	response := encryptedForwardJSON(http.StatusBadRequest, `{"error":{"type":"invalid_request_error","code":"encrypted_context_invalid","message":"rebuild context"}}`)
	result, err := svc.handleOpenAIEncryptedContextFailure(context.Background(), response, c, account, nil, "gpt-6.1-sol")
	require.Error(t, err)
	require.Nil(t, result)
	require.Zero(t, upstream.callCount)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "encrypted_context_invalid", gjson.Get(rec.Body.String(), "error.code").String())
}
