package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/avagenc/api-gateway/internal/agent"
	"github.com/avagenc/api-gateway/internal/identity"
	"github.com/avagenc/api-gateway/internal/zep"
	zepclient "github.com/getzep/zep-go/v3/client"
	zepoption "github.com/getzep/zep-go/v3/option"
	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/joho/godotenv"
	"google.golang.org/api/idtoken"
	apihttp "go.naturallyfunny.dev/api/http"
	apisession "go.naturallyfunny.dev/api/session"
	apitime "go.naturallyfunny.dev/api/time"
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("info: .env file not found, using system environment variables")
	}

	// redisURL := os.Getenv("REDIS_URL")
	// if redisURL == "" {
	// 	log.Fatal("fatal: REDIS_URL is required")
	// }
	// redisOpts, err := redis.ParseURL(redisURL)
	// if err != nil {
	// 	log.Fatalf("fatal: parse redis URL: %v", fmt.Errorf("unable to parse redis config: %w", err))
	// }
	// redisOpts.PoolSize = 20
	// redisOpts.MinIdleConns = 5
	// redisClient := redis.NewClient(redisOpts)
	// pingCtx, pingCancel := context.WithTimeout(context.Background(), 5*time.Second)
	// defer pingCancel()
	// if err := redisClient.Ping(pingCtx).Err(); err != nil {
	// 	log.Fatalf("fatal: connect to redis: %v", err)
	// }
	// defer redisClient.Close()
	// log.Println("Redis connected")
	// paymentGuard := identity.NewPaymentGuard(redisClient)

	jwksURL := os.Getenv("IDENTITY_JWKS_URL")
	if jwksURL == "" {
		log.Fatal("fatal: IDENTITY_JWKS_URL is required")
	}
	jwtAuthenticator, err := identity.NewJWTAuthenticator(jwksURL)
	if err != nil {
		log.Fatalf("fatal: build JWT authenticator: %v", err)
	}

	zepAPIKey := os.Getenv("ZEP_API_KEY")
	if zepAPIKey == "" {
		log.Fatal("fatal: ZEP_API_KEY is required")
	}
	zepClient := zepclient.NewClient(zepoption.WithAPIKey(zepAPIKey))
	zepService := zep.NewService(zepClient)
	zepHandler := zep.NewHandler(zepService)

	avaURL := os.Getenv("AVA_URL")
	if avaURL == "" {
		log.Fatal("fatal: AVA_URL is required")
	}
	avaHandler, err := agent.NewHandler(avaURL, nil)
	if err != nil {
		log.Fatalf("fatal: build ava handler: %v", err)
	}

	zeeURL := os.Getenv("ZEE_URL")
	if zeeURL == "" {
		log.Fatal("fatal: ZEE_URL is required")
	}
	zeeOIDC, err := idtoken.NewClient(context.Background(), zeeURL)
	if err != nil {
		log.Fatalf("fatal: build OIDC client for zee: %v", err)
	}
	zeeHandler, err := agent.NewHandler(zeeURL, zeeOIDC.Transport)
	if err != nil {
		log.Fatalf("fatal: build zee handler: %v", err)
	}

	appEnv := os.Getenv("APP_ENV")
	if appEnv == "" {
		log.Fatal("fatal: APP_ENV is required")
	}

	r := chi.NewRouter()

	r.Use(chiMiddleware.RequestID)
	r.Use(chiMiddleware.RealIP)
	r.Use(chiMiddleware.Logger)
	r.Use(chiMiddleware.Recoverer)

	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		apihttp.WriteJSON(w, http.StatusOK, struct {
			Service     string `json:"service"`
			Version     string `json:"version"`
			Environment string `json:"environment"`
			Status      string `json:"status"`
		}{"platform-api", "v0.0.1", appEnv, "UP"})
	})

	r.Group(func(r chi.Router) {
		r.Use(jwtAuthenticator.Authenticate)

		r.Route("/ava", func(r chi.Router) {
			r.Group(func(r chi.Router) {
				r.Use(apitime.HTTPWithZone)
				r.Use(apisession.HTTPWithID)
				r.Mount("/chat", http.StripPrefix("/ava", http.HandlerFunc(avaHandler.Proxy)))
			})
			r.Mount("/", http.StripPrefix("/ava", http.HandlerFunc(avaHandler.Proxy)))
		})

		r.Route("/zee", func(r chi.Router) {
			r.Group(func(r chi.Router) {
				r.Use(apitime.HTTPWithZone)
				r.Use(apisession.HTTPWithID)
				r.Mount("/chat", http.StripPrefix("/zee", http.HandlerFunc(zeeHandler.Proxy)))
			})
			r.Mount("/", http.StripPrefix("/zee", http.HandlerFunc(zeeHandler.Proxy)))
		})

		r.Route("/sessions", func(r chi.Router) {
			r.Get("/{session-id}/messages", zepHandler.GetMessages)
			r.Delete("/{session-id}/messages", zepHandler.ClearMessages)
		})

	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	s := &http.Server{
		Addr:         ":" + port,
		Handler:      r,
		ReadTimeout:  16 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	log.Printf("In the name of Allah, The Most Compassionate, The Most Merciful")
	log.Printf("Starting gateway service [%s] on port %s", appEnv, port)
	if err := s.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("fatal: failed to start server: %v", err)
	}
}
