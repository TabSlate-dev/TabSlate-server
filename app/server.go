package app

import (
	"log"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/tabslate/server/billing"
	"github.com/tabslate/server/db"
	"github.com/tabslate/server/internal/captcha"
	"github.com/tabslate/server/internal/handler"
	"github.com/tabslate/server/internal/mailer"
	"github.com/tabslate/server/internal/middleware"
)

// Server is the HTTP server for the TabSlate backend.
// It is constructed with all dependencies injected so that the Cloud edition
// can reuse it with a different billing.Provider.
type Server struct {
	cfg     *Config
	db      *db.DB
	billing billing.Provider
	captcha *captcha.Verifier
	mailer  *mailer.Mailer
	router  *gin.Engine
}

// New creates and configures the server. Captcha and mailer services are
// created automatically from Config so that external modules (Cloud) do not
// need to import internal packages. Call Run to start listening.
func New(cfg *Config, database *db.DB, bp billing.Provider) *Server {
	if cfg.GinMode != "" {
		gin.SetMode(cfg.GinMode)
	}

	// Create captcha verifier from config.
	cv := captcha.New(cfg.ProsopoSecret, cfg.ProsopoServerURL)
	if cv.Enabled() {
		log.Println("prosopo captcha enabled")
	}

	// Create mailer from config.
	m := mailer.New(mailer.Config{
		Provider:     cfg.MailProvider,
		SMTPHost:     cfg.SMTPHost,
		SMTPPort:     cfg.SMTPPort,
		SMTPUser:     cfg.SMTPUser,
		SMTPPassword: cfg.SMTPPassword,
		SMTPFrom:     cfg.SMTPFrom,
		ResendAPIKey: cfg.ResendAPIKey,
		ResendFrom:   cfg.ResendFrom,
	})
	if m.Enabled() {
		log.Printf("email provider: %s", cfg.MailProvider)
	} else {
		log.Println("email disabled — users will be auto-verified")
	}

	s := &Server{
		cfg:     cfg,
		db:      database,
		billing: bp,
		captcha: cv,
		mailer:  m,
		router:  gin.Default(),
	}
	s.setupCORS()
	s.setupRoutes()
	return s
}

// RegisterWebhook registers an additional POST route for billing webhooks.
// Called by the Cloud edition after New() to attach the Lago webhook handler.
func (s *Server) RegisterWebhook(path string, h gin.HandlerFunc) {
	s.router.POST(path, h)
}

// Run starts the HTTP server and blocks until it exits.
func (s *Server) Run() {
	addr := ":" + s.cfg.Port
	log.Printf("TabSlate server listening on %s", addr)
	if err := s.router.Run(addr); err != nil {
		log.Fatal(err)
	}
}

func (s *Server) setupCORS() {
	s.router.Use(cors.New(cors.Config{
		// Allow Chrome extension origins and direct (empty) origins in dev
		AllowOriginFunc: func(origin string) bool {
			return origin == "" ||
				origin == "null" ||
				(len(origin) >= 19 && origin[:19] == "chrome-extension://")
		},
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization"},
		AllowCredentials: true,
	}))
}

func (s *Server) setupRoutes() {
	authH := handler.NewAuthHandler(s.db, s.cfg.JWTSecret, s.billing, s.captcha, s.mailer, s.cfg.VerifyBaseURL)
	wsH := handler.NewWorkspaceHandler(s.db)
	colH := handler.NewCollectionHandler(s.db)
	bmH := handler.NewBookmarkHandler(s.db)
	tagH := handler.NewTagHandler(s.db)
	syncH := handler.NewSyncHandler(s.db)
	billH := handler.NewBillingHandler(s.billing)

	// ── Rate limiters ─────────────────────────────────────────────────────────
	// 10 requests/minute per IP for auth endpoints (register, login, resend).
	authRL := middleware.NewRateLimiter(10, 1*time.Minute)

	// ── Public routes ─────────────────────────────────────────────────────────
	auth := s.router.Group("/auth")
	{
		auth.POST("/register", middleware.RateLimitByIP(authRL), authH.Register)
		auth.POST("/login", middleware.RateLimitByIP(authRL), authH.Login)
		auth.POST("/refresh", authH.Refresh)
		auth.POST("/logout", authH.Logout)
		auth.GET("/verify-email", authH.VerifyEmail)
		auth.POST("/resend-verification", middleware.RateLimitByIP(authRL), authH.ResendVerification)
		auth.GET("/login-captcha-status", authH.LoginCaptchaStatus)
	}

	// ── Protected routes ──────────────────────────────────────────────────────
	api := s.router.Group("/")
	api.Use(middleware.Auth(s.cfg.JWTSecret))
	{
		api.GET("/auth/me", authH.Me)

		api.GET("/workspaces", wsH.List)
		api.POST("/workspaces", wsH.Create)
		api.PUT("/workspaces/:id", wsH.Update)
		api.DELETE("/workspaces/:id", wsH.Delete)

		api.GET("/collections", colH.List)
		api.POST("/collections", colH.Create)
		api.PUT("/collections/:id", colH.Update)
		api.DELETE("/collections/:id", colH.Delete)

		api.GET("/bookmarks", bmH.List)
		api.POST("/bookmarks", bmH.Create)
		api.PUT("/bookmarks/:id", bmH.Update)
		api.DELETE("/bookmarks/:id", bmH.Delete)

		api.GET("/tags", tagH.List)
		api.POST("/tags", tagH.Create)
		api.PUT("/tags/:id", tagH.Update)
		api.DELETE("/tags/:id", tagH.Delete)

		api.GET("/sync", syncH.Pull)
		api.POST("/sync", syncH.Push)

		// Billing — behaviour varies by provider (OSS vs Cloud)
		bill := api.Group("/api")
		{
			bill.GET("/subscription", billH.GetSubscription)
			bill.GET("/limits", billH.GetLimits)
			bill.POST("/checkout", billH.CreateCheckout)
			bill.GET("/invoices", billH.ListInvoices)
			bill.DELETE("/subscription", billH.CancelSubscription)
		}
	}
}
