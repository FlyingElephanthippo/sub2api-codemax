package service

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/domesticip"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	DomesticIPGrantMinimumPaidCents int64 = 20000
	DomesticIPGrantMaxIPs                 = 3
	DomesticIPGrantDailyLimit             = 5
)

var (
	ErrDomesticIPGrantInvalidIP  = infraerrors.BadRequest("DOMESTIC_IP_INVALID", "当前网络取不到公网 IP，请切换网络后重试")
	ErrDomesticIPGrantNotFound   = infraerrors.NotFound("DOMESTIC_IP_GRANT_NOT_FOUND", "未找到这条 IP 记录，请刷新后重试")
	ErrDomesticIPGrantMaxIPs     = infraerrors.Conflict("DOMESTIC_IP_LIMIT", "最多添加 3 个 IP，请先删除旧 IP 或将旧 IP 替换为当前 IP")
	ErrDomesticIPGrantDailyLimit = infraerrors.New(http.StatusTooManyRequests, "DOMESTIC_IP_DAILY_LIMIT", "今天已变更 5 次，请在北京时间明天 00:00 后重试")
)

func DomesticIPGrantEligibilityError(paidCents int64) error {
	remaining := domesticIPRemainingCents(paidCents)
	return infraerrors.Forbidden("DOMESTIC_IP_PAYMENT_REQUIRED", fmt.Sprintf("累计实付充值满 200 元可用，还差 %.2f 元", float64(remaining)/100)).
		WithMetadata(map[string]string{"remaining_amount_cny": fmt.Sprintf("%.2f", float64(remaining)/100)})
}

func domesticIPRemainingCents(paidCents int64) int64 {
	if paidCents >= DomesticIPGrantMinimumPaidCents {
		return 0
	}
	return DomesticIPGrantMinimumPaidCents - paidCents
}

