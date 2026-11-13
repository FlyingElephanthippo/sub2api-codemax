package service

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

func isOpenAIEncryptedContextError(code, message string) bool {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "invalid_encrypted_content", "encrypted_context_invalid":
		return true
	case "thinking_signature_invalid":
		return strings.Contains(message, "The encrypted content") &&
			strings.Contains(message, "could not be verified") &&
			strings.Contains(message, "could not be decrypted or parsed")
	default:
		return false
	}
}

func isOpenAIEncryptedContextErrorPayload(payload []byte) bool {
	if isOpenAIEncryptedContextError(extractUpstreamErrorCode(payload), extractUpstreamErrorMessage(payload)) {
		return true
	}
	for _, prefix := range []string{"response.error.", ""} {
		if isOpenAIEncryptedContextError(gjson.GetBytes(payload, prefix+"code").String(), gjson.GetBytes(payload, prefix+"message").String()) {
			return true
		}
	}
	return false
}

// SSE transport success can still contain a request failure. Preserve the
// provider's error object while passing it through the normal HTTP error path.
func openAIEncryptedContextErrorResponse(resp *http.Response, payload []byte) *http.Response {
	errorObject := gjson.GetBytes(payload, "error")
	if !errorObject.IsObject() {
		errorObject = gjson.GetBytes(payload, "response.error")
	}
	if !errorObject.IsObject() && isOpenAIEncryptedContextError(gjson.GetBytes(payload, "code").String(), gjson.GetBytes(payload, "message").String()) {
		errorObject = gjson.ParseBytes(payload)
	}
	body := payload
	if errorObject.IsObject() {
		body = []byte(`{"error":` + errorObject.Raw + `}`)
	}
	header := resp.Header.Clone()
	if header == nil {
		header = make(http.Header)
	}
	header.Set("Content-Type", "application/json")
	header.Del("Content-Length")
	return &http.Response{
		StatusCode:    http.StatusBadRequest,
		Header:        header,
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}
