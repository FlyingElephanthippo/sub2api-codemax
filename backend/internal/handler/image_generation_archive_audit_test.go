package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type archiveAuditHistoryRepository struct {
	service.ImageGenerationRepository
	saved *service.ImageGenerationHistoryCreate
}

func (r *archiveAuditHistoryRepository) Create(_ context.Context, userID int64, in *service.ImageGenerationHistoryCreate) (*service.ImageGenerationHistoryRecord, bool, error) {
	r.saved = in
	return &service.ImageGenerationHistoryRecord{
		ID: 1, UserID: userID, APIKeyID: in.APIKeyID, Model: in.Model, Provider: in.Provider,
		Prompt: in.Prompt, NormalizedParams: in.NormalizedParams, ReferenceSnapshot: in.ReferenceSnapshot,
		Content: in.Content, ContentType: in.ContentType, SizeBytes: int64(len(in.Content)),
		Status: in.Status, CreatedAt: time.Now(),
	}, true, nil
}

type archiveAuditKeyRepository struct{}

func (archiveAuditKeyRepository) GetByID(_ context.Context, id int64) (*service.APIKey, error) {
	return &service.APIKey{ID: id, UserID: 7}, nil
}

type archiveAuditChunkedReader struct {
	reader io.Reader
	chunk  int
}

func (r *archiveAuditChunkedReader) Read(p []byte) (int, error) {
	if len(p) > r.chunk {
		p = p[:r.chunk]
	}
	return r.reader.Read(p)
}

func TestImageGenerationArchivePreservesMultipartAfterAuditPrefixCapture(t *testing.T) {
	gin.SetMode(gin.TestMode)
	image := bytes.Repeat([]byte{137, 80, 78, 71, 13, 10, 26, 10}, 1344*1024/8)
	for _, fileFirst := range []bool{false, true} {
		for _, chunkSize := range []int{257, 32768} {
			t.Run("file_first="+strconv.FormatBool(fileFirst)+"/chunk="+strconv.Itoa(chunkSize), func(t *testing.T) {
				var body bytes.Buffer
				writer := multipart.NewWriter(&body)
				writeFile := func() {
					file, err := writer.CreateFormFile("file", "generated.png")
					require.NoError(t, err)
					_, err = file.Write(image)
					require.NoError(t, err)
				}
				if fileFirst {
					writeFile()
				}
				for _, field := range [][2]string{
					{"model", "gpt-image-2.0"}, {"provider", "openai-responses"}, {"prompt", "生成一张图片"},
					{"api_key_id", "11"}, {"content_type", "image/png"}, {"status", "completed"},
					{"normalized_params", `{"size":"3840x2160","output_format":"png"}`},
					{"reference_snapshot", `[]`},
				} {
					require.NoError(t, writer.WriteField(field[0], field[1]))
				}
				if !fileFirst {
					writeFile()
				}
				require.NoError(t, writer.Close())
				require.Greater(t, body.Len(), service.AuditRequestBodyCaptureLimit)

				repository := &archiveAuditHistoryRepository{}
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

				req := httptest.NewRequest(http.MethodPost, "/api/v1/image-generations/history", &archiveAuditChunkedReader{
					reader: bytes.NewReader(body.Bytes()), chunk: chunkSize,
				})
				req.Header.Set("Content-Type", writer.FormDataContentType())
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, req)

				require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
				require.NotNil(t, repository.saved)
				require.Equal(t, image, repository.saved.Content)
				require.Equal(t, "gpt-image-2.0", repository.saved.Model)
				require.Equal(t, "openai-responses", repository.saved.Provider)
				require.Equal(t, int64(11), *repository.saved.APIKeyID)
				require.JSONEq(t, `{"size":"3840x2160","output_format":"png"}`, string(repository.saved.NormalizedParams))
				require.JSONEq(t, `[]`, string(repository.saved.ReferenceSnapshot))
				var response struct {
					Code int `json:"code"`
					Data struct {
						ContentAvailable bool `json:"content_available"`
						SizeBytes        int  `json:"size_bytes"`
					} `json:"data"`
				}
				require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
				require.Equal(t, 0, response.Code)
				require.True(t, response.Data.ContentAvailable)
				require.Equal(t, len(image), response.Data.SizeBytes)
			})
		}
	}
}
