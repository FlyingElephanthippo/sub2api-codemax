package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// A keepalive may have committed HTTP 200 without committing model output.
// Recovery can still run, but its final failure must terminate that SSE stream.
func (s *OpenAIGatewayService) handleOpenAIEncryptedContextFailure(ctx context.Context, resp *http.Response, c *gin.Context, account *Account, requestBody []byte, requestedModel string) (*OpenAIForwardResult, error) {
	StopOpenAICompactSSEKeepaliveCommitted(c)
	if !c.Writer.Written() {
		return s.handleErrorResponse(ctx, resp, c, account, requestBody, requestedModel)
	}

	body := s.readUpstreamErrorBody(resp)
	_ = resp.Body.Close()
	body = s.redactAgentIdentitySensitiveBody(ctx, account, body)
	message := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(body)))
	if message == "" {
		message = openAIUpstreamClientErrorFallbackMessage
	}
	code := extractUpstreamErrorCode(body)
	errType := strings.TrimSpace(gjson.GetBytes(body, "error.type").String())
	if errType == "" {
		errType = openAIUpstreamClientErrorFallbackType
	}
	// Rebuild from sanitized scalar fields rather than forwarding unknown
	// provider fields into the client event or the operations log.
	payload, err := marshalOpenAIUpstreamJSON(gin.H{"error": gin.H{
		"type": errType, "code": code, "message": message,
	}})
	if err != nil {
		return nil, fmt.Errorf("serialize encrypted context failure: %w", err)
	}
	detail := ""
	if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
		maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
		if maxBytes <= 0 {
			maxBytes = 2048
		}
		detail = truncateString(string(payload), maxBytes)
	}
	setOpsUpstreamError(c, http.StatusBadRequest, message, detail)
	event := OpsUpstreamErrorEvent{
		ProxyID: opsUpstreamProxyID(account), ProxyName: opsUpstreamProxyName(account),
		Platform: PlatformOpenAI, UpstreamStatusCode: http.StatusBadRequest,
		UpstreamRequestID: resp.Header.Get("x-request-id"),
		Kind:              "stream_failed", Message: message, Detail: detail,
	}
	if account != nil {
		event.Platform = account.Platform
		event.AccountID = account.ID
		event.AccountName = account.Name
	}
	appendOpsUpstreamError(c, event)
	MarkOpsStreamFailure(c, errType, code, message, http.StatusBadRequest)
	MarkResponseCommitted(c)
	if _, err := c.Writer.WriteString(buildOpenAIResponseFailedSSE("", "", 0, payload, message)); err != nil {
		return nil, fmt.Errorf("write encrypted context failure: %w", err)
	}
	c.Writer.Flush()
	return nil, fmt.Errorf("upstream encrypted context rejected: %s", message)
}
