package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"go.uber.org/zap"
)

const (
	defaultImageHistoryRetentionDays    = 90
	defaultImageHistoryCleanupInterval  = 60 * time.Minute
	defaultImageHistoryCleanupBatchSize = 500
	// 单次生图请求的张数上限。前端不加限制，但后端必须有，否则一眼看穿的
	// image_count: 1e9 就能把预估接口变成免费的算力消耗。
	maxImageEstimateCount = 100
)

var (
	ErrImageGenerationDisabled = infraerrors.New(http.StatusNotFound, "IMAGE_GENERATION_DISABLED", "image generation is not enabled")
	ErrImageEstimateInvalid    = infraerrors.New(http.StatusBadRequest, "IMAGE_GENERATION_ESTIMATE_INVALID", "invalid image generation estimate request")
	ErrImageEstimateGroupUnset = infraerrors.New(http.StatusBadRequest, "IMAGE_GENERATION_GROUP_REQUIRED", "the selected API key has no group to price against")
	ErrImageGenerationKeyNotOwned = infraerrors.New(http.StatusForbidden, "IMAGE_GENERATION_KEY_NOT_OWNED", "api key does not belong to this user")
)

// ImageGenerationCapabilities 是前端打开生图页时读到的功能开关。
// RetentionDays 必须与清理任务实际使用的保留期一致，否则前端会展示已经被删掉的记录。
type ImageGenerationCapabilities struct {
	HistoryEnabled bool `json:"history_enabled"`
	RetentionDays  int  `json:"retention_days"`
	MaxImageCount  int  `json:"max_image_count"`
}

// ImageGenerationEstimateRequest 是预估接口的入参。只取计费必需的字段：
// 提示词、参考图、宽高比等都不参与定价，多余字段一律忽略。
type ImageGenerationEstimateRequest struct {
	APIKeyID  int64  `json:"api_key_id"`
	Model     string `json:"model"`
	ImageSize string `json:"image_size"`
	// ImageCount 允许为 0（前端未指定），此时按 1 张计算。
	ImageCount int `json:"image_count"`
}

// ImageGenerationEstimate 是预估结果。unit_cost / total_cost 是不含分组倍率的
// 标准价，estimated_cost 才是用户实际会被扣掉的金额，前端三个字段都要显示。
type ImageGenerationEstimate struct {
	EstimatedCost  float64 `json:"estimated_cost"`
	TotalCost      float64 `json:"total_cost"`
	UnitCost       float64 `json:"unit_cost"`
	ImageCount     int     `json:"image_count"`
	BillingTier    string  `json:"billing_tier"`
	RateMultiplier float64 `json:"rate_multiplier"`
}

// ImageGenerationHistoryItem 是列表项。Content 不序列化，图片走单独的 content 接口。
type ImageGenerationHistoryItem struct {
	ID                int64           `json:"id"`
	TaskID            string          `json:"task_id,omitempty"`
	Model             string          `json:"model"`
	Provider          string          `json:"provider"`
	Prompt            string          `json:"prompt"`
	NormalizedParams  json.RawMessage `json:"normalized_params,omitempty"`
	ReferenceSnapshot json.RawMessage `json:"reference_snapshot,omitempty"`
	ContentType       string          `json:"content_type"`
	SizeBytes         int64           `json:"size_bytes"`
	ContentURL        string          `json:"content_url,omitempty"`
	StorageObjectID   string          `json:"storage_object_id,omitempty"`
	LibraryAssetID    string          `json:"library_asset_id,omitempty"`
	Status            string          `json:"status"`
	Error             string          `json:"error,omitempty"`
	ContentAvailable  bool            `json:"content_available"`
	CreatedAt         time.Time       `json:"created_at"`
}

// ImageGenerationGroupPricingRepository 只暴露定价所需的只读查询，
// 与 BatchImageGroupPricingRepository 保持同一收窄策略。
type ImageGenerationGroupPricingRepository interface {
	GetByIDLite(ctx context.Context, id int64) (*Group, error)
}

// ImageGenerationUserGroupRateRepository 只读用户在指定分组的专属倍率。
type ImageGenerationUserGroupRateRepository interface {
	GetByUserAndGroup(ctx context.Context, userID, groupID int64) (*float64, error)
}

// ImageGenerationAPIKeyRepository 只读 API Key 本身，用于解析它绑定的分组。
type ImageGenerationAPIKeyRepository interface {
	GetByID(ctx context.Context, id int64) (*APIKey, error)
}

