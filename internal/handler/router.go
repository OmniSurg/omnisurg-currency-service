package handler

import (
	"context"
	"net/http"
	"strings"

	api "github.com/OmniSurg/omnisurg-currency-service/internal/generated/api"
	"github.com/OmniSurg/omnisurg-currency-service/internal/model"
	"github.com/OmniSurg/omnisurg-currency-service/internal/service"
	mw "github.com/OmniSurg/omnisurg-go-common/middleware"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// RouterConfig is the dependency set for NewRouter.
type RouterConfig struct {
	Rates       *service.CurrencyService
	Config      *service.ConfigService
	Audit       AuditQuerier
	JWTSecret   string
	Env         string
	BaseLogger  zerolog.Logger
	CORSOrigins []string
	Ping        func(context.Context) error
}

// NewRouter builds the gin engine: middleware chain, public health, JWT
// protected rate and config routes, and per-route RBAC.
func NewRouter(cfg RouterConfig) http.Handler {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(mw.RequestID())
	r.Use(mw.Logger(cfg.BaseLogger))
	r.Use(mw.Recovery())
	r.Use(corsMiddleware(cfg.CORSOrigins))

	h := &Handler{rates: cfg.Rates, config: cfg.Config, audit: cfg.Audit, ping: cfg.Ping}
	wrapper := api.ServerInterfaceWrapper{
		Handler: h,
		ErrorHandler: func(c *gin.Context, err error, _ int) {
			respondError(c, model.ErrValidation.WithDetails([]map[string]string{{"field": "request", "issue": err.Error()}}))
		},
	}

	grp := r.Group("/api/v1/currency")
	// Public, pre-auth.
	grp.GET("/health", wrapper.GetHealth)

	authed := grp.Group("", mw.JWTAuth(cfg.JWTSecret))
	// Reads are available to any authenticated caller (billing-service and the
	// staff app both quote conversions). Tenant scope on config is enforced by
	// RLS plus the caller tenant claim.
	authed.GET("/rates/latest", wrapper.GetLatestRate)
	authed.POST("/convert", wrapper.ConvertAmount)
	authed.GET("/config", wrapper.GetTenantConfig)

	// Refresh pulls from the upstream source; provider scoped (platform action).
	authed.POST("/refresh", mw.RequireRole(
		model.RoleProviderSuperAdmin, model.RoleProviderSupport, model.RoleProviderBilling,
	), wrapper.RefreshRates)

	// Manual override and config update are tenant scoped admin actions.
	authed.POST("/rates/manual", mw.RequireRole(model.RolePracticeAdmin), wrapper.SetManualRate)
	authed.PUT("/config", mw.RequireRole(model.RolePracticeAdmin), wrapper.UpdateTenantConfig)

	if cfg.Env != "production" {
		authed.GET("/_debug/audit", h.debugAudit)
	}
	return r
}

func (h *Handler) debugAudit(c *gin.Context) {
	caller, ok := callerFrom(c)
	if !ok {
		respondError(c, model.ErrTenantMissing)
		return
	}
	action := c.Query("action")
	var actor *uuid.UUID
	if a := c.Query("actor"); a != "" {
		if id, err := uuid.Parse(a); err == nil {
			actor = &id
		}
	}
	rows, err := h.audit.Query(c.Request.Context(), caller.TenantID, action, actor)
	if err != nil {
		respondError(c, err)
		return
	}
	respondSuccess(c, http.StatusOK, gin.H{"count": len(rows)})
}

func corsMiddleware(origins []string) gin.HandlerFunc {
	allowed := make(map[string]struct{}, len(origins))
	for _, o := range origins {
		allowed[strings.TrimSpace(o)] = struct{}{}
	}
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if _, ok := allowed[origin]; ok {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
			c.Header("Access-Control-Allow-Headers", "Authorization,Content-Type,X-Tenant-ID,X-Request-ID")
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
