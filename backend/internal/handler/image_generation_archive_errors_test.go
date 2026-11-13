package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"
)

func archiveErrorTestRouter(t *testing.T, repository *archiveAuditHistoryRepository) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := NewImageGenerationHandler(&service.ImageGenerationService{
		Repo: repository, APIKeyRepo: archiveAuditKeyRepository{},
		Config: &config.Config{ImageHistory: config.ImageHistoryConfig{Enabled: true}},
	})
	audit := service.NewAuditLogService(nil, nil)
	t.Cleanup(audit.Stop)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 7})
		c.Next()
	})
	router.Use(gin.HandlerFunc(middleware2.NewAuditLogMiddleware(audit)))
	router.POST("/api/v1/image-generations/history", h.Archive)
	return router
}

func archiveErrorTestForm(t *testing.T, fields map[string]string, image []byte) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, value := range fields {
		require.NoError(t, writer.WriteField(name, value))
	}
	if image != nil {
		file, err := writer.CreateFormFile("file", "generated.png")
		require.NoError(t, err)
		_, err = file.Write(image)
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	return body.Bytes(), writer.FormDataContentType()
}

type archiveFailingReader struct {
	reader io.Reader
	err    error
}

func (r *archiveFailingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if errors.Is(err, io.EOF) {
		return n, r.err
	}
	return n, err
}

