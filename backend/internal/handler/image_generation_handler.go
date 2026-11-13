package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// maxImageGenerationArchiveBytes 限制归档单张图片的字节数。图片走 Postgres 行内
// 存储，不设上限就会让一个用户把库写满。
const maxImageGenerationArchiveBytes = 32 << 20

// ImageGenerationHandler 服务生图页的面板接口：能力、预估、历史归档与取回。
type ImageGenerationHandler struct {
	service *service.ImageGenerationService
}

func NewImageGenerationHandler(svc *service.ImageGenerationService) *ImageGenerationHandler {
	return &ImageGenerationHandler{service: svc}
}

// Capabilities GET /api/v1/image-generations/capabilities
func (h *ImageGenerationHandler) Capabilities(c *gin.Context) {
	if _, ok := imageGenerationUserID(c); !ok {
		return
	}
	if h == nil || h.service == nil {
		imageGenerationError(c, service.ErrImageGenerationDisabled)
		return
	}
	response.Success(c, h.service.Capabilities())
}

// Estimate POST /api/v1/image-generations/estimate
func (h *ImageGenerationHandler) Estimate(c *gin.Context) {
	userID, ok := imageGenerationUserID(c)
	if !ok {
		return
	}
	var req service.ImageGenerationEstimateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		imageGenerationError(c, service.ErrImageEstimateInvalid)
		return
	}
	if h == nil || h.service == nil {
		imageGenerationError(c, service.ErrImageGenerationDisabled)
		return
	}
	estimate, err := h.service.Estimate(c.Request.Context(), userID, req)
	if err != nil {
		imageGenerationError(c, err)
		return
	}
	response.Success(c, estimate)
}

// ListHistory GET /api/v1/image-generations/history
func (h *ImageGenerationHandler) ListHistory(c *gin.Context) {
	userID, ok := imageGenerationUserID(c)
	if !ok {
		return
	}
	page, pageSize := response.ParsePagination(c)
	params := pagination.PaginationParams{
		Page:      page,
		PageSize:  pageSize,
		SortBy:    c.DefaultQuery("sort_by", "created_at"),
		SortOrder: c.DefaultQuery("sort_order", "desc"),
	}
	if h == nil || h.service == nil {
		imageGenerationError(c, service.ErrImageGenerationDisabled)
		return
	}
	items, total, err := h.service.ListHistory(c.Request.Context(), userID, params)
	if err != nil {
		imageGenerationError(c, err)
		return
	}
	response.Paginated(c, items, total, page, pageSize)
}

