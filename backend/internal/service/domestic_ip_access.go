package service

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/domesticip"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const DomesticIPDeniedHeader = "X-Sub2api-Network-Denied"
const DomesticIPDeniedHeaderValue = "domestic-ip"

// MarkDomesticIPDenied lets the trusted ingress preserve the network-denial
// transport behavior. Other authorization and upstream errors must never set it.
func MarkDomesticIPDenied(headers http.Header, err error) {
	if errors.Is(err, ErrDomesticIPDenied) {
		headers.Set(DomesticIPDeniedHeader, DomesticIPDeniedHeaderValue)
	}
}

var (
	ErrDomesticIPDenied      = infraerrors.Forbidden("DOMESTIC_IP_NOT_ALLOWED", "当前 IP 尚未放行，请到控制台完成自助放行后重试。")
	ErrDomesticIPUnavailable = infraerrors.New(http.StatusServiceUnavailable, "DOMESTIC_IP_UNAVAILABLE", "IP 放行状态暂时无法验证，请稍后重试。")
	ErrDomesticIPInvalid     = infraerrors.BadRequest("PUBLIC_IP_UNAVAILABLE", "当前网络取不到公网 IP，请切换网络后重试")
)

type domesticIPGrantLookup interface {
	HasActiveGrant(context.Context, string) (bool, error)
}

type DomesticIPAccessService struct {
	policy *domesticip.Policy
	grants domesticIPGrantLookup
}

func ProvideDomesticIPPolicy(cfg *config.Config) (*domesticip.Policy, error) {
	return domesticip.NewPolicy(domesticip.Options{
		Enabled:         cfg.DomesticIP.Enabled,
		SnapshotFile:    cfg.DomesticIP.SnapshotFile,
		SnapshotSHA256:  cfg.DomesticIP.SnapshotSHA256,
		TrustedProxyIPs: cfg.DomesticIP.TrustedProxyIPs,
	})
}

func NewDomesticIPAccessService(policy *domesticip.Policy, grants *DomesticIPGrantService) *DomesticIPAccessService {
	return &DomesticIPAccessService{policy: policy, grants: grants}
}

func (s *DomesticIPAccessService) Enabled() bool {
	return s != nil && s.policy != nil && s.policy.Enabled()
}

func (s *DomesticIPAccessService) Check(r *http.Request) error {
	if s == nil || s.policy == nil {
		return ErrDomesticIPUnavailable
	}
	if !s.policy.Enabled() {
		return nil
	}
	ip, required, err := s.policy.NeedsGrant(r)
	if err != nil {
		return ErrDomesticIPInvalid
	}
	if !required {
		return nil
	}
	if s.grants == nil {
		return ErrDomesticIPUnavailable
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	allowed, err := s.grants.HasActiveGrant(ctx, ip)
	if err != nil {
		return ErrDomesticIPUnavailable
	}
	if !allowed {
		return ErrDomesticIPDenied
	}
	return nil
}

// ValidateDomesticIP is called after key/user authentication, including read-only
// task routes and simple mode. Missing wiring must not disable an enabled policy.
func (s *APIKeyService) ValidateDomesticIP(r *http.Request) error {
	if s == nil {
		return ErrDomesticIPUnavailable
	}
	if s.cfg == nil || !s.cfg.DomesticIP.Enabled {
		return nil
	}
	if s.domesticIPAccess == nil || s.domesticIPAccess.policy == nil || !s.domesticIPAccess.policy.Enabled() {
		return ErrDomesticIPUnavailable
	}
	return s.domesticIPAccess.Check(r)
}
