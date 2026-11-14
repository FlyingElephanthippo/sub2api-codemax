package service

import (
	"context"
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/pkg/domesticip"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

type AdminDomesticIPEntry struct {
	ID        int64     `json:"id"`
	IP        string    `json:"ip"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type AdminDomesticIPList struct {
	Items     []AdminDomesticIPEntry `json:"items"`
	Total     int64                  `json:"total"`
	Page      int                    `json:"page"`
	PageSize  int                    `json:"page_size"`
	Enabled   bool                   `json:"enabled"`
	StaticIPs []string               `json:"static_ips"`
}

type AdminDomesticIPActor struct {
	UserID     int64
	AuthMethod string
	SourceIP   string
	RequestID  string
}

type AdminDomesticIPRepository interface {
	List(context.Context, int, int, string) ([]AdminDomesticIPEntry, int64, error)
	Add(context.Context, string, string, AdminDomesticIPActor) (*AdminDomesticIPEntry, error)
	Delete(context.Context, int64, AdminDomesticIPActor) error
}

type AdminDomesticIPService struct {
	repo   AdminDomesticIPRepository
	policy *domesticip.Policy
}

func NewAdminDomesticIPService(repo AdminDomesticIPRepository, policy *domesticip.Policy) *AdminDomesticIPService {
	return &AdminDomesticIPService{repo: repo, policy: policy}
}

func (s *AdminDomesticIPService) List(ctx context.Context, page, pageSize int, search string) (*AdminDomesticIPList, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	search = strings.TrimSpace(search)
	if !utf8.ValidString(search) || utf8.RuneCountInString(search) > 200 || strings.ContainsRune(search, '\x00') {
		return nil, infraerrors.BadRequest("ADMIN_DOMESTIC_IP_SEARCH_INVALID", "搜索内容最多 200 个字符，且必须是有效文本")
	}
	items, total, err := s.repo.List(ctx, page, pageSize, search)
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = []AdminDomesticIPEntry{}
	}
	return &AdminDomesticIPList{Items: items, Total: total, Page: page, PageSize: pageSize, Enabled: s.policy.Enabled(), StaticIPs: s.policy.ManualIPs()}, nil
}

func normalizeAdminDomesticIPActor(actor AdminDomesticIPActor) (AdminDomesticIPActor, error) {
	if actor.UserID <= 0 || (actor.AuthMethod != "jwt" && actor.AuthMethod != "admin_api_key") {
		return actor, infraerrors.Forbidden("ADMIN_DOMESTIC_IP_ACTOR_REQUIRED", "需要有效的管理员身份")
	}
	if actor.SourceIP != "" {
		addr, err := netip.ParseAddr(actor.SourceIP)
		if err != nil || addr.Zone() != "" {
			return actor, infraerrors.BadRequest("ADMIN_DOMESTIC_IP_SOURCE_INVALID", "无法验证管理员来源 IP")
		}
		actor.SourceIP = addr.Unmap().String()
	}
	if len(actor.RequestID) > 128 {
		actor.RequestID = ""
	}
	return actor, nil
}

func (s *AdminDomesticIPService) Add(ctx context.Context, raw, note string, actor AdminDomesticIPActor) (*AdminDomesticIPEntry, error) {
	if !s.policy.Enabled() {
		return nil, infraerrors.Conflict("ADMIN_DOMESTIC_IP_DISABLED", "IP 放行功能尚未启用")
	}
	ip, err := domesticip.NormalizePublicIP(raw)
	if err != nil {
		return nil, infraerrors.BadRequest("ADMIN_DOMESTIC_IP_INVALID", "请输入单个有效公网 IPv4 或 IPv6 地址，不支持网段")
	}
	note = strings.TrimSpace(note)
	if !utf8.ValidString(note) || utf8.RuneCountInString(note) > 500 || strings.ContainsRune(note, '\x00') {
		return nil, infraerrors.BadRequest("ADMIN_DOMESTIC_IP_NOTE_INVALID", "备注最多 500 个字符")
	}
	actor, err = normalizeAdminDomesticIPActor(actor)
	if err != nil {
		return nil, err
	}
	return s.repo.Add(ctx, ip, note, actor)
}

func (s *AdminDomesticIPService) Delete(ctx context.Context, id int64, actor AdminDomesticIPActor) error {
	if id <= 0 {
		return infraerrors.BadRequest("ADMIN_DOMESTIC_IP_ID_INVALID", "无效的 IP 记录")
	}
	actor, err := normalizeAdminDomesticIPActor(actor)
	if err != nil {
		return err
	}
	return s.repo.Delete(ctx, id, actor)
}