// ImageGenerationService 支撑生图页：功能开关、费用预估、历史归档与保留期清理。
type ImageGenerationService struct {
	Repo          ImageGenerationRepository
	APIKeyRepo    ImageGenerationAPIKeyRepository
	GroupRepo     ImageGenerationGroupPricingRepository
	UserGroupRate ImageGenerationUserGroupRateRepository
	Billing       *BillingService
	Config        *config.Config
	// Tasks 提供异步生图任务的列表查询。历史归档在 Postgres，进行中的任务在
	// Redis，两者是不同的数据源，所以这里直接持有任务服务而不是各自的仓储。
	Tasks *ImageTaskService

	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex
}

func NewImageGenerationService(
	repo ImageGenerationRepository,
	apiKeyRepo ImageGenerationAPIKeyRepository,
	groupRepo ImageGenerationGroupPricingRepository,
	userGroupRate ImageGenerationUserGroupRateRepository,
	billing *BillingService,
	cfg *config.Config,
	tasks *ImageTaskService,
) *ImageGenerationService {
	return &ImageGenerationService{
		Repo:          repo,
		APIKeyRepo:    apiKeyRepo,
		GroupRepo:     groupRepo,
		UserGroupRate: userGroupRate,
		Billing:       billing,
		Config:        cfg,
		Tasks:         tasks,
	}
}

func (s *ImageGenerationService) Enabled() bool {
	return s != nil && s.Repo != nil && s.Config != nil && s.Config.ImageHistory.Enabled
}

// Capabilities 报告生图页当前可用的能力。历史功能关闭时仍然返回 200，
// 只是把 history_enabled 置 false —— 前端据此隐藏历史区，而不是整页报错。
func (s *ImageGenerationService) Capabilities() ImageGenerationCapabilities {
	return ImageGenerationCapabilities{
		HistoryEnabled: s.Enabled(),
		RetentionDays:  s.retentionDays(),
		MaxImageCount:  maxImageEstimateCount,
	}
}

func (s *ImageGenerationService) retentionDays() int {
	if s != nil && s.Config != nil && s.Config.ImageHistory.RetentionDays > 0 {
		return s.Config.ImageHistory.RetentionDays
	}
	return defaultImageHistoryRetentionDays
}

// Estimate 计算一次生图的费用预估。
//
// 计费口径与真实扣费保持一致：先取分组在对应尺寸档位上的单价（分组未配置时
// 回落到 LiteLLM 默认价），乘以张数得到标准价 total_cost，再乘以用户在该分组
// 生效的倍率得到 estimated_cost。倍率解析顺序与 batch_image 的
// resolvePricingSnapshot 相同：用户专属倍率 > 分组倍率，生图独立倍率优先。
func (s *ImageGenerationService) Estimate(ctx context.Context, userID int64, req ImageGenerationEstimateRequest) (*ImageGenerationEstimate, error) {
	if s == nil || s.Billing == nil {
		return nil, ErrImageGenerationDisabled
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		return nil, ErrImageEstimateInvalid
	}
	count := req.ImageCount
	if count <= 0 {
		count = 1
	}
	if count > maxImageEstimateCount {
		return nil, ErrImageEstimateInvalid
	}
	if req.APIKeyID <= 0 || s.APIKeyRepo == nil || s.GroupRepo == nil {
		return nil, ErrImageEstimateGroupUnset
	}

	// 前端传的是用户自己选的 API Key，必须先确认归属，否则可以用别人的 Key
	// 反推出其专属分组倍率。
	apiKey, err := s.APIKeyRepo.GetByID(ctx, req.APIKeyID)
	if err != nil || apiKey == nil {
		return nil, ErrImageEstimateGroupUnset
	}
	if apiKey.UserID != userID {
		return nil, ErrImageGenerationKeyNotOwned
	}
	if apiKey.GroupID == nil || *apiKey.GroupID <= 0 {
		// 未绑定分组的 Key 没有专属定价，只能按默认倍率计价。
		groupMultiplier := 1.0
		breakdown := s.Billing.CalculateImageCost(model, NormalizeImageBillingTierOrDefault(req.ImageSize), count, nil, groupMultiplier)
		return imageEstimateFromBreakdown(breakdown, count, req.ImageSize, groupMultiplier), nil
	}

	group, err := s.GroupRepo.GetByIDLite(ctx, *apiKey.GroupID)
	if err != nil || group == nil {
		return nil, ErrImageEstimateGroupUnset
	}

	tier := NormalizeImageBillingTierOrDefault(req.ImageSize)
	groupMultiplier := s.resolveGroupMultiplier(ctx, userID, group)
	if groupMultiplier < 0 {
		groupMultiplier = 0
	}

	breakdown := s.Billing.CalculateImageCost(model, tier, count, groupImagePriceConfig(group), groupMultiplier)
	return imageEstimateFromBreakdown(breakdown, count, tier, groupMultiplier), nil
}

