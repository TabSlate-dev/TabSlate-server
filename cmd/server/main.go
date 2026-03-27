package main

import (
	"log"
	"os"
	"path/filepath"
	"runtime"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"github.com/lieutenant/tabmaster/internal/db"
	"github.com/lieutenant/tabmaster/internal/handler"
	"github.com/lieutenant/tabmaster/internal/middleware"
)

func main() {
	// Load .env (ignored in production where real env vars are set)
	_ = godotenv.Load()

	// ── Database ─────────────────────────────────────────────────────────────
	dsn := mustEnv("DATABASE_URL")
	database, err := db.Open(dsn)
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}
	defer database.Close()

	if err := db.Migrate(database, findSchema()); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	log.Println("database ready")

	// ── Gin ──────────────────────────────────────────────────────────────────
	if os.Getenv("GIN_MODE") != "" {
		gin.SetMode(os.Getenv("GIN_MODE"))
	}
	r := gin.Default()

	// CORS — Chrome extensions send Origin: chrome-extension://<id>
	r.Use(cors.New(cors.Config{
		AllowOriginFunc: func(origin string) bool {
			return origin == "" ||
				origin == "null" ||
				(len(origin) >= 19 && origin[:19] == "chrome-extension://")
		},
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization"},
		AllowCredentials: true,
	}))

	// ── Handlers ─────────────────────────────────────────────────────────────
	jwtSecret := mustEnv("JWT_SECRET")
	authH := handler.NewAuthHandler(database, jwtSecret)
	wsH := handler.NewWorkspaceHandler(database)
	colH := handler.NewCollectionHandler(database)
	bmH := handler.NewBookmarkHandler(database)
	tagH := handler.NewTagHandler(database)
	syncH := handler.NewSyncHandler(database)

	// ── Public routes ─────────────────────────────────────────────────────────
	auth := r.Group("/auth")
	{
		auth.POST("/register", authH.Register)
		auth.POST("/login", authH.Login)
		auth.POST("/refresh", authH.Refresh)
		auth.POST("/logout", authH.Logout)
	}

	// ── Protected routes ──────────────────────────────────────────────────────
	api := r.Group("/")
	api.Use(middleware.Auth(jwtSecret))
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
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("listening on :%s", port)
	if err := r.Run(":" + port); err != nil {
		log.Fatal(err)
	}
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("required env var %s is not set", key)
	}
	return v
}

func findSchema() string {
	// Production: schema.sql next to the binary
	exe, _ := os.Executable()
	candidate := filepath.Join(filepath.Dir(exe), "schema.sql")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	// Development: go up from cmd/server/ to repo root
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "schema.sql")
}