// Archive POST /api/v1/image-generations/history
// 前端用 multipart/form-data 上传，其余元数据以普通字段附带。
func (h *ImageGenerationHandler) Archive(c *gin.Context) {
	userID, ok := imageGenerationUserID(c)
	if !ok {
		return
	}
	if h == nil || h.service == nil {
		imageGenerationArchiveError(c, "service", service.ErrImageGenerationDisabled)
		return
	}

	var body *imageGenerationArchiveBody
	if c.Request.Body != nil {
		body = &imageGenerationArchiveBody{ReadCloser: c.Request.Body}
		c.Request.Body = body
	}
	form, err := c.MultipartForm()
	if err != nil {
		var readErr error
		if body != nil {
			readErr = body.readErr
		}
		imageGenerationArchiveError(c, "multipart", imageGenerationMultipartError(err, readErr))
		return
	}

	in := &service.ImageGenerationHistoryCreate{
		TaskID:          strings.TrimSpace(c.PostForm("task_id")),
		Model:           strings.TrimSpace(c.PostForm("model")),
		Provider:        strings.TrimSpace(c.PostForm("provider")),
		Prompt:          c.PostForm("prompt"),
		ContentType:     strings.TrimSpace(c.PostForm("content_type")),
		ContentURL:      strings.TrimSpace(c.PostForm("content_url")),
		StorageObjectID: strings.TrimSpace(c.PostForm("storage_object_id")),
		LibraryAssetID:  strings.TrimSpace(c.PostForm("library_asset_id")),
		Status:          strings.TrimSpace(c.PostForm("status")),
		Error:           strings.TrimSpace(c.PostForm("error")),
		NormalizedParams: json.RawMessage(strings.TrimSpace(c.PostForm("normalized_params"))),
		ReferenceSnapshot: json.RawMessage(strings.TrimSpace(c.PostForm("reference_snapshot"))),
	}

	apiKeyID, err := parseImageGenerationAPIKeyID(c.PostForm("api_key_id"))
	if err != nil {
		imageGenerationArchiveError(c, "key", err)
		return
	}
	if apiKeyID != nil {
		// 归档行的 api_key_id 只用于展示归属，但允许写入别人的 Key 会污染
		// 对方的用量视图，所以这里必须校验归属。
		owned, verifyErr := h.service.VerifyAPIKeyOwnership(c.Request.Context(), userID, *apiKeyID)
		if verifyErr != nil {
			imageGenerationArchiveError(c, "key_ownership", verifyErr)
			return
		}
		if !owned {
			imageGenerationArchiveError(c, "key_ownership", service.ErrImageGenerationKeyNotOwned)
			return
		}
		in.APIKeyID = apiKeyID
	}

	if content, err := readImageGenerationUpload(form); err != nil {
		imageGenerationArchiveError(c, "file", err)
		return
	} else if len(content) > 0 {
		in.Content = content
	}

	record, created, err := h.service.ArchiveHistory(c.Request.Context(), userID, in)
	if err != nil {
		imageGenerationArchiveError(c, "service", err)
		return
	}
	if created {
		response.Created(c, imageGenerationHistoryResponse(record))
		return
	}
	response.Success(c, imageGenerationHistoryResponse(record))
}

// GetContent GET /api/v1/image-generations/history/:id/content
func (h *ImageGenerationHandler) GetContent(c *gin.Context) {
	userID, ok := imageGenerationUserID(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		imageGenerationError(c, service.ErrImageGenerationHistoryNotFound)
		return
	}
	if h == nil || h.service == nil {
		imageGenerationError(c, service.ErrImageGenerationDisabled)
		return
	}
	record, err := h.service.GetHistoryContent(c.Request.Context(), id, userID)
	if err != nil {
		imageGenerationError(c, err)
		return
	}
	contentType := strings.TrimSpace(record.ContentType)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	c.Header("Cache-Control", "private, max-age=300")
	c.Data(http.StatusOK, contentType, record.Content)
}

// DeleteHistory DELETE /api/v1/image-generations/history/:id
//
// 已提交但尚未完成的任务（行上仍有 task_id）不能真删：任务完成后还会回写图片。
// 此时置为墓碑，图片一落库即丢弃。前端据此把进行中的行置灰。
func (h *ImageGenerationHandler) DeleteHistory(c *gin.Context) {
	userID, ok := imageGenerationUserID(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		imageGenerationError(c, service.ErrImageGenerationHistoryNotFound)
		return
	}
	if h == nil || h.service == nil {
		imageGenerationError(c, service.ErrImageGenerationDisabled)
		return
	}
	if err := h.service.DeleteHistory(c.Request.Context(), id, userID); err != nil {
		imageGenerationError(c, err)
		return
	}
	response.Success(c, gin.H{"deleted": true})
}

// ListTasks GET /v1/images/tasks —— 挂在网关鉴权下，列出当前 API Key 最近的任务。
func (h *ImageGenerationHandler) ListTasks(c *gin.Context) {
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey == nil || apiKey.UserID <= 0 || apiKey.ID <= 0 {
		imageGenerationGatewayError(c, http.StatusForbidden, "API key is required")
		return
	}
	limit := parseImageGenerationTaskLimit(c.Query("limit"))
	if h == nil || h.service == nil {
		imageGenerationGatewayError(c, http.StatusNotFound, "image task history is not available")
		return
	}
	tasks, err := h.service.ListTasks(c.Request.Context(), apiKey.UserID, apiKey.ID, limit)
	if err != nil {
		imageGenerationError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"data": tasks})
}

