package admin

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type DomesticIPHandler struct {
	service *service.AdminDomesticIPService
}

func NewDomesticIPHandler(svc *service.AdminDomesticIPService) *DomesticIPHandler {
	return &DomesticIPHandler{service: svc}
}

func adminDomesticIPActor(c *gin.Context) (service.AdminDomesticIPActor, bool) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "Unauthorized")
		return service.AdminDomesticIPActor{}, false
	}
	role, ok := middleware.GetUserRoleFromContext(c)
	if !ok || role != "admin" {
		response.Forbidden(c, "Admin access required")
		return service.AdminDomesticIPActor{}, false
	}
	return service.AdminDomesticIPActor{
		UserID: subject.UserID, AuthMethod: c.GetString("auth_method"),
		SourceIP: c.ClientIP(), RequestID: c.Writer.Header().Get("X-Request-ID"),
	}, true
}

func (h *DomesticIPHandler) List(c *gin.Context) {
	if _, ok := adminDomesticIPActor(c); !ok {
		return
	}
	page, size := response.ParsePagination(c)
	result, err := h.service.List(c.Request.Context(), page, size, c.Query("search"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func (h *DomesticIPHandler) Add(c *gin.Context) {
	actor, ok := adminDomesticIPActor(c)
	if !ok {
		return
	}
	var request struct {
		IP   string `json:"ip" binding:"required,max=128"`
		Note string `json:"note" binding:"max=500"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		response.BadRequest(c, "请输入单个公网 IP，备注最多 500 个字符")
		return
	}
	item, err := h.service.Add(c.Request.Context(), request.IP, request.Note, actor)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}

func (h *DomesticIPHandler) Delete(c *gin.Context) {
	actor, ok := adminDomesticIPActor(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "无效的 IP 记录")
		return
	}
	if err = h.service.Delete(c.Request.Context(), id, actor); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"deleted": true})
}
