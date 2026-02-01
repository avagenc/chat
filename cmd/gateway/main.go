package main

import (
	"log"
	"net/http"
	"net/url"

	"github.com/avagenc/api-gateway/internal/config"
	"github.com/avagenc/api-gateway/internal/gateway"
	"github.com/avagenc/api-gateway/internal/middleware"
	"github.com/avagenc/api-gateway/internal/redis"
	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/joho/godotenv"
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("Info: .env file not found, using system environment variables")
	}

	// 1. Load Configurations
	cfg := struct {
		app      *config.App
		server   *config.Server
		redis    *config.Redis
		security *config.Security
		target   *config.Target
	}{}

	var err error

	cfg.app, err = config.LoadApp()
	if err != nil {
		log.Fatalf("Failed to load app config: %v", err)
	}

	cfg.server, err = config.LoadServer()
	if err != nil {
		log.Fatalf("Failed to load server config: %v", err)
	}

	cfg.redis, err = config.LoadRedis()
	if err != nil {
		log.Fatalf("Failed to load redis config: %v", err)
	}

	cfg.security, err = config.LoadSecurity()
	if err != nil {
		log.Fatalf("Failed to load security config: %v", err)
	}

	cfg.target, err = config.LoadTarget()
	if err != nil {
		log.Fatalf("Failed to load target config: %v", err)
	}

	// 2. Initialize Redis
	redis, err := redis.NewClient(cfg.redis)
	if err != nil {
		log.Fatalf("Failed to connect to Redis: %v", err)
	}
	defer redis.Close()
	log.Println("Redis connected")

	// 3. Parse Target URLs
	target := struct {
		Nayo *url.URL
	}{}

	target.Nayo, err = url.Parse(cfg.target.Nayo)
	if err != nil {
		log.Fatalf("Invalid Nayo Target URL detected: %v", err)
	}

	// 4. Initialize Middleware
	mw := struct {
		JWT       *middleware.JWT
		Blocklist *middleware.Blocklist
	}{}

	mw.JWT, err = middleware.NewJWT(cfg.security.IdentitySupabaseJWKSURL)
	if err != nil {
		log.Fatalf("Failed to initialize JWT middleware: %v", err)
	}

	repo := gateway.NewRepository(redis)
	mw.Blocklist = middleware.NewBlocklist(repo)

	hdl := struct {
		Nayo *gateway.Handler
	}{
		Nayo: gateway.NewHandler(target.Nayo, cfg.security.APIKey),
	}

	// 6. Router Setup
	r := chi.NewRouter()

	r.Use(chiMiddleware.RequestID)
	r.Use(chiMiddleware.RealIP)
	r.Use(chiMiddleware.Logger)
	r.Use(chiMiddleware.Recoverer)

	r.Group(func(r chi.Router) {
		r.Use(mw.JWT.Authenticate)
		r.Use(mw.Blocklist.DenyBlocked)

		r.Mount("/nayo", http.StripPrefix("/nayo", http.HandlerFunc(hdl.Nayo.Proxy)))
	})

	// 7. Start Server
	server := &http.Server{
		Addr:         ":" + cfg.server.Port,
		Handler:      r,
		ReadTimeout:  cfg.server.ReadTimeout,
		WriteTimeout: cfg.server.WriteTimeout,
		IdleTimeout:  cfg.server.IdleTimeout,
	}

	log.Printf("Starting %s (%s) on port %s", cfg.app.Name, cfg.app.Version, cfg.server.Port)
	log.Printf("Forwarding /nayo -> %s", cfg.target.Nayo)

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Failed to start server: %v", err)
	}
}