func imageGenerationUserID(c *gin.Context) (int64, bool) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not found in context")
		return 0, false
	}
	return subject.UserID, true
}

func parseImageGenerationAPIKeyID(raw string) (*int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return nil, service.ErrImageGenerationHistoryKeyInvalid
	}
	return &id, nil
}

// readImageGenerationUpload 取出 multipart 里的 file 字段。
// 超限或读失败时返回对应上传错误，而不是静默截断。
func readImageGenerationUpload(form *multipart.Form) ([]byte, error) {
	if form == nil {
		return nil, nil
	}
	files := form.File["file"]
	if len(files) == 0 {
		return nil, nil
	}
	fileHeader := files[0]
	if fileHeader.Size > maxImageGenerationArchiveBytes {
		return nil, service.ErrImageGenerationHistoryTooLarge
	}
	file, err := fileHeader.Open()
	if err != nil {
		return nil, service.ErrImageGenerationHistoryUploadIncomplete
	}
	defer func() { _ = file.Close() }()
	// 再包一层限长读，防止 multipart 头里的 Size 与实际不符。
	content, err := io.ReadAll(io.LimitReader(file, maxImageGenerationArchiveBytes+1))
	if err != nil {
		return nil, service.ErrImageGenerationHistoryUploadIncomplete
	}
	if int64(len(content)) > maxImageGenerationArchiveBytes {
		return nil, service.ErrImageGenerationHistoryTooLarge
	}
	return content, nil
}

// 保存底层读取失败的类别；multipart 包的错误不总能区分读体失败和格式错误。
type imageGenerationArchiveBody struct {
	io.ReadCloser
	readErr error
}

func (b *imageGenerationArchiveBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		b.readErr = err
	}
	return n, err
}

func imageGenerationMultipartError(err, readErr error) error {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) || errors.As(readErr, &maxErr) || errors.Is(err, multipart.ErrMessageTooLarge) {
		return service.ErrImageGenerationHistoryTooLarge
	}
	if readErr != nil || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return service.ErrImageGenerationHistoryUploadIncomplete
	}
	return service.ErrImageGenerationHistoryUploadInvalid
}

func imageGenerationArchiveError(c *gin.Context, stage string, err error) {
	logger.L().Warn("image_generation.archive_failed", imageGenerationArchiveDiagnosticFields(c, stage, err)...)
	imageGenerationError(c, err)
}

func imageGenerationArchiveDiagnosticFields(c *gin.Context, stage string, err error) []zap.Field {
	_, params, parseErr := mime.ParseMediaType(c.GetHeader("Content-Type"))
	contentLength := c.Request.ContentLength
	if contentLength < -1 {
		contentLength = -1
	}
	reason := infraerrors.Reason(err)
	if reason == "" {
		reason = "INTERNAL_ERROR"
	}
	return []zap.Field{
		zap.String("stage", stage),
		zap.String("reason", reason),
		zap.Int64("content_length", contentLength),
		zap.Bool("multipart_boundary_present", parseErr == nil && params["boundary"] != ""),
	}
}

func parseImageGenerationTaskLimit(raw string) int {
	limit, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || limit <= 0 {
		return 100
	}
	if limit > 100 {
		return 100
	}
	return limit
}

func imageGenerationHistoryResponse(record *service.ImageGenerationHistoryRecord) any {
	if record == nil {
		return nil
	}
	return service.ImageGenerationHistoryItemFromRecord(record)
}

func imageGenerationError(c *gin.Context, err error) {
	response.ErrorFrom(c, err)
}

// imageGenerationGatewayError 用 OpenAI 风格错误体回网关调用方，
// 与 /v1/images/tasks 既有端点的错误格式保持一致。
func imageGenerationGatewayError(c *gin.Context, status int, message string) {
	c.Header("Cache-Control", "no-store")
	c.JSON(status, gin.H{"error": gin.H{"type": "invalid_request_error", "code": "INVALID_REQUEST", "message": message}})
}