func imageEstimateFromBreakdown(breakdown *CostBreakdown, count int, tier string, groupMultiplier float64) *ImageGenerationEstimate {
	estimate := &ImageGenerationEstimate{
		ImageCount:     count,
		BillingTier:    NormalizeImageBillingTierOrDefault(tier),
		RateMultiplier: groupMultiplier,
	}
	if breakdown == nil {
		return estimate
	}
	estimate.EstimatedCost = breakdown.ActualCost
	estimate.TotalCost = breakdown.TotalCost
	if count > 0 {
		estimate.UnitCost = breakdown.TotalCost / float64(count)
	}
	return estimate
}

func groupImagePriceConfig(group *Group) *ImagePriceConfig {
	if group == nil {
		return nil
	}
	return &ImagePriceConfig{
		Price1K: group.ImagePrice1K,
		Price2K: group.ImagePrice2K,
		Price4K: group.ImagePrice4K,
	}
}

// resolveGroupMultiplier 复刻网关计费的倍率解析顺序，让预估与实际扣费一致。
func (s *ImageGenerationService) resolveGroupMultiplier(ctx context.Context, userID int64, group *Group) float64 {
	if group == nil {
		return 1
	}
	multiplier := group.RateMultiplier
	if multiplier < 0 {
		multiplier = 0
	}
	if s.UserGroupRate != nil {
		override, err := s.UserGroupRate.GetByUserAndGroup(ctx, userID, group.ID)
		if err == nil && override != nil {
			multiplier = *override
		}
	}
	if group.ImageRateIndependent {
		multiplier = group.ImageRateMultiplier
	}
	if multiplier < 0 {
		multiplier = 0
	}
	return multiplier
}

// ListHistory 返回未归档的历史，按创建时间倒序分页。
func (s *ImageGenerationService) ListHistory(ctx context.Context, userID int64, params pagination.PaginationParams) ([]ImageGenerationHistoryItem, int64, error) {
	if !s.Enabled() {
		return nil, 0, ErrImageGenerationDisabled
	}
	records, total, err := s.Repo.ListByUser(ctx, userID, params)
	if err != nil {
		return nil, 0, err
	}
	items := make([]ImageGenerationHistoryItem, 0, len(records))
	for i := range records {
		items = append(items, ImageGenerationHistoryItemFromRecord(&records[i]))
	}
	return items, total, nil
}

// GetHistoryContent 取回单条历史的图片字节。
func (s *ImageGenerationService) GetHistoryContent(ctx context.Context, id, userID int64) (*ImageGenerationHistoryRecord, error) {
	if !s.Enabled() {
		return nil, ErrImageGenerationDisabled
	}
	record, err := s.Repo.Get(ctx, id, userID)
	if err != nil {
		return nil, err
	}
	if !record.ContentAvailable() {
		return nil, ErrImageGenerationHistoryNoFile
	}
	if len(record.Content) == 0 {
		// 走对象存储的记录没有内联字节：交给前端用 content_url 自己取，
		// 这里不代转，避免网关成为任意 URL 的代理。
		return nil, ErrImageGenerationHistoryNoFile
	}
	return record, nil
}

// DeleteHistory 删除一条历史。
func (s *ImageGenerationService) DeleteHistory(ctx context.Context, id, userID int64) error {
	if !s.Enabled() {
		return ErrImageGenerationDisabled
	}
	return s.Repo.Delete(ctx, id, userID)
}

// ArchiveHistory 归档一条记录（前端生成或上传完成后调用）。
// 返回记录与是否为新建：任务重复归档是常态，幂等返回已有记录即可。
func (s *ImageGenerationService) ArchiveHistory(ctx context.Context, userID int64, in *ImageGenerationHistoryCreate) (*ImageGenerationHistoryRecord, bool, error) {
	if !s.Enabled() {
		return nil, false, ErrImageGenerationDisabled
	}
	if in == nil || strings.TrimSpace(in.Model) == "" || strings.TrimSpace(in.Provider) == "" {
		return nil, false, ErrImageGenerationHistoryInvalid
	}
	return s.Repo.Create(ctx, userID, in)
}

