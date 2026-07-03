package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	gcptasks "cloud.google.com/go/cloudtasks/apiv2"
	"cloud.google.com/go/firestore"
	firebase "firebase.google.com/go/v4"
	"github.com/avagenc/chat/internal/agent"
	internalava "github.com/avagenc/chat/internal/agent/ava"
	"github.com/avagenc/chat/internal/agent/specialist"
	"github.com/avagenc/chat/internal/identity"
	"github.com/avagenc/chat/internal/linking"
	"github.com/avagenc/chat/internal/memory"
	internalzep "github.com/avagenc/chat/internal/zep"
	zepclient "github.com/getzep/zep-go/v3/client"
	zepoption "github.com/getzep/zep-go/v3/option"
	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"go.avagenc.com/ava"
	"go.avagenc.com/rafal"
	"go.avagenc.com/zee"
	adkzep "go.naturallyfunny.dev/adk/zep"
	apihttp "go.naturallyfunny.dev/api/http"
	apisession "go.naturallyfunny.dev/api/session"
	apitime "go.naturallyfunny.dev/api/time"
	apiuser "go.naturallyfunny.dev/api/user"
	"go.naturallyfunny.dev/gworkspace"
	gworkspacefirestore "go.naturallyfunny.dev/gworkspace/firestore"
	"go.naturallyfunny.dev/postera"
	posteracloudtasks "go.naturallyfunny.dev/postera/cloudtasks"
	posterapostgres "go.naturallyfunny.dev/postera/postgres"
	"go.naturallyfunny.dev/tuya"
	"go.naturallyfunny.dev/tuya/cloud"
	tuyafirestore "go.naturallyfunny.dev/tuya/firestore"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
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
	gcpProjectID := os.Getenv("GCP_PROJECT_ID")
	if gcpProjectID == "" {
		log.Fatal("fatal: GCP_PROJECT_ID is required")
	}
	firestoreDatabaseID := os.Getenv("FIRESTORE_DATABASE_ID")
	if firestoreDatabaseID == "" {
		log.Fatal("fatal: FIRESTORE_DATABASE_ID is required")
	}
	// One Firestore client, shared by every store backed by it (tuya accounts,
	// gworkspace tokens).
	firestoreClient, err := firestore.NewClientWithDatabase(context.Background(), gcpProjectID, firestoreDatabaseID)
	if err != nil {
		log.Fatalf("fatal: init firestore client: %v", err)
	}
	defer firestoreClient.Close()
	tuyaAccountStore := tuyafirestore.NewAccountStore(firestoreClient, tuyafirestore.WithCollection("tuya_accounts"))
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
	// 1. 4. Rafal
	// 1. 4. 0. Google Workspace Client
	googleClientID := os.Getenv("GOOGLE_CLIENT_ID")
	if googleClientID == "" {
		log.Fatal("fatal: GOOGLE_CLIENT_ID is required")
	}
	googleClientSecret := os.Getenv("GOOGLE_CLIENT_SECRET")
	if googleClientSecret == "" {
		log.Fatal("fatal: GOOGLE_CLIENT_SECRET is required")
	}
	// Frontend callback page Google redirects the browser to after consent.
	// Must be registered verbatim as an authorized redirect URI on the OAuth
	// client in Google Cloud Console.
	googleOAuthRedirectURL := os.Getenv("GOOGLE_OAUTH_REDIRECT_URL")
	if googleOAuthRedirectURL == "" {
		log.Fatal("fatal: GOOGLE_OAUTH_REDIRECT_URL is required")
	}
	gworkspaceTokenStore := gworkspacefirestore.NewTokenStore(firestoreClient, gworkspacefirestore.WithCollection("gworkspace_tokens"))
	// One OAuth refresh token spans Calendar, Gmail, and Contacts — the client
	// must carry the union of all three scope sets (rafal checks this at build).
	gworkspaceScopes := make([]string, 0, len(gworkspace.CalendarRequiredScopes)+len(gworkspace.GmailRequiredScopes)+len(gworkspace.ContactsRequiredScopes))
	gworkspaceScopes = append(gworkspaceScopes, gworkspace.CalendarRequiredScopes...)
	gworkspaceScopes = append(gworkspaceScopes, gworkspace.GmailRequiredScopes...)
	gworkspaceScopes = append(gworkspaceScopes, gworkspace.ContactsRequiredScopes...)
	gworkspaceClient := gworkspace.NewClient(gworkspaceTokenStore, &oauth2.Config{
		ClientID:     googleClientID,
		ClientSecret: googleClientSecret,
		RedirectURL:  googleOAuthRedirectURL,
		Endpoint:     google.Endpoint,
		Scopes:       gworkspaceScopes,
	})
	// 1. 4. 1. ADK Agent
	rafalAgent, err := rafal.New(rafal.Config{Model: agentModel, WorkspaceClient: gworkspaceClient, AdditionalInstruction: agent.Instruction()})
	if err != nil {
		log.Fatalf("fatal: build rafal agent: %v", err)
	}
	// 1. 4. 2. ADK Runner
	rafalRunner, err := runner.New(runner.Config{
		AppName:           appName,
		Agent:             rafalAgent,
		SessionService:    agentSessSvc,
		MemoryService:     agentMemSvc,
		AutoCreateSession: true,
	})
	if err != nil {
		log.Fatalf("fatal: build rafal runner: %v", err)
	}
	// 1. 4. 3. HTTP Handler
	rafalHandler := specialist.NewHandler(rafalRunner)
	// 1. 5. Ava
	// 1. 5. 0. Postera
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
	// 1. 5. 1. Sub Agents
	zeeAvaSubAgent := internalava.NewSubAgent(zeeAgent, zeeRunner)
	rafalAvaSubAgent := internalava.NewSubAgent(rafalAgent, rafalRunner)
	// 1. 5. 2. ADK Agent
	avaAgent, err := ava.New(ava.Config{
		Model:                 agentModel,
		Postarius:             postarius,
		SubAgents:             []ava.SubAgent{zeeAvaSubAgent, rafalAvaSubAgent},
		AdditionalInstruction: agent.Instruction(),
	})
	if err != nil {
		log.Fatalf("fatal: build ava agent: %v", err)
	}
	// 1. 5. 3. ADK Runner
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
	// 1. 5. 4. HTTP Handler
	avaHandler := internalava.NewHandler(avaRunner)
	// 2. LINKING
	// 2. 0. Google Workspace — reuses the same gworkspace client rafal holds;
	// rafal only consumes the stored token, linking manages it.
	gworkspaceStateSecret := os.Getenv("GWORKSPACE_STATE_SECRET")
	if gworkspaceStateSecret == "" {
		log.Fatal("fatal: GWORKSPACE_STATE_SECRET is required")
	}
	gworkspaceLinkHandler := linking.NewGworkspaceHandler(gworkspaceClient, []byte(gworkspaceStateSecret))
	// 3. MEMORY
	// 3. 0. Service
	// 3. 0. 1. Session
	memorySessStore := internalzep.NewSessionStore(zepClient)
	memorySessSvc := memory.NewSessionService(memorySessStore)
	// 3. 0. 2. Knowledge
	memoryKnowledgeStore := internalzep.NewKnowledgeStore(zepClient)
	memoryKnowledgeSvc := memory.NewKnowledgeService(memoryKnowledgeStore)
	// 3. 0. 3. Postera
	// Postarius is made in ava wiring
	// 3. 1. Handler
	memoryHandler := memory.NewHandler(memorySessSvc, memoryKnowledgeSvc, postarius)
	// 4. HTTP
	// 4. 0. Index
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
	// 4. 1. Ava self-awaken — Cloud Tasks callback, not user traffic. Identity
	// comes from headers set by the postera enqueuer (user-id, session-id,
	// time-zone), not a Firebase ID token, so it stays outside the Firebase
	// auth group.
	r.Group(func(r chi.Router) {
		r.Use(apiuser.HTTPWithID)
		r.Use(apitime.HTTPWithZone)
		r.Use(apisession.HTTPWithID)
		r.Post(avaAwakenEndpoint, avaHandler.HandleSelfAwaken)
	})
	// 4. 2. Authenticated routes
	firebaseProjectID := os.Getenv("FIREBASE_PROJECT_ID")
	if firebaseProjectID == "" {
		log.Fatal("fatal: FIREBASE_PROJECT_ID is required")
	}
	firebaseApp, err := firebase.NewApp(context.Background(), &firebase.Config{ProjectID: firebaseProjectID})
	if err != nil {
		log.Fatalf("fatal: init firebase app: %v", err)
	}
	firebaseAuthClient, err := firebaseApp.Auth(context.Background())
	if err != nil {
		log.Fatalf("fatal: init firebase auth client: %v", err)
	}
	firebaseAuthenticator := identity.NewFirebaseAuthenticator(firebaseAuthClient)
	r.Group(func(r chi.Router) {
		r.Use(firebaseAuthenticator.Authenticate)
		r.Group(func(r chi.Router) {
			r.Use(apitime.HTTPWithZone)
			r.Use(apisession.HTTPWithID)
			// 4. 2. 0. Ava
			r.Post("/ava", avaHandler.HandleHuman)
			// 4. 2. 1. Zee
			r.Post("/zee", zeeHandler.HandleHuman)
			// 4. 2. 2. Rafal
			r.Post("/rafal", rafalHandler.HandleHuman)
		})
		// 4. 2. 3. Memory
		// 4. 2. 3. 0. Session
		r.Route("/sessions", func(r chi.Router) {
			r.Get("/{session-id}/messages", memoryHandler.GetMessages)
			r.Delete("/{session-id}/messages", memoryHandler.ClearMessages)
		})
		// 4. 2. 3. 1. Knowledge
		r.Route("/knowledge", func(r chi.Router) {
			r.Get("/", memoryHandler.GetKnowledge)
			r.Delete("/", memoryHandler.DeleteKnowledge)
		})
		// 4. 2. 3. 2. Postera
		r.Route("/postera", func(r chi.Router) {
			r.Get("/", memoryHandler.ListUpcoming)
			r.Delete("/{posterum-id}", memoryHandler.Cancel)
		})
		// 4. 2. 4. Linking
		// 4. 2. 4. 0. Google Workspace
		r.Route("/gworkspace", func(r chi.Router) {
			r.Get("/auth-url", gworkspaceLinkHandler.HandleAuthURL)
			r.Post("/connection", gworkspaceLinkHandler.HandleConnect)
			r.Delete("/connection", gworkspaceLinkHandler.HandleDisconnect)
		})
	})
	// 5. Server
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
