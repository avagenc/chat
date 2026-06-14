package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	gcptasks "cloud.google.com/go/cloudtasks/apiv2"
	"github.com/avagenc/platform/internal/agent"
	"github.com/avagenc/platform/internal/identity"
	intpostera "github.com/avagenc/platform/internal/postera"
	"github.com/avagenc/platform/internal/zep"
	zepclient "github.com/getzep/zep-go/v3/client"
	zepoption "github.com/getzep/zep-go/v3/option"
	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	apihttp "go.naturallyfunny.dev/api/http"
	apisession "go.naturallyfunny.dev/api/session"
	apitime "go.naturallyfunny.dev/api/time"
	apiuser "go.naturallyfunny.dev/api/user"
	"go.naturallyfunny.dev/postera"
	posteracloudtasks "go.naturallyfunny.dev/postera/cloudtasks"
	posterapostgres "go.naturallyfunny.dev/postera/postgres"
	"google.golang.org/api/idtoken"
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("info: .env file not found, using system environment variables")
	}

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

	posteraDBURL := os.Getenv("POSTERA_DB_URL")
	if posteraDBURL == "" {
		log.Fatal("fatal: POSTERA_DB_URL is required")
	}
	posteraDBPool, err := pgxpool.New(context.Background(), posteraDBURL)
	if err != nil {
		log.Fatalf("fatal: init postera db pool: %v", err)
	}
	defer posteraDBPool.Close()
	posteraStore, err := posterapostgres.NewStore(
		context.Background(),
		posteraDBPool,
		posterapostgres.WithAutoMigrate(),
	)
	if err != nil {
		log.Fatalf("fatal: init postera store: %v", err)
	}

	gcpProjectID := os.Getenv("GCP_PROJECT_ID")
	if gcpProjectID == "" {
		log.Fatal("fatal: GCP_PROJECT_ID is required")
	}
	cloudTasksLocationID := os.Getenv("CLOUD_TASKS_LOCATION_ID")
	if cloudTasksLocationID == "" {
		log.Fatal("fatal: CLOUD_TASKS_LOCATION_ID is required")
	}
	cloudTasksQueueID := os.Getenv("CLOUD_TASKS_QUEUE_ID")
	if cloudTasksQueueID == "" {
		log.Fatal("fatal: CLOUD_TASKS_QUEUE_ID is required")
	}
	cloudTasksClient, err := gcptasks.NewClient(context.Background())
	if err != nil {
		log.Fatalf("fatal: init cloud tasks client: %v", err)
	}
	defer cloudTasksClient.Close()
	posteraEnqueuer, err := posteracloudtasks.NewEnqueuer(
		cloudTasksClient,
		gcpProjectID,
		cloudTasksLocationID,
		cloudTasksQueueID,
	)
	if err != nil {
		log.Fatalf("fatal: init postera enqueuer: %v", err)
	}

	// Postarius reads the caller's human identity from the same context key
	// apiuser.HTTPWithID populates, so ListUpcoming/Cancel scope to the
	// authenticated user. Access control stays in the gateway (see internal/postera).
	postarius := postera.New(
		posteraStore,
		posteraEnqueuer,
		postera.WithHumanFromContext(apiuser.ContextKey),
	)
	posteraHandler := intpostera.NewHandler(
		intpostera.NewService(postarius),
	)

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
		}{"platform", "v0.0.1", appEnv, "UP"})
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

		r.Route("/postera", func(r chi.Router) {
			r.Get("/", posteraHandler.ListUpcoming)
			r.Delete("/{posterum-id}", posteraHandler.Cancel)
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
	log.Printf("Starting platform service [%s] on port %s", appEnv, port)
	if err := s.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("fatal: failed to start server: %v", err)
	}
}
