package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	gcptasks "cloud.google.com/go/cloudtasks/apiv2"
	"github.com/avagenc/chat/internal/agent"
	"github.com/avagenc/chat/internal/memory"
	memoryzep "github.com/avagenc/chat/internal/memory/zep"
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
	"go.naturallyfunny.dev/tuya"
	"go.naturallyfunny.dev/tuya/cloud"
	tuyapostgres "go.naturallyfunny.dev/tuya/postgres"
	"google.golang.org/adk/model/gemini"
	"google.golang.org/genai"
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("info: .env file not found, using system environment variables")
	}

	// TEMP: JWT bearer auth disabled. Identity is taken straight from the user-id
	// header via apiuser.HTTPWithID (see route group below). Restore
	// jwtAuthenticator.Authenticate before any non-trusted deployment.
	//
	// jwksURL := os.Getenv("IDENTITY_JWKS_URL")
	// if jwksURL == "" {
	// 	log.Fatal("fatal: IDENTITY_JWKS_URL is required")
	// }
	// jwtAuthenticator, err := identity.NewJWTAuthenticator(jwksURL)
	// if err != nil {
	// 	log.Fatalf("fatal: build JWT authenticator: %v", err)
	// }

	zepAPIKey := os.Getenv("ZEP_API_KEY")
	if zepAPIKey == "" {
		log.Fatal("fatal: ZEP_API_KEY is required")
	}
	zepClient := zepclient.NewClient(zepoption.WithAPIKey(zepAPIKey))
	sessionService := memory.NewSessionService(memoryzep.NewSessionStore(zepClient))
	knowledgeService := memory.NewKnowledgeService(memoryzep.NewKnowledgeStore(zepClient))

	// Agents now run in-process over the shared Zep backend (zepClient) instead
	// of being reverse-proxied to AVA_URL/ZEE_URL. agent.Build composes the whole
	// roster (Ava + specialists), their per-channel Zep runners, and Ava's
	// in-process delegation graph, returning the human-facing chat Service.
	geminiAPIKey := os.Getenv("GEMINI_API_KEY")
	if geminiAPIKey == "" {
		log.Fatal("fatal: GEMINI_API_KEY is required")
	}
	model, err := gemini.NewModel(context.Background(), "gemini-2.5-flash", &genai.ClientConfig{
		APIKey: geminiAPIKey,
	})
	if err != nil {
		log.Fatalf("fatal: build gemini model: %v", err)
	}

	// Zee is a Tuya tool-using agent. Its per-user Tuya UID is resolved from a
	// PostgreSQL account store (ZEE_DB_URL), self-migrating on boot.
	tuyaAccessID := os.Getenv("TUYA_ACCESS_ID")
	if tuyaAccessID == "" {
		log.Fatal("fatal: TUYA_ACCESS_ID is required")
	}
	tuyaAccessSecret := os.Getenv("TUYA_ACCESS_SECRET")
	if tuyaAccessSecret == "" {
		log.Fatal("fatal: TUYA_ACCESS_SECRET is required")
	}
	tuyaBaseURL := os.Getenv("TUYA_BASE_URL")
	if tuyaBaseURL == "" {
		log.Fatal("fatal: TUYA_BASE_URL is required")
	}
	zeeDBURL := os.Getenv("ZEE_DB_URL")
	if zeeDBURL == "" {
		log.Fatal("fatal: ZEE_DB_URL is required")
	}
	tuyaDBPool, err := pgxpool.New(context.Background(), zeeDBURL)
	if err != nil {
		log.Fatalf("fatal: init tuya db pool: %v", err)
	}
	defer tuyaDBPool.Close()
	tuyaAccountStore, err := tuyapostgres.NewAccountStore(
		context.Background(),
		tuyaDBPool,
		tuyapostgres.WithAutoMigrate(),
	)
	if err != nil {
		log.Fatalf("fatal: init tuya account store: %v", err)
	}
	tuyaCloudClient, err := cloud.New(tuyaAccessID, tuyaAccessSecret, tuyaBaseURL)
	if err != nil {
		log.Fatalf("fatal: build tuya cloud client: %v", err)
	}
	tuyaClient := tuya.New(cloud.NewIoT(tuyaCloudClient), tuyaAccountStore)

	chatService, err := agent.Build(model, zepClient, tuyaClient)
	if err != nil {
		log.Fatalf("fatal: build chat service: %v", err)
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
	// authenticated user. Access control stays in the gateway (see internal/memory).
	postarius := postera.New(
		posteraStore,
		posteraEnqueuer,
		postera.WithHumanFromContext(apiuser.ContextKey),
	)

	// One handler fronts the whole memory family: episodic (sessions), semantic
	// (knowledge graph), and prospective (postera) memory.
	memoryHandler := memory.NewHandler(sessionService, knowledgeService, postarius)

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
		r.Use(apiuser.HTTPWithID) // TEMP: replaces jwtAuthenticator.Authenticate

		// POST /{agent}/chat — in-process group chat. {agent} is the dispatch key
		// (e.g. "ava", "zee"); Ava delegates to specialists via sub-agent tool
		// calls inside its own run, over the shared session.
		r.Group(func(r chi.Router) {
			r.Use(apitime.HTTPWithZone)
			r.Use(apisession.HTTPWithID)
			r.Post("/{agent}/chat", chatService.Chat)
		})

		r.Route("/sessions", func(r chi.Router) {
			r.Get("/{session-id}/messages", memoryHandler.GetMessages)
			r.Delete("/{session-id}/messages", memoryHandler.ClearMessages)
		})

		r.Route("/memory", func(r chi.Router) {
			r.Get("/", memoryHandler.GetKnowledge)
			r.Delete("/", memoryHandler.DeleteKnowledge)
		})

		r.Route("/postera", func(r chi.Router) {
			r.Get("/", memoryHandler.ListUpcoming)
			r.Delete("/{posterum-id}", memoryHandler.Cancel)
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