type DomesticIPGrantEntry struct {
	ID        int64     `json:"id"`
	IP        string    `json:"ip"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type DomesticIPGrantSnapshot struct {
	PaidCents   int64
	ChangesUsed int
	Entries     []DomesticIPGrantEntry
}

type DomesticIPGrantStatus struct {
	Enabled            bool                   `json:"enabled"`
	Eligible           bool                   `json:"eligible"`
	PaidAmountCNY      float64                `json:"paid_amount_cny"`
	RemainingAmountCNY float64                `json:"remaining_amount_cny"`
	CurrentIP          string                 `json:"current_ip"`
	CurrentIPValid     bool                   `json:"current_ip_valid"`
	Entries            []DomesticIPGrantEntry `json:"entries"`
	MaxIPs             int                    `json:"max_ips"`
	DailyLimit         int                    `json:"daily_limit"`
	ChangesUsed        int                    `json:"changes_used"`
	ChangesRemaining   int                    `json:"changes_remaining"`
}

type DomesticIPGrantChange struct {
	UserID    int64
	EntryID   int64
	Operation string
	SourceIP  string
}

type DomesticIPGrantMutation struct {
	Changed            bool   `json:"changed"`
	EventID            int64  `json:"event_id,omitempty"`
	NotificationStatus string `json:"notification_status,omitempty"`
	Operation          string `json:"-"`
	OldIP              string `json:"-"`
	NewIP              string `json:"-"`
}

type DomesticIPGrantRepository interface {
	Snapshot(context.Context, int64) (*DomesticIPGrantSnapshot, error)
	Mutate(context.Context, DomesticIPGrantChange) (*DomesticIPGrantMutation, error)
	HasActiveGrant(context.Context, string) (bool, error)
	MarkNotification(context.Context, int64, string) error
}

type domesticIPGrantEmailSender interface {
	SendEmail(context.Context, string, string, string) error
}

type domesticIPGrantUserReader interface {
	GetByID(context.Context, int64) (*User, error)
}

type DomesticIPGrantService struct {
	repo              DomesticIPGrantRepository
	email             domesticIPGrantEmailSender
	users             domesticIPGrantUserReader
	notificationSlots chan struct{}
}

func NewDomesticIPGrantService(repo DomesticIPGrantRepository, email *EmailService, users UserRepository) *DomesticIPGrantService {
	s := &DomesticIPGrantService{repo: repo, users: users, notificationSlots: make(chan struct{}, 4)}
	if email != nil {
		s.email = email
	}
	return s
}

func (s *DomesticIPGrantService) Status(ctx context.Context, userID int64, currentIP string, enabled bool) (*DomesticIPGrantStatus, error) {
	snapshot, err := s.repo.Snapshot(ctx, userID)
	if err != nil {
		return nil, err
	}
	ip, ipErr := domesticip.NormalizePublicIP(currentIP)
	entries := snapshot.Entries
	if entries == nil {
		entries = []DomesticIPGrantEntry{}
	}
	return &DomesticIPGrantStatus{
		Enabled: enabled, Eligible: snapshot.PaidCents >= DomesticIPGrantMinimumPaidCents,
		PaidAmountCNY:      float64(snapshot.PaidCents) / 100,
		RemainingAmountCNY: float64(domesticIPRemainingCents(snapshot.PaidCents)) / 100,
		CurrentIP:          ip, CurrentIPValid: ipErr == nil, Entries: entries,
		MaxIPs: DomesticIPGrantMaxIPs, DailyLimit: DomesticIPGrantDailyLimit,
		ChangesUsed: snapshot.ChangesUsed, ChangesRemaining: max(DomesticIPGrantDailyLimit-snapshot.ChangesUsed, 0),
	}, nil
}

func (s *DomesticIPGrantService) Add(ctx context.Context, userID int64, currentIP string) (*DomesticIPGrantMutation, error) {
	return s.change(ctx, DomesticIPGrantChange{UserID: userID, Operation: "add", SourceIP: currentIP})
}

func (s *DomesticIPGrantService) Replace(ctx context.Context, userID, entryID int64, currentIP string) (*DomesticIPGrantMutation, error) {
	return s.change(ctx, DomesticIPGrantChange{UserID: userID, EntryID: entryID, Operation: "replace", SourceIP: currentIP})
}

func (s *DomesticIPGrantService) Delete(ctx context.Context, userID, entryID int64, currentIP string) (*DomesticIPGrantMutation, error) {
	return s.change(ctx, DomesticIPGrantChange{UserID: userID, EntryID: entryID, Operation: "delete", SourceIP: currentIP})
}

func (s *DomesticIPGrantService) HasActiveGrant(ctx context.Context, sourceIP string) (bool, error) {
	ip, err := domesticip.NormalizePublicIP(sourceIP)
	if err != nil {
		return false, ErrDomesticIPGrantInvalidIP
	}
	return s.repo.HasActiveGrant(ctx, ip)
}

func (s *DomesticIPGrantService) change(ctx context.Context, change DomesticIPGrantChange) (*DomesticIPGrantMutation, error) {
	ip, err := domesticip.NormalizePublicIP(change.SourceIP)
	if err != nil {
		return nil, ErrDomesticIPGrantInvalidIP
	}
	change.SourceIP = ip
	result, err := s.repo.Mutate(ctx, change)
	if err != nil || !result.Changed {
		return result, err
	}
	// The mutation has committed. A failed delivery must never roll it back or
	// encourage the caller to repeat a successful mutation.
	s.enqueueNotification(context.WithoutCancel(ctx), change.UserID, result)
	return result, nil
}

func (s *DomesticIPGrantService) enqueueNotification(ctx context.Context, userID int64, result *DomesticIPGrantMutation) {
	if s.email == nil || s.users == nil {
		s.markNotification(ctx, result, "skipped")
		return
	}
	select {
	case s.notificationSlots <- struct{}{}:
		// SMTP can outlive the panel's request deadline. Never mutate the
		// returned result from a goroutine; the audit row holds delivery state.
		copy := *result
		go func() {
			defer func() { <-s.notificationSlots }()
			s.notify(ctx, userID, &copy)
		}()
	default:
		// Do not create an unbounded queue or hide a dropped notification.
		s.markNotification(ctx, result, "failed")
	}
}

func (s *DomesticIPGrantService) notify(ctx context.Context, userID int64, result *DomesticIPGrantMutation) {
	status := "skipped"
	if s.email != nil && s.users != nil {
		notifyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		user, err := s.users.GetByID(notifyCtx, userID)
		status = "failed"
		if err == nil && user != nil && user.Email != "" {
			action := map[string]string{"add": "添加", "replace": "替换", "delete": "删除"}[result.Operation]
			body := fmt.Sprintf("<p>你的IP自助放行记录已%s。</p><p>旧 IP：%s<br>新 IP：%s</p><p>如果不是你本人操作，请立即登录控制台检查账户安全。</p>", action, html.EscapeString(result.OldIP), html.EscapeString(result.NewIP))
			if s.email.SendEmail(notifyCtx, user.Email, "IP自助放行记录已变更", body) == nil {
				status = "sent"
			}
		}
	}
	s.markNotification(ctx, result, status)
}

func (s *DomesticIPGrantService) markNotification(ctx context.Context, result *DomesticIPGrantMutation, status string) {
	markCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if s.repo.MarkNotification(markCtx, result.EventID, status) == nil {
		result.NotificationStatus = status
	}
}