// MarkTaskArchived 把仍在跑的任务标记为墓碑：图片一旦落库立即删除，只留记录。
func (s *ImageGenerationService) MarkTaskArchived(ctx context.Context, taskID string, userID int64) error {
	if !s.Enabled() {
		return ErrImageGenerationDisabled
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return ErrImageGenerationHistoryInvalid
	}
	return s.Repo.MarkArchived(ctx, taskID, userID)
}

// VerifyAPIKeyOwnership 报告 apiKeyID 是否属于 userID。
// 归档时用来拒绝把别人的 Key 写进自己的历史。
func (s *ImageGenerationService) VerifyAPIKeyOwnership(ctx context.Context, userID, apiKeyID int64) (bool, error) {
	if s == nil || s.APIKeyRepo == nil || apiKeyID <= 0 {
		return false, nil
	}
	apiKey, err := s.APIKeyRepo.GetByID(ctx, apiKeyID)
	if err != nil || apiKey == nil {
		return false, nil
	}
	return apiKey.UserID == userID, nil
}

// ListTasks 列出当前 API Key 最近创建的异步生图任务，最新在前。
func (s *ImageGenerationService) ListTasks(ctx context.Context, userID, apiKeyID int64, limit int) ([]*ImageTask, error) {
	if s == nil || s.Tasks == nil {
		return nil, ErrImageTaskUnavailable
	}
	return s.Tasks.List(ctx, ImageTaskOwner{UserID: userID, APIKeyID: apiKeyID}, limit)
}

// RunCleanupOnce 删除超过保留期的历史，返回删除条数。
func (s *ImageGenerationService) RunCleanupOnce(ctx context.Context, now time.Time) (int64, error) {
	if !s.Enabled() {
		return 0, nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	cutoff := now.AddDate(0, 0, -s.retentionDays())
	return s.Repo.DeleteExpired(ctx, cutoff, s.cleanupBatchSize())
}

// Start 启动保留期清理循环。与 BatchImageCleanupService 同构：
// 后台 goroutine + ticker，功能关闭时不启动。
func (s *ImageGenerationService) Start() {
	if !s.Enabled() || s.cleanupInterval() <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan struct{})
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(s.cleanupInterval())
		defer ticker.Stop()
		for {
			if _, err := s.RunCleanupOnce(ctx, time.Now()); err != nil {
				logger.L().Warn("image_generation.history_cleanup_failed", zap.Error(err))
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (s *ImageGenerationService) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	cancel := s.cancel
	done := s.done
	s.cancel = nil
	s.done = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

func (s *ImageGenerationService) cleanupInterval() time.Duration {
	if s != nil && s.Config != nil && s.Config.ImageHistory.CleanupIntervalMinutes > 0 {
		return time.Duration(s.Config.ImageHistory.CleanupIntervalMinutes) * time.Minute
	}
	return defaultImageHistoryCleanupInterval
}

func (s *ImageGenerationService) cleanupBatchSize() int {
	if s != nil && s.Config != nil && s.Config.ImageHistory.CleanupBatchSize > 0 {
		return s.Config.ImageHistory.CleanupBatchSize
	}
	return defaultImageHistoryCleanupBatchSize
}

// ImageGenerationHistoryItemFromRecord 把仓储记录转成列表项（不含图片字节）。
func ImageGenerationHistoryItemFromRecord(record *ImageGenerationHistoryRecord) ImageGenerationHistoryItem {
	if record == nil {
		return ImageGenerationHistoryItem{}
	}
	return ImageGenerationHistoryItem{
		ID:                record.ID,
		TaskID:            record.TaskID,
		Model:             record.Model,
		Provider:          record.Provider,
		Prompt:            record.Prompt,
		NormalizedParams:  record.NormalizedParams,
		ReferenceSnapshot: record.ReferenceSnapshot,
		ContentType:       record.ContentType,
		SizeBytes:         record.SizeBytes,
		ContentURL:        record.ContentURL,
		StorageObjectID:   record.StorageObjectID,
		LibraryAssetID:    record.LibraryAssetID,
		Status:            record.Status,
		Error:             record.Error,
		ContentAvailable:  record.ContentAvailable(),
		CreatedAt:         record.CreatedAt,
	}
}
