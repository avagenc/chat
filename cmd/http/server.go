package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	gcptasks "cloud.google.com/go/cloudtasks/apiv2"
	"github.com/avagenc/chat/internal/agent"
	internalava "github.com/avagenc/chat/internal/agent/ava"
	"github.com/avagenc/chat/internal/agent/specialist"
	"github.com/avagenc/chat/internal/memory"
	internalzep "github.com/avagenc/chat/internal/zep"
	zepclient "github.com/getzep/zep-go/v3/client"
	zepoption "github.com/getzep/zep-go/v3/option"
	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"go.avagenc.com/ava"
	"go.avagenc.com/zee"
	adkzep "go.naturallyfunny.dev/adk/zep"
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
	"google.golang.org/adk/runner"
	"google.golang.org/genai"
)

func main() {
	// 0. Load environment vars
	if err := godotenv.Load(); err != nil {
		log.Println("info: .env file not found, using system environment variables")
	}
	// 1. Agent
	// 1. 0. ADK Session Service
	zepAPIKey := os.Getenv("ZEP_API_KEY")
	if zepAPIKey == "" {
		log.Fatal("fatal: ZEP_API_KEY is required")
	}
	zepClient := zepclient.NewClient(zepoption.WithAPIKey(zepAPIKey))
	agentSessSvc := adkzep.NewSessionService(
		zepClient,
		adkzep.WithSpeakerResolver(adkzep.SpeakerFromContext()),
		adkzep.WithInstruction(agent.SessionInstructionDeltaKey),
		adkzep.WithMessageHistoryLength(16),
		adkzep.WithTimeHarness(adkzep.TZFromContext()),
	)
	// 1. 1. ADK Memory Service
	agentMemSvc := adkzep.NewMemoryService(zepClient)
	// 1. 2. Model
	geminiAPIKey := os.Getenv("GEMINI_API_KEY")
	if geminiAPIKey == "" {
		log.Fatal("fatal: GEMINI_API_KEY is required")
	}
	agentModel, err := gemini.NewModel(context.Background(), "gemini-3-flash-preview", &genai.ClientConfig{
		APIKey: geminiAPIKey,
	})
	if err != nil {
		log.Fatalf("fatal: build gemini model: %v", err)
	}
	// 1. 3. Zee
	// 1. 3. 0. Tuya App Client
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
	tuyaCloudClient, err := cloud.New(tuyaAccessID, tuyaAccessSecret, tuyaBaseURL)
	if err != nil {
		log.Fatalf("fatal: build tuya cloud client: %v", err)
	}
	tuyaIoTClient := cloud.NewIoT(tuyaCloudClient)
	zeeDBURL := os.Getenv("ZEE_DB_URL")
	if zeeDBURL == "" {
		log.Fatal("fatal: ZEE_DB_URL is required")
	}
	zeeDBPool, err := pgxpool.New(context.Background(), zeeDBURL)
	if err != nil {
		log.Fatalf("fatal: init tuya db pool: %v", err)
	}
	defer zeeDBPool.Close()
	tuyaAccountStore, err := tuyapostgres.NewAccountStore(
		context.Background(),
		zeeDBPool,
		tuyapostgres.WithAutoMigrate(),
	)
	if err != nil {
		log.Fatalf("fatal: init tuya account store: %v", err)
	}
	tuyaAppClient := tuya.New(tuyaIoTClient, tuyaAccountStore)
	// 1. 3. 1. ADK Agent
	zeeAgent, err := zee.New(zee.Config{Model: agentModel, TuyaClient: tuyaAppClient, AdditionalInstruction: agent.Instruction()})
	if err != nil {
		log.Fatalf("fatal: build zee agent: %v", err)
	}
	// 1. 3. 2. ADK Runner
	const appName = "chat"
	zeeRunner, err := runner.New(runner.Config{
		AppName:           appName,
		Agent:             zeeAgent,
		SessionService:    agentSessSvc,
		MemoryService:     agentMemSvc,
		AutoCreateSession: true,
	})
	if err != nil {
		log.Fatalf("fatal: build zee runner: %v", err)
	}
	// 1. 3. 3. HTTP Handler
	zeeHandler := specialist.NewHandler(zeeRunner)
	// 1. 4. Ava
	// 1. 4. 0. Postera
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
	gcpRuntimeSAEmail := os.Getenv("GCP_RUNTIME_SA_EMAIL")
	if gcpRuntimeSAEmail == "" {
		log.Fatal("fatal: GCP_RUNTIME_SA_EMAIL is required")
	}
	hostURL := os.Getenv("HOST_URL")
	if hostURL == "" {
		log.Fatal("fatal: HOST_URL is required")
	}
	const avaAwakenEndpoint = "/ava/awaken"
	posteraEnqueuer, err := posteracloudtasks.NewEnqueuer(
		cloudTasksClient,
		gcpProjectID,
		cloudTasksLocationID,
		cloudTasksQueueID,
		posteracloudtasks.WithTargetURL(hostURL+avaAwakenEndpoint),
		posteracloudtasks.WithServiceAccountEmail(gcpRuntimeSAEmail),
		posteracloudtasks.WithHumanHeader("user-id"),
		posteracloudtasks.WithSessionHeader("session-id"),
		posteracloudtasks.WithMetadataHeader("timezone", "time-zone"),
	)
	if err != nil {
		log.Fatalf("fatal: init postera enqueuer: %v", err)
	}
	postarius, err := postera.New(
		posteraStore,
		posteraEnqueuer,
		postera.WithHumanFromContext(apiuser.ContextKey),
		postera.WithTimezoneFromContext(apitime.ContextKey),
		postera.WithSessionFromContext(apisession.ContextKey),
		postera.WithMetadataEntryFromContext("timezone", apitime.ContextKey),
	)
	if err != nil {
		log.Fatalf("fatal: init postarius: %v", err)
	}
	// 1. 4. 1. Sub Agent
	zeeAvaSubAgent := internalava.NewSubAgent(zeeAgent, zeeRunner)
	// 1. 4. 2. ADK Agent
	avaAgent, err := ava.New(ava.Config{
		Model:                 agentModel,
		Postarius:             postarius,
		SubAgents:             []ava.SubAgent{zeeAvaSubAgent},
		AdditionalInstruction: agent.Instruction(),
	})
	if err != nil {
		log.Fatalf("fatal: build ava agent: %v", err)
	}
	// 1. 4. 3. ADK Runner
	avaRunner, err := runner.New(runner.Config{
		AppName:           appName,
		Agent:             avaAgent,
		SessionService:    agentSessSvc,
		MemoryService:     agentMemSvc,
		AutoCreateSession: true,
	})
	if err != nil {
		log.Fatalf("fatal: build ava runner: %v", err)
	}
	// 1. 4. 4. HTTP Handler
	avaHandler := internalava.NewHandler(avaRunner)
	// 2. MEMORY
	// 2. 0. Service
	// 2. 0. 1. Session
	memorySessStore := internalzep.NewSessionStore(zepClient)
	memorySessSvc := memory.NewSessionService(memorySessStore)
	// 2. 0. 2. Knowledge
	memoryKnowledgeStore := internalzep.NewKnowledgeStore(zepClient)
	memoryKnowledgeSvc := memory.NewKnowledgeService(memoryKnowledgeStore)
	// 2. 0. 3. Postera
	// Postarius is made in ava wiring
	// 2. 1. Handler
	memoryHandler := memory.NewHandler(memorySessSvc, memoryKnowledgeSvc, postarius)
	// 3. HTTP
	// 3. 0. Index
	r := chi.NewRouter()
	r.Use(chiMiddleware.RequestID)
	r.Use(chiMiddleware.RealIP)
	r.Use(chiMiddleware.Logger)
	r.Use(chiMiddleware.Recoverer)
	svcEnv := os.Getenv("APP_ENV")
	if svcEnv == "" {
		log.Fatal("fatal: APP_ENV is required")
	}
	const svcName = appName + "-api"
	const svcVersion = "v0.0.1"
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		apihttp.WriteJSON(w, http.StatusOK, struct {
			Service     string `json:"service"`
			Version     string `json:"version"`
			Environment string `json:"environment"`
			Status      string `json:"status"`
		}{svcName, svcVersion, svcEnv, "UP"})
	})
	// 3. 1. Agents
	r.Group(func(r chi.Router) {
		r.Use(apiuser.HTTPWithID) // TEMP: replaces jwtAuthenticator.Authenticate
		r.Group(func(r chi.Router) {
			r.Use(apitime.HTTPWithZone)
			r.Use(apisession.HTTPWithID)
			// 3. 1. 0. Ava
			r.Post("/ava", avaHandler.HandleHuman)
			r.Post(avaAwakenEndpoint, avaHandler.HandleSelfAwaken)
			// 3. 1. 1. Zee
			r.Post("/zee", zeeHandler.HandleHuman)
		})
		// 3. 2. Memory
		// 3. 2. 0. Session
		r.Route("/sessions", func(r chi.Router) {
			r.Get("/{session-id}/messages", memoryHandler.GetMessages)
			r.Delete("/{session-id}/messages", memoryHandler.ClearMessages)
		})
		// 3. 2. 1. Knowledge
		r.Route("/knowledge", func(r chi.Router) {
			r.Get("/", memoryHandler.GetKnowledge)
			r.Delete("/", memoryHandler.DeleteKnowledge)
		})
		// 3. 2. 2. Postera
		r.Route("/postera", func(r chi.Router) {
			r.Get("/", memoryHandler.ListUpcoming)
			r.Delete("/{posterum-id}", memoryHandler.Cancel)
		})
	})
	// 3. Server
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
	log.Printf("Starting %s service [%s] on port %s", svcName, svcEnv, port)
	if err := s.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("fatal: failed to start server: %v", err)
	}
}
