package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/domesticip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type DomesticIPHandler struct {
	grants *service.DomesticIPGrantService
	policy *domesticip.Policy
	access *service.DomesticIPAccessService
}

func NewDomesticIPHandler(grants *service.DomesticIPGrantService, policy *domesticip.Policy, access *service.DomesticIPAccessService) *DomesticIPHandler {
	return &DomesticIPHandler{grants: grants, policy: policy, access: access}
}

func (h *DomesticIPHandler) user(c *gin.Context) (int64, bool) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return 0, false
	}
	if h == nil || h.policy == nil || !h.policy.Enabled() || h.grants == nil {
		response.NotFound(c, "IP自助放行尚未启用")
		return 0, false
	}
	c.Header("Cache-Control", "no-store")
	return subject.UserID, true
}

func (h *DomesticIPHandler) Status(c *gin.Context) {
	userID, ok := h.user(c)
	if !ok {
		return
	}
	h.status(c, userID)
}

func (h *DomesticIPHandler) status(c *gin.Context, userID int64) {
	currentIP, _ := h.policy.ResolveClientIP(c.Request)
	status, err := h.grants.Status(c.Request.Context(), userID, currentIP, true)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, status)
}

func (h *DomesticIPHandler) Add(c *gin.Context)     { h.mutate(c, "add") }
func (h *DomesticIPHandler) Replace(c *gin.Context) { h.mutate(c, "replace") }
func (h *DomesticIPHandler) Delete(c *gin.Context)  { h.mutate(c, "delete") }

func (h *DomesticIPHandler) mutate(c *gin.Context, operation string) {
	userID, ok := h.user(c)
	if !ok {
		return
	}
	currentIP, err := h.policy.ResolveClientIP(c.Request)
	if err != nil {
		response.ErrorFrom(c, service.ErrDomesticIPInvalid)
		return
	}
	var id int64
	if operation != "add" {
		id, err = strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil || id <= 0 {
			response.BadRequest(c, "无效的 IP 记录")
			return
		}
	}
	if operation != "delete" {
		// expected_ip binds the confirmation to its preview; it is never the
		// address passed to the service. Arbitrary IP and extra fields are rejected.
		var req struct {
			ExpectedIP string `json:"expected_ip"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			response.BadRequest(c, "请刷新当前 IP 后重试")
			return
		}
		if decoder.Decode(new(any)) != io.EOF {
			response.BadRequest(c, "无效的请求内容")
			return
		}
		expected, err := domesticip.NormalizePublicIP(req.ExpectedIP)
		if err != nil || expected != currentIP {
			response.ErrorWithDetails(c, http.StatusConflict, "当前网络 IP 已变化，请刷新后重新确认", "IP_CHANGED", nil)
			return
		}
	}
	switch operation {
	case "add":
		_, err = h.grants.Add(c.Request.Context(), userID, currentIP)
	case "replace":
		_, err = h.grants.Replace(c.Request.Context(), userID, id, currentIP)
	case "delete":
		_, err = h.grants.Delete(c.Request.Context(), userID, id, currentIP)
	}
	if response.ErrorFrom(c, err) {
		return
	}
	h.status(c, userID)
}

// RequireAccess protects panel endpoints that return generated content. Login,
// recharge, and grant management remain reachable from an unregistered network.
func (h *DomesticIPHandler) RequireAccess(c *gin.Context) {
	if h == nil {
		response.ErrorFrom(c, service.ErrDomesticIPUnavailable)
		c.Abort()
		return
	}
	if err := h.access.Check(c.Request); err != nil {
		response.ErrorFrom(c, err)
		c.Abort()
		return
	}
	c.Next()
}

// NetworkAccess is a Caddy forward_auth target for Sub2API's signed image assets.
// It checks only source-network admission; storage signatures still authorize
// individual objects. ResolveClientIP rejects every non-loopback direct peer.
func (h *DomesticIPHandler) NetworkAccess(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if h == nil || h.policy == nil || !h.policy.Enabled() || !h.access.Enabled() {
		response.ErrorFrom(c, service.ErrDomesticIPUnavailable)
		return
	}
	if err := h.access.Check(c.Request); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	c.Status(http.StatusOK)
}
