package main

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	gcptasks "cloud.google.com/go/cloudtasks/apiv2"
	"cloud.google.com/go/firestore"
	firebase "firebase.google.com/go/v4"
	"github.com/avagenc/chat/internal/agent"
	internalava "github.com/avagenc/chat/internal/agent/ava"
	"github.com/avagenc/chat/internal/agent/specialist"
	"github.com/avagenc/chat/internal/identity"
	"github.com/avagenc/chat/internal/knowledge"
	knowledgezep "github.com/avagenc/chat/internal/knowledge/zep"
	gworkspacelink "github.com/avagenc/chat/internal/link/gworkspace"
	spotifylink "github.com/avagenc/chat/internal/link/spotify"
	internalpostera "github.com/avagenc/chat/internal/postera"
	"github.com/avagenc/chat/internal/session"
	sessionzep "github.com/avagenc/chat/internal/session/zep"
	internalwallet "github.com/avagenc/chat/internal/wallet"
	walletpostgres "github.com/avagenc/chat/internal/wallet/postgres"
	zepclient "github.com/getzep/zep-go/v3/client"
	zepoption "github.com/getzep/zep-go/v3/option"
	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	spotifyauth "github.com/zmb3/spotify/v2/auth"
	"go.avagenc.com/ava"
	"go.avagenc.com/rafal"
	"go.avagenc.com/yori"
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
	"go.naturallyfunny.dev/spotify"
	spotifyfirestore "go.naturallyfunny.dev/spotify/firestore"
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
	// 1. 3. Billing — wallet ledger + biller shared by every agent run
	walletDBURL := os.Getenv("WALLET_DB_URL")
	if walletDBURL == "" {
		log.Fatal("fatal: WALLET_DB_URL is required")
	}
	walletDBPool, err := pgxpool.New(context.Background(), walletDBURL)
	if err != nil {
		log.Fatalf("fatal: init wallet db pool: %v", err)
	}
	defer walletDBPool.Close()
	// Schema is applied by goose in the deploy pipeline, not here — NewLedger
	// only validates the wallet_* tables exist.
	walletLedger, err := walletpostgres.NewLedger(context.Background(), walletDBPool)
	if err != nil {
		log.Fatalf("fatal: init wallet ledger: %v", err)
	}
	// Rates in rupiah per million tokens for gemini-3-flash-preview, Gemini
	// price card × USD/IDR × margin. Hardcoded, not env: the snapshot is
	// recorded on every ledger entry, and explicit wiring is the convention.
	// Update here when the model, FX, or margin moves.
	biller := internalwallet.NewBiller(walletLedger, internalwallet.Price{
		InputPerMTok:  10_000,
		CachedPerMTok: 2_500,
		OutputPerMTok: 42_000,
	})
	// 1. 4. Zee
	// 1. 4. 0. Tuya App Client
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
	// 1. 4. 1. ADK Agent
	zeeAgent, err := zee.New(zee.Config{Model: agentModel, TuyaClient: tuyaAppClient, AdditionalInstruction: agent.Instruction()})
	if err != nil {
		log.Fatalf("fatal: build zee agent: %v", err)
	}
	// 1. 4. 2. ADK Runner
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
	// 1. 4. 3. HTTP Handler
	zeeHandler := specialist.NewHandler(zeeRunner, biller, zeeAgent.Name())
	// 1. 5. Rafal
	// 1. 5. 0. Google Workspace Client
	googleClientID := os.Getenv("GOOGLE_CLIENT_ID")
	if googleClientID == "" {
		log.Fatal("fatal: GOOGLE_CLIENT_ID is required")
	}
	googleClientSecret := os.Getenv("GOOGLE_CLIENT_SECRET")
	if googleClientSecret == "" {
		log.Fatal("fatal: GOOGLE_CLIENT_SECRET is required")
	}
	// The web app origin the OAuth flow redirects back to after consent. Each
	// linking provider gets its own callback path, /link/callback/<integration>,
	// derived from this origin; every such URL must be registered verbatim as an
	// authorized redirect URI at that provider (Google Cloud Console, Spotify
	// Developer Dashboard).
	webAppURL := os.Getenv("WEB_APP_URL")
	if webAppURL == "" {
		log.Fatal("fatal: WEB_APP_URL is required")
	}
	gworkspaceRedirectURL, err := url.JoinPath(webAppURL, "link", "callback", "gworkspace")
	if err != nil {
		log.Fatalf("fatal: build gworkspace redirect URL: %v", err)
	}
	spotifyRedirectURL, err := url.JoinPath(webAppURL, "link", "callback", "spotify")
	if err != nil {
		log.Fatalf("fatal: build spotify redirect URL: %v", err)
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
		RedirectURL:  gworkspaceRedirectURL,
		Endpoint:     google.Endpoint,
		Scopes:       gworkspaceScopes,
	})
	// 1. 5. 1. ADK Agent
	rafalAgent, err := rafal.New(rafal.Config{Model: agentModel, WorkspaceClient: gworkspaceClient, AdditionalInstruction: agent.Instruction()})
	if err != nil {
		log.Fatalf("fatal: build rafal agent: %v", err)
	}
	// 1. 5. 2. ADK Runner
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
	// 1. 5. 3. HTTP Handler
	rafalHandler := specialist.NewHandler(rafalRunner, biller, rafalAgent.Name())
	// 1. 6. Yori
	// 1. 6. 0. Spotify Client
	spotifyClientID := os.Getenv("SPOTIFY_CLIENT_ID")
	if spotifyClientID == "" {
		log.Fatal("fatal: SPOTIFY_CLIENT_ID is required")
	}
	spotifyClientSecret := os.Getenv("SPOTIFY_CLIENT_SECRET")
	if spotifyClientSecret == "" {
		log.Fatal("fatal: SPOTIFY_CLIENT_SECRET is required")
	}
	spotifyTokenStore := spotifyfirestore.New(firestoreClient, spotifyfirestore.WithCollection("spotify_tokens"))
	spotifyClient := spotify.New(spotifyTokenStore, spotifyauth.New(
		spotifyauth.WithClientID(spotifyClientID),
		spotifyauth.WithClientSecret(spotifyClientSecret),
		spotifyauth.WithRedirectURL(spotifyRedirectURL),
		spotifyauth.WithScopes(spotify.RequiredScopes...),
	))
	// 1. 6. 1. ADK Agent
	yoriAgent, err := yori.New(yori.Config{Model: agentModel, SpotifyClient: spotifyClient, AdditionalInstruction: agent.Instruction()})
	if err != nil {
		log.Fatalf("fatal: build yori agent: %v", err)
	}
	// 1. 6. 2. ADK Runner
	yoriRunner, err := runner.New(runner.Config{
		AppName:           appName,
		Agent:             yoriAgent,
		SessionService:    agentSessSvc,
		MemoryService:     agentMemSvc,
		AutoCreateSession: true,
	})
	if err != nil {
		log.Fatalf("fatal: build yori runner: %v", err)
	}
	// 1. 6. 3. HTTP Handler
	yoriHandler := specialist.NewHandler(yoriRunner, biller, yoriAgent.Name())
	// 1. 7. Ava
	// 1. 7. 0. Postera
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
	// 1. 7. 1. Sub Agents
	zeeAvaSubAgent := internalava.NewSubAgent(zeeAgent, zeeRunner, biller)
	rafalAvaSubAgent := internalava.NewSubAgent(rafalAgent, rafalRunner, biller)
	yoriAvaSubAgent := internalava.NewSubAgent(yoriAgent, yoriRunner, biller)
	// 1. 7. 2. ADK Agent
	avaAgent, err := ava.New(ava.Config{
		Model:                 agentModel,
		Postarius:             postarius,
		SubAgents:             []ava.SubAgent{zeeAvaSubAgent, rafalAvaSubAgent, yoriAvaSubAgent},
		AdditionalInstruction: agent.Instruction(),
	})
	if err != nil {
		log.Fatalf("fatal: build ava agent: %v", err)
	}
	// 1. 7. 3. ADK Runner
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
	// 1. 7. 4. HTTP Handler
	avaHandler := internalava.NewHandler(avaRunner, biller)
	// 2. LINKING
	// One secret signs the OAuth state for every integration; each handler
	// domain-separates it by folding its integration name into the mac
	// (see link.SignState).
	oauthStateSecret := os.Getenv("OAUTH_STATE_SECRET")
	if oauthStateSecret == "" {
		log.Fatal("fatal: OAUTH_STATE_SECRET is required")
	}
	// 2. 0. Google Workspace — reuses the same gworkspace client rafal holds;
	// rafal only consumes the stored token, linking manages it.
	gworkspaceLinkHandler := gworkspacelink.NewHandler(gworkspaceClient, []byte(oauthStateSecret))
	// 2. 1. Spotify — reuses the same spotify client yori holds; yori only
	// consumes the stored token, linking manages it.
	spotifyLinkHandler := spotifylink.NewHandler(spotifyClient, []byte(oauthStateSecret))
	// 3. MEMORY — one package per memory type
	// 3. 0. Session (episodic)
	sessionStore := sessionzep.NewStore(zepClient)
	sessionService := session.NewService(sessionStore)
	sessionHandler := session.NewHandler(sessionService)
	// 3. 1. Knowledge (semantic)
	knowledgeStore := knowledgezep.NewStore(zepClient)
	knowledgeService := knowledge.NewService(knowledgeStore)
	knowledgeHandler := knowledge.NewHandler(knowledgeService)
	// 3. 2. Postera (prospective) — Postarius is made in ava wiring
	posteraHandler := internalpostera.NewHandler(postarius)
	// 4. WALLET
	walletGuard := internalwallet.NewGuard(walletLedger)
	walletHandler := internalwallet.NewHandler(walletLedger)
	// 5. HTTP
	// 5. 0. Index
	r := chi.NewRouter()
	r.Use(chiMiddleware.RequestID)
	r.Use(chiMiddleware.RealIP)
	r.Use(chiMiddleware.Logger)
	r.Use(chiMiddleware.Recoverer)
	// The browser SPA is served from a different origin than this API, so every
	// request carrying an Authorization or custom header is preceded by a CORS
	// preflight. Handle it before the auth groups so an unauthenticated OPTIONS
	// never reaches a protected route. Bearer tokens, not cookies, carry
	// identity — so credentials stay off.
	corsAllowedOrigins := os.Getenv("CORS_ALLOWED_ORIGINS")
	if corsAllowedOrigins == "" {
		log.Fatal("fatal: CORS_ALLOWED_ORIGINS is required")
	}
	allowedOrigins := strings.Split(corsAllowedOrigins, ",")
	for i := range allowedOrigins {
		allowedOrigins[i] = strings.TrimSpace(allowedOrigins[i])
	}
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   allowedOrigins,
		AllowedMethods:   []string{http.MethodGet, http.MethodPost, http.MethodDelete, http.MethodOptions},
		AllowedHeaders:   []string{"Authorization", "Content-Type", "session-id", "time-zone"},
		AllowCredentials: false,
		MaxAge:           300,
	}))
	svcEnv := os.Getenv("APP_ENV")
	if svcEnv == "" {
		log.Fatal("fatal: APP_ENV is required")
	}
	const svcName = appName + "-http-server"
	const svcVersion = "v0.0.1"
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		apihttp.WriteJSON(w, http.StatusOK, struct {
			Service     string `json:"service"`
			Version     string `json:"version"`
			Environment string `json:"environment"`
			Status      string `json:"status"`
		}{svcName, svcVersion, svcEnv, "UP"})
	})
	// 5. 1. Ava self-awaken — Cloud Tasks callback, not user traffic. Identity
	// comes from headers set by the postera enqueuer (user-id, session-id,
	// time-zone), not a Firebase ID token, so it stays outside the Firebase
	// auth group. Balance-gated like every agent route: an empty wallet skips
	// the run (Cloud Tasks retries the 402 until its retry policy exhausts).
	r.Group(func(r chi.Router) {
		r.Use(apiuser.HTTPWithID)
		r.Use(walletGuard.RequireBalance)
		r.Use(apitime.HTTPWithZone)
		r.Use(apisession.HTTPWithID)
		r.Post(avaAwakenEndpoint, avaHandler.HandleSelfAwaken)
	})
	// 5. 2. Authenticated routes
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
			r.Use(walletGuard.RequireBalance)
			r.Use(apitime.HTTPWithZone)
			r.Use(apisession.HTTPWithID)
			// 5. 2. 0. Ava
			r.Post("/ava", avaHandler.HandleHuman)
			// 5. 2. 1. Zee
			r.Post("/zee", zeeHandler.HandleHuman)
			// 5. 2. 2. Rafal
			r.Post("/rafal", rafalHandler.HandleHuman)
			// 5. 2. 3. Yori
			r.Post("/yori", yoriHandler.HandleHuman)
		})
		// 5. 2. 4. Memory
		// 5. 2. 4. 0. Session
		r.Route("/sessions", func(r chi.Router) {
			r.Get("/{session-id}/messages", sessionHandler.HandleGetMessages)
			r.Delete("/{session-id}/messages", sessionHandler.HandleClearMessages)
		})
		// 5. 2. 4. 1. Knowledge
		r.Route("/knowledge", func(r chi.Router) {
			r.Get("/", knowledgeHandler.HandleGet)
			r.Delete("/", knowledgeHandler.HandleDelete)
		})
		// 5. 2. 4. 2. Postera
		r.Route("/postera", func(r chi.Router) {
			r.Get("/", posteraHandler.HandleListUpcoming)
			r.Delete("/{posterum-id}", posteraHandler.HandleCancel)
		})
		// 5. 2. 5. Linking
		// 5. 2. 5. 0. Google Workspace
		r.Route("/gworkspace", func(r chi.Router) {
			r.Get("/auth-url", gworkspaceLinkHandler.HandleAuthURL)
			r.Get("/connection", gworkspaceLinkHandler.HandleStatus)
			r.Post("/connection", gworkspaceLinkHandler.HandleConnect)
			r.Delete("/connection", gworkspaceLinkHandler.HandleDisconnect)
		})
		// 5. 2. 5. 1. Spotify
		r.Route("/spotify", func(r chi.Router) {
			r.Get("/auth-url", spotifyLinkHandler.HandleAuthURL)
			r.Get("/connection", spotifyLinkHandler.HandleStatus)
			r.Post("/connection", spotifyLinkHandler.HandleConnect)
			r.Delete("/connection", spotifyLinkHandler.HandleDisconnect)
		})
		// 5. 2. 6. Wallet — not behind the balance guard: an empty wallet
		// must still be able to see its balance.
		r.Route("/wallet", func(r chi.Router) {
			r.Get("/", walletHandler.HandleBalance)
			r.With(apitime.HTTPWithZone).Get("/usage/today", walletHandler.HandleTodayUsage)
		})
	})
	// 6. Server
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
	log.Printf("Starting %s HTTP server [%s] on port %s", svcName, svcEnv, port)
	if err := s.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("fatal: failed to start server: %v", err)
	}
}
