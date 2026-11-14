package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/gin-gonic/gin"
)

func registerAdminDomesticIPRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	grants := admin.Group("/domestic-ip/grants")
	grants.GET("", h.Admin.DomesticIP.List)
	grants.POST("", h.Admin.DomesticIP.Add)
	grants.DELETE("/:id", h.Admin.DomesticIP.Delete)
}
