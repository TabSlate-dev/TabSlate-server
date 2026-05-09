package app

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/tabslate/server/billing"
	"github.com/tabslate/server/db"
	"github.com/tabslate/server/internal/captcha"
	"github.com/tabslate/server/internal/handler"
	"github.com/tabslate/server/internal/infra"
	"github.com/tabslate/server/internal/mailer"
	"github.com/tabslate/server/internal/middleware"
	"github.com/tabslate/server/internal/search"
)

// Server is the HTTP server for the TabSlate backend.
// It is constructed with all dependencies injected so that the Cloud edition
// can reuse it with a different billing.Provider.
type Server struct {
	cfg          *Config
	db           *db.DB
	billing      billing.Provider
	captcha      *captcha.Verifier
	mailer       *mailer.Mailer
	search       *search.Client
	router       *gin.Engine
	ctx          context.Context
	infra        *infra.Providers
	infraCleanup func()
}

// New creates and configures the server. Captcha and mailer services are
// created automatically from Config so that external modules (Cloud) do not
// need to import internal packages. Call Run to start listening.
// ctx controls background goroutines (cleanup tasks); pass a context tied to
// the process lifetime, e.g. a signal-cancellable context from main.
func New(cfg *Config, database *db.DB, bp billing.Provider, ctx context.Context) *Server {
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

	sc := search.New(cfg.MeiliSearchHost, cfg.MeiliSearchAPIKey)
	if sc != nil {
		log.Println("meilisearch search indexing enabled")
	} else {
		log.Println("meilisearch not configured — search indexing disabled")
	}

	infraProviders, infraCleanup, err := infra.New(cfg.RedisURL)
	if err != nil {
		log.Fatalf("infra: %v", err)
	}

	r := gin.Default()
	if err := r.SetTrustedProxies(cfg.TrustedProxies); err != nil {
		log.Fatalf("router: SetTrustedProxies: %v", err)
	}

	s := &Server{
		cfg:          cfg,
		db:           database,
		billing:      bp,
		captcha:      cv,
		mailer:       m,
		search:       sc,
		router:       r,
		ctx:          ctx,
		infra:        infraProviders,
		infraCleanup: infraCleanup,
	}
	s.setupCORS()
	s.setupRoutes()
	cleanupH := handler.NewCleanupHandler(database, cfg.TrashGraceDays)
	go cleanupH.Run(ctx)
	return s
}

// RegisterWebhook registers an additional POST route for billing webhooks.
// Called by the Cloud edition after New() to attach the Lago webhook handler.
func (s *Server) RegisterWebhook(path string, h gin.HandlerFunc) {
	s.router.POST(path, h)
}

// SyncSubscription updates the local subscriptions table to match the billing
// platform's state. Called by the Cloud edition when a Lago webhook confirms
// a plan change, so that internal/plan quota checks stay accurate.
//
// For active plans supply the Lago plan code (e.g. "pro") and status "active".
// For terminated/cancelled subscriptions, supply planCode "free" and status "active"
// to fall the user back to the free tier automatically.
func (s *Server) SyncSubscription(ctx context.Context, userID, planCode, status string) error {
	now := time.Now().Unix()
	_, err := s.db.Exec(ctx,
		`UPDATE subscriptions SET plan = $1, status = $2, updated_at = $3 WHERE user_id = $4`,
		planCode, status, now, userID,
	)
	if err != nil {
		return fmt.Errorf("sync subscription: %w", err)
	}
	return nil
}