func TestImageGenerationArchiveClassifiesInvalidAndInterruptedUploads(t *testing.T) {
	validFields := map[string]string{"model": "gpt-image-2.0", "provider": "openai-responses", "api_key_id": "11"}
	image := bytes.Repeat([]byte{137, 80, 78, 71, 13, 10, 26, 10}, 1344*1024/8)
	validBody, contentType := archiveErrorTestForm(t, validFields, image)
	for _, tc := range []struct {
		name        string
		body        func() io.Reader
		contentType string
		wantReason  string
	}{
		{
			name: "missing boundary", body: func() io.Reader { return bytes.NewReader(validBody) },
			contentType: "multipart/form-data", wantReason: "IMAGE_GENERATION_HISTORY_UPLOAD_INVALID",
		},
		{
			name: "wrong media type", body: func() io.Reader { return bytes.NewReader(validBody) },
			contentType: "application/json", wantReason: "IMAGE_GENERATION_HISTORY_UPLOAD_INVALID",
		},
		{
			name: "malformed part header",
			body: func() io.Reader { return bytes.NewBufferString("--test-boundary\r\nmalformed header\r\n\r\nbody\r\n--test-boundary--\r\n") },
			contentType: "multipart/form-data; boundary=test-boundary", wantReason: "IMAGE_GENERATION_HISTORY_UPLOAD_INVALID",
		},
		{
			name: "truncated file", body: func() io.Reader { return bytes.NewReader(validBody[:len(validBody)/2]) },
			contentType: contentType, wantReason: "IMAGE_GENERATION_HISTORY_UPLOAD_INCOMPLETE",
		},
		{
			name: "body read failure after audit prefix",
			body: func() io.Reader {
				return &archiveFailingReader{
					reader: bytes.NewReader(validBody[:len(validBody)/2]), err: errors.New("private transport failure detail"),
				}
			},
			contentType: contentType, wantReason: "IMAGE_GENERATION_HISTORY_UPLOAD_INCOMPLETE",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repository := &archiveAuditHistoryRepository{}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/image-generations/history", tc.body())
			req.Header.Set("Content-Type", tc.contentType)
			recorder := httptest.NewRecorder()
			archiveErrorTestRouter(t, repository).ServeHTTP(recorder, req)
			require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
			var response struct {
				Code    int    `json:"code"`
				Reason  string `json:"reason"`
				Message string `json:"message"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
			require.Equal(t, http.StatusBadRequest, response.Code)
			require.Equal(t, tc.wantReason, response.Reason)
			require.NotEmpty(t, response.Message)
			require.NotContains(t, response.Message, "private transport")
			require.Nil(t, repository.saved, "invalid uploads must not reach the repository")
		})
	}
}

func TestImageGenerationArchiveClassifiesKeyAndRequiredMetadata(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields map[string]string
		reason string
	}{
		{
			name: "invalid Key ID", fields: map[string]string{"model": "gpt-image-2.0", "provider": "openai-responses", "api_key_id": "invalid"},
			reason: "IMAGE_GENERATION_HISTORY_KEY_INVALID",
		},
		{
			name: "missing model", fields: map[string]string{"provider": "openai-responses", "api_key_id": "11"},
			reason: "IMAGE_GENERATION_HISTORY_INVALID",
		},
		{
			name: "missing provider", fields: map[string]string{"model": "gpt-image-2.0", "api_key_id": "11"},
			reason: "IMAGE_GENERATION_HISTORY_INVALID",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, contentType := archiveErrorTestForm(t, tc.fields, []byte("png"))
			repository := &archiveAuditHistoryRepository{}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/image-generations/history", bytes.NewReader(body))
			req.Header.Set("Content-Type", contentType)
			recorder := httptest.NewRecorder()
			archiveErrorTestRouter(t, repository).ServeHTTP(recorder, req)
			require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
			var response struct { Reason string `json:"reason"` }
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
			require.Equal(t, tc.reason, response.Reason)
			require.Nil(t, repository.saved)
		})
	}
}

func TestImageGenerationArchiveRejectsOversizedFileWith413(t *testing.T) {
	repository := &archiveAuditHistoryRepository{}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/image-generations/history", nil)
	req.Header.Set("Content-Type", "multipart/form-data; boundary=test-boundary")
	// Multipart parsing already established the file length; no need to allocate a 32 MiB fixture.
	req.MultipartForm = &multipart.Form{
		Value: url.Values{"model": {"gpt-image-2.0"}, "provider": {"openai-responses"}},
		File: map[string][]*multipart.FileHeader{"file": {{Filename: "generated.png", Size: maxImageGenerationArchiveBytes + 1}}},
	}
	req.PostForm = req.MultipartForm.Value
	recorder := httptest.NewRecorder()
	archiveErrorTestRouter(t, repository).ServeHTTP(recorder, req)
	require.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code, recorder.Body.String())
	var response struct { Reason string `json:"reason"` }
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, "IMAGE_GENERATION_HISTORY_TOO_LARGE", response.Reason)
	require.Nil(t, repository.saved)
}

func TestImageGenerationArchiveMultipartReadErrorClassification(t *testing.T) {
	for _, err := range []error{context.Canceled, context.DeadlineExceeded, io.ErrUnexpectedEOF} {
		require.ErrorIs(t, imageGenerationMultipartError(err, nil), service.ErrImageGenerationHistoryUploadIncomplete)
	}
	require.ErrorIs(t, imageGenerationMultipartError(multipart.ErrMessageTooLarge, nil), service.ErrImageGenerationHistoryTooLarge)
	require.ErrorIs(t, imageGenerationMultipartError(&http.MaxBytesError{Limit: 1}, nil), service.ErrImageGenerationHistoryTooLarge)
	require.Equal(t, http.StatusRequestEntityTooLarge, infraerrors.Code(service.ErrImageGenerationHistoryTooLarge))
}

func TestImageGenerationArchiveDiagnosticsDoNotExposeRequestOrReadError(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/image-generations/history", bytes.NewBufferString("private-image-and-prompt"))
	request.Header.Set("Content-Type", "multipart/form-data; boundary=private-boundary")
	request.Header.Set("Authorization", "Bearer private-token")
	request.ContentLength = -2
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = request
	fields := imageGenerationArchiveDiagnosticFields(c, "multipart", errors.New("private-key-and-read-error"))
	encoded := zapcore.NewMapObjectEncoder()
	for _, field := range fields {
		field.AddTo(encoded)
	}
	require.Equal(t, map[string]any{
		"stage": "multipart", "reason": "INTERNAL_ERROR", "content_length": int64(-1), "multipart_boundary_present": true,
	}, encoded.Fields)
}
