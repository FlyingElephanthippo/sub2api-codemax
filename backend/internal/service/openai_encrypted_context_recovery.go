package service

import (
	"bytes"
	"errors"
	"strings"

	"github.com/tidwall/gjson"
)

// The forwarding loop owns the retry budget and request-body repair. Response
// readers only return this signal while replay is still safe.
type openAIEncryptedContextRecoverySignal struct {
	payload []byte
	message string
}

func (e *openAIEncryptedContextRecoverySignal) Error() string {
	if e == nil || strings.TrimSpace(e.message) == "" {
		return "upstream encrypted continuation context rejected"
	}
	return e.message
}

func asOpenAIEncryptedContextRecoverySignal(err error) (*openAIEncryptedContextRecoverySignal, bool) {
	var signal *openAIEncryptedContextRecoverySignal
	ok := errors.As(err, &signal) && signal != nil
	return signal, ok
}

func newOpenAIEncryptedContextRecoverySignal(payload []byte) error {
	if !isOpenAIEncryptedContextErrorPayload(payload) {
		return nil
	}
	return &openAIEncryptedContextRecoverySignal{
		payload: append([]byte(nil), payload...),
		message: extractOpenAISSEErrorMessage(payload),
	}
}

func openAIEncryptedFailureHasOutput(payload []byte) bool {
	for _, path := range []string{"response.output", "output"} {
		output := gjson.GetBytes(payload, path)
		if output.IsArray() && len(output.Array()) > 0 {
			return true
		}
	}
	return false
}

// Even a nonstreaming client can have caused upstream tool execution before
// the failed terminal. Buffering the response does not make those effects safe
// to replay. Use the same conservative semantic boundary as the stream reader.
func openAIEncryptedContextSSEHasOutput(body string) bool {
	hasOutput := false
	forEachOpenAISSEFrame(body, func(eventType string, payload []byte) {
		if hasOutput || bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) {
			return
		}
		if openAIEncryptedFailureHasOutput(payload) {
			hasOutput = true
			return
		}
		switch eventType {
		case "error", "response.failed":
			return
		case "response.completed", "response.done":
			hasOutput = true
		default:
			hasOutput = openAIStreamDataStartsClientOutput(string(payload), eventType)
		}
	})
	return hasOutput
}