// Run starts the HTTP server and blocks until the process context is cancelled.
func (s *Server) Run() {
	addr := ":" + s.cfg.Port
	ln, err := net.Listen("tcp4", addr)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}

	httpSrv := &http.Server{Handler: s.router}
	log.Printf("TabSlate server listening on %s", addr)

	go func() {
		if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Fatalf("serve: %v", err)
		}
	}()

	<-s.ctx.Done()
	log.Println("shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Printf("server shutdown: %v", err)
	}
	s.infraCleanup()
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
	authH := handler.NewAuthHandler(s.db, s.cfg.JWTSecret, s.billing, s.captcha, s.mailer,
		s.infra.Limiter, s.infra.Cache,
		s.cfg.RegisterCaptchaThreshold, s.cfg.RegisterCaptchaWindow,
		s.cfg.OTPCaptchaThreshold, s.cfg.OTPCaptchaWindow)
	captchaH := handler.NewCaptchaHandler(s.cfg.ProsopoBundleURL)
	wsH := handler.NewWorkspaceHandler(s.db, s.infra.Hub)
	colH := handler.NewCollectionHandler(s.db, s.infra.Hub)
	bmH := handler.NewBookmarkHandler(s.db, s.search, s.infra.Hub)
	tagH := handler.NewTagHandler(s.db, s.infra.Hub)
	syncH := handler.NewSyncHandler(s.db, s.search, s.infra.Hub)
	searchH := handler.NewSearchHandler(s.search)
	sseH := handler.NewSSEHandler(s.infra.Hub, s.infra.Cache)
	billH := handler.NewBillingHandler(s.billing, s.infra.Cache)
	prefH := handler.NewPreferencesHandler(s.db)

	// ── Public routes ─────────────────────────────────────────────────────────
	s.router.GET("/captcha/widget", captchaH.Widget)
	s.router.GET("/captcha/widget.js", captchaH.WidgetJS)

	auth := s.router.Group("/auth")
	{
		auth.POST("/register", middleware.RateLimitByIP(s.infra.Limiter, s.cfg.RateLimitAuth, s.cfg.RateLimitAuthWindow), authH.Register)
		auth.POST("/login", middleware.RateLimitByIP(s.infra.Limiter, s.cfg.RateLimitAuth, s.cfg.RateLimitAuthWindow), authH.Login)
		auth.POST("/refresh", authH.Refresh)
		auth.POST("/logout", authH.Logout)
		auth.POST("/verify-email", middleware.RateLimitByIP(s.infra.Limiter, s.cfg.RateLimitAuth, s.cfg.RateLimitAuthWindow), authH.VerifyEmail)
		auth.POST("/resend-verification", middleware.RateLimitByIP(s.infra.Limiter, s.cfg.RateLimitAuth, s.cfg.RateLimitAuthWindow), authH.ResendVerification)
		auth.POST("/forgot-password", middleware.RateLimitByIP(s.infra.Limiter, s.cfg.RateLimitAuth, s.cfg.RateLimitAuthWindow), authH.ForgotPassword)
		auth.POST("/reset-password", middleware.RateLimitByIP(s.infra.Limiter, s.cfg.RateLimitAuth, s.cfg.RateLimitAuthWindow), authH.ResetPassword)
		auth.GET("/login-captcha-status", authH.LoginCaptchaStatus)
		auth.GET("/otp-captcha-status", authH.OTPCaptchaStatus)
		auth.GET("/register-captcha-status", authH.RegisterCaptchaStatus)
	}

	// ── SSE stream — unauthenticated (SSE token auth inside handler) ─────────
	s.router.GET("/sync/stream", sseH.Stream)

	// ── Protected routes ──────────────────────────────────────────────────────
	api := s.router.Group("/")
	api.Use(middleware.Auth(s.cfg.JWTSecret))
	{
		api.GET("/auth/me", authH.Me)

		api.GET("/preferences", prefH.Get)
		api.PUT("/preferences", prefH.Update)

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

		api.GET("/search", middleware.RateLimitByIP(s.infra.Limiter, s.cfg.RateLimitSearch, s.cfg.RateLimitSearchWindow), searchH.Search)

		api.GET("/tags", tagH.List)
		api.POST("/tags", tagH.Create)
		api.PUT("/tags/:id", tagH.Update)
		api.DELETE("/tags/:id", tagH.Delete)

		api.POST("/auth/sse-token", authH.IssueSSEToken)

		api.POST("/sync/push", func(c *gin.Context) {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 512*1024)
			c.Next()
		}, middleware.RateLimitByIP(s.infra.Limiter, s.cfg.RateLimitSyncPush, s.cfg.RateLimitSyncPushWindow), syncH.Push)
		api.GET("/sync/pull", middleware.RateLimitByIP(s.infra.Limiter, s.cfg.RateLimitSyncPull, s.cfg.RateLimitSyncPullWindow), syncH.Pull)

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
