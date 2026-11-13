package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// RegisterImageGenerationRoutes 注册生图页的面板接口。
// 与 batch image 不同，这些接口不经过 API Key 鉴权 —— 它们服务的是面板用户。
func RegisterImageGenerationRoutes(
	v1 *gin.RouterGroup,
	h *handler.Handlers,
	jwtAuth middleware.JWTAuthMiddleware,
	auditLog middleware.AuditLogMiddleware,
	settingService *service.SettingService,
	panelRateLimiter *middleware.PanelRateLimiter,
) {
	authenticated := v1.Group("")
	authenticated.Use(gin.HandlerFunc(jwtAuth))
	authenticated.Use(middleware.BackendModeUserGuard(settingService))
	authenticated.Use(panelRateLimiter.Global())
	authenticated.Use(gin.HandlerFunc(auditLog))
	{
		generations := authenticated.Group("/image-generations")
		{
			generations.GET("/capabilities", h.ImageGeneration.Capabilities)
			generations.POST("/estimate", h.ImageGeneration.Estimate)
			generations.GET("/history", h.ImageGeneration.ListHistory)
			generations.POST("/history", panelRateLimiter.Heavy(), h.ImageGeneration.Archive)
			// 归档单张最大 32MB，读回同理，按重查询限流。
			generations.GET("/history/:id/content", panelRateLimiter.Heavy(), h.ImageGeneration.GetContent)
			generations.DELETE("/history/:id", h.ImageGeneration.DeleteHistory)
		}
	}
}
