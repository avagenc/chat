package main

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"

	_ "time/tzdata"

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
	tuyalink "github.com/avagenc/chat/internal/link/tuya"
	internalpostera "github.com/avagenc/chat/internal/postera"
	"github.com/avagenc/chat/internal/session"
	sessionzep "github.com/avagenc/chat/internal/session/zep"
	internalwallet "github.com/avagenc/chat/internal/wallet"

	// walletmidtrans "github.com/avagenc/chat/internal/wallet/midtrans"
	walletpostgres "github.com/avagenc/chat/internal/wallet/postgres"
	zepclient "github.com/getzep/zep-go/v3/client"
	zepoption "github.com/getzep/zep-go/v3/option"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	spotifyauth "github.com/zmb3/spotify/v2/auth"
	"go.avagenc.com/ava"
	"go.avagenc.com/rafal"
	"go.avagenc.com/yori"
	"go.avagenc.com/zee"
	adkzep "go.naturallyfunny.dev/adk/zep"
	apihttp "go.naturallyfunny.dev/api/http"
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
	// ---- DEPENDENCY INJECTIONS----
	// 1. Info
	if err := godotenv.Load(); err != nil {
		log.Println("info: .env file not found, using system environment variables")
	}
	svcEnv := os.Getenv("APP_ENV")
	if svcEnv == "" {
		log.Fatal("fatal: APP_ENV is required")
	}
	const appName = "chat"
	const svcVersion = "v0.0.1"
	const svcName = appName + "-http-server"
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		apihttp.WriteJSON(w, http.StatusOK, struct {
			Service     string `json:"service"`
			Version     string `json:"version"`
			Environment string `json:"environment"`
			Status      string `json:"status"`
		}{svcName, svcVersion, svcEnv, "UP"})
	})
	// 2. Wallet
	walletDBURL := os.Getenv("WALLET_DB_URL")
	if walletDBURL == "" {
		log.Fatal("fatal: WALLET_DB_URL is required")
	}
	walletDBPool, err := pgxpool.New(context.Background(), walletDBURL)
	if err != nil {
		log.Fatalf("fatal: init wallet db pool: %v", err)
	}
	defer walletDBPool.Close()
	walletLedger, err := walletpostgres.NewLedger(context.Background(), walletDBPool)
	if err != nil {
		log.Fatalf("fatal: init wallet ledger: %v", err)
	}
	walletHandler := internalwallet.NewHandler(walletLedger)
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
	mux.Handle(
		"GET /wallet",
		firebaseAuthenticator.Authenticate(
			http.HandlerFunc(walletHandler.HandleBalance),
		),
	)
	mux.Handle(
		"GET /wallet/usage/today",
		firebaseAuthenticator.Authenticate(apitime.HTTPWithZone(
			http.HandlerFunc(walletHandler.HandleTodayUsage),
		)),
	)
	// midtransServerKey := os.Getenv("MIDTRANS_SERVER_KEY")
	// if midtransServerKey == "" {
	// 	log.Fatal("fatal: MIDTRANS_SERVER_KEY is required")
	// }
	// midtransBaseURL := os.Getenv("MIDTRANS_BASE_URL")
	// if midtransBaseURL == "" {
	// 	log.Fatal("fatal: MIDTRANS_BASE_URL is required")
	// }
	// midtransHandler := walletmidtrans.NewHandler(walletLedger, &http.Client{Timeout: 30 * time.Second}, midtransServerKey, midtransBaseURL)
	// mux.Handle("POST /wallet/topup",
	// 	firebaseAuthenticator.Authenticate(
	// 		http.HandlerFunc(midtransHandler.HandleCreateTopup),
	// 	),
	// )
	// mux.Handle("POST /wallet/topup/notification",
	// 	http.HandlerFunc(midtransHandler.HandleNotification),
	// )
	// 3. Zee
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
	agentMemSvc := adkzep.NewMemoryService(zepClient)
	geminiAPIKey := os.Getenv("GEMINI_API_KEY")
	if geminiAPIKey == "" {
		log.Fatal("fatal: GEMINI_API_KEY is required")
	}
	modelCallTimeout := 90 * time.Second
	agentModel, err := gemini.NewModel(context.Background(), "gemini-3.5-flash", &genai.ClientConfig{
		APIKey:      geminiAPIKey,
		HTTPOptions: genai.HTTPOptions{Timeout: &modelCallTimeout},
	})
	if err != nil {
		log.Fatalf("fatal: build gemini model: %v", err)
	}
	firestoreDatabaseID := os.Getenv("FIRESTORE_DATABASE_ID")
	if firestoreDatabaseID == "" {
		log.Fatal("fatal: FIRESTORE_DATABASE_ID is required")
	}
	gcpProjectID := os.Getenv("GCP_PROJECT_ID")
	if gcpProjectID == "" {
		log.Fatal("fatal: GCP_PROJECT_ID is required")
	}
	firestoreClient, err := firestore.NewClientWithDatabase(context.Background(), gcpProjectID, firestoreDatabaseID)
	if err != nil {
		log.Fatalf("fatal: init firestore client: %v", err)
	}
	defer firestoreClient.Close()
	tuyaAccountStore := tuyafirestore.NewAccountStore(firestoreClient, tuyafirestore.WithCollection("tuya_accounts"))
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
	tuyaAppClient := tuya.New(tuyaIoTClient, tuyaAccountStore)
	zeeAgent, err := zee.New(zee.Config{Model: agentModel, TuyaClient: tuyaAppClient, AdditionalInstruction: specialist.Instruction()})
	if err != nil {
		log.Fatalf("fatal: build zee agent: %v", err)
	}
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
	biller := internalwallet.NewBiller(walletLedger, internalwallet.Price{
		InputPerMTok:  10_000,
		CachedPerMTok: 2_500,
		OutputPerMTok: 42_000,
	})
	zeeHandler := specialist.NewHandler(zeeRunner, biller, zeeAgent.Name())
	walletGuard := internalwallet.NewGuard(walletLedger)
	mux.Handle(
		"POST /zee",
		firebaseAuthenticator.Authenticate(walletGuard.RequireBalance(apitime.HTTPWithZone(
			http.HandlerFunc(zeeHandler.HandleHuman),
		))),
	)
	// 4. Rafal
	googleClientID := os.Getenv("GOOGLE_CLIENT_ID")
	if googleClientID == "" {
		log.Fatal("fatal: GOOGLE_CLIENT_ID is required")
	}
	googleClientSecret := os.Getenv("GOOGLE_CLIENT_SECRET")
	if googleClientSecret == "" {
		log.Fatal("fatal: GOOGLE_CLIENT_SECRET is required")
	}
	webAppURL := os.Getenv("WEB_APP_URL")
	if webAppURL == "" {
		log.Fatal("fatal: WEB_APP_URL is required")
	}
	gworkspaceRedirectURL, err := url.JoinPath(webAppURL, "gworkspace", "link", "callback")
	if err != nil {
		log.Fatalf("fatal: build gworkspace redirect URL: %v", err)
	}
	spotifyRedirectURL, err := url.JoinPath(webAppURL, "spotify", "link", "callback")
	if err != nil {
		log.Fatalf("fatal: build spotify redirect URL: %v", err)
	}
	gworkspaceTokenStore := gworkspacefirestore.NewTokenStore(firestoreClient, gworkspacefirestore.WithCollection("gworkspace_tokens"))
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
	rafalAgent, err := rafal.New(rafal.Config{Model: agentModel, WorkspaceClient: gworkspaceClient, AdditionalInstruction: specialist.Instruction()})
	if err != nil {
		log.Fatalf("fatal: build rafal agent: %v", err)
	}
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
	rafalHandler := specialist.NewHandler(rafalRunner, biller, rafalAgent.Name())
	mux.Handle(
		"POST /rafal",
		firebaseAuthenticator.Authenticate(walletGuard.RequireBalance(apitime.HTTPWithZone(
			http.HandlerFunc(rafalHandler.HandleHuman),
		))),
	)
	// 5. Yori
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
	yoriAgent, err := yori.New(yori.Config{Model: agentModel, SpotifyClient: spotifyClient, AdditionalInstruction: specialist.Instruction()})
	if err != nil {
		log.Fatalf("fatal: build yori agent: %v", err)
	}
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
	yoriHandler := specialist.NewHandler(yoriRunner, biller, yoriAgent.Name())
	mux.Handle(
		"POST /yori",
		firebaseAuthenticator.Authenticate(walletGuard.RequireBalance(apitime.HTTPWithZone(
			http.HandlerFunc(yoriHandler.HandleHuman),
		))),
	)
	// 6. Ava
	zeeAvaSubAgent := internalava.NewSubAgent(zeeAgent, zeeRunner, biller, specialist.RanByAvaInstruction)
	rafalAvaSubAgent := internalava.NewSubAgent(rafalAgent, rafalRunner, biller, specialist.RanByAvaInstruction)
	yoriAvaSubAgent := internalava.NewSubAgent(yoriAgent, yoriRunner, biller, specialist.RanByAvaInstruction)
	gcpRuntimeSAEmail := os.Getenv("GCP_RUNTIME_SA_EMAIL")
	if gcpRuntimeSAEmail == "" {
		log.Fatal("fatal: GCP_RUNTIME_SA_EMAIL is required")
	}
	hostURL := os.Getenv("HOST_URL")
	if hostURL == "" {
		log.Fatal("fatal: HOST_URL is required")
	}
	const avaAwakenEndpoint = "/ava/awaken"
	cloudTasksQueueID := os.Getenv("CLOUD_TASKS_QUEUE_ID")
	if cloudTasksQueueID == "" {
		log.Fatal("fatal: CLOUD_TASKS_QUEUE_ID is required")
	}
	cloudTasksLocationID := os.Getenv("CLOUD_TASKS_LOCATION_ID")
	if cloudTasksLocationID == "" {
		log.Fatal("fatal: CLOUD_TASKS_LOCATION_ID is required")
	}
	cloudTasksClient, err := gcptasks.NewClient(context.Background())
	if err != nil {
		log.Fatalf("fatal: init cloud tasks client: %v", err)
	}
	defer cloudTasksClient.Close()
	posteraAPIKey := os.Getenv("POSTERA_API_KEY")
	if posteraAPIKey == "" {
		log.Fatal("fatal: POSTERA_API_KEY is required")
	}
	posteraEnqueuer, err := posteracloudtasks.NewEnqueuer(
		cloudTasksClient,
		gcpProjectID,
		cloudTasksLocationID,
		cloudTasksQueueID,
		posteracloudtasks.WithTargetURL(hostURL+avaAwakenEndpoint),
		posteracloudtasks.WithServiceAccountEmail(gcpRuntimeSAEmail),
		posteracloudtasks.WithHumanHeader("user-id"),
		posteracloudtasks.WithMetadataHeader("timezone", "time-zone"),
		posteracloudtasks.WithFixedHeader("api-key", posteraAPIKey),
	)
	if err != nil {
		log.Fatalf("fatal: init postera enqueuer: %v", err)
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
	postarius, err := postera.New(
		posteraStore,
		posteraEnqueuer,
		postera.WithHumanFromContext(apiuser.ContextKey),
		postera.WithTimezoneFromContext(apitime.ContextKey),
		postera.WithMetadataEntryFromContext("timezone", apitime.ContextKey),
	)
	if err != nil {
		log.Fatalf("fatal: init postarius: %v", err)
	}
	avaAgent, err := ava.New(ava.Config{
		Model:                 agentModel,
		Postarius:             postarius,
		SubAgents:             []ava.SubAgent{zeeAvaSubAgent, rafalAvaSubAgent, yoriAvaSubAgent},
		AdditionalInstruction: internalava.Instruction(),
	})
	if err != nil {
		log.Fatalf("fatal: build ava agent: %v", err)
	}
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
	avaHandler := internalava.NewHandler(avaRunner, biller)
	mux.Handle(
		"POST /ava",
		firebaseAuthenticator.Authenticate(walletGuard.RequireBalance(apitime.HTTPWithZone(
			http.HandlerFunc(avaHandler.HandleHuman),
		))),
	)
	thirdPartyAPIKey := os.Getenv("THIRD_PARTY_API_KEY")
	if thirdPartyAPIKey == "" {
		log.Fatal("fatal: THIRD_PARTY_API_KEY is required")
	}
	thirdPartyAuthenticator := identity.NewAPIKeyAuthenticator(thirdPartyAPIKey)
	mux.Handle(
		"POST /ava/voice",
		thirdPartyAuthenticator.Authenticate(apiuser.HTTPWithID(walletGuard.RequireBalance(apitime.HTTPWithZone(
			http.HandlerFunc(avaHandler.HandleVoice),
		)))),
	)
	posteraAuthenticator := identity.NewAPIKeyAuthenticator(posteraAPIKey)
	mux.Handle(
		"POST "+avaAwakenEndpoint,
		posteraAuthenticator.Authenticate(apiuser.HTTPWithID(walletGuard.RequireBalance(apitime.HTTPWithZone(
			http.HandlerFunc(avaHandler.HandleSelfAwaken),
		)))),
	)
	// 7. Gworkspace
	oauthStateSecret := os.Getenv("OAUTH_STATE_SECRET")
	if oauthStateSecret == "" {
		log.Fatal("fatal: OAUTH_STATE_SECRET is required")
	}
	gworkspaceLinkHandler := gworkspacelink.NewHandler(gworkspaceClient, []byte(oauthStateSecret))
	mux.Handle(
		"GET /gworkspace/auth-url",
		firebaseAuthenticator.Authenticate(
			http.HandlerFunc(gworkspaceLinkHandler.HandleAuthURL),
		),
	)
	mux.Handle(
		"GET /gworkspace/connection",
		firebaseAuthenticator.Authenticate(
			http.HandlerFunc(gworkspaceLinkHandler.HandleStatus),
		),
	)
	mux.Handle(
		"POST /gworkspace/connection",
		firebaseAuthenticator.Authenticate(
			http.HandlerFunc(gworkspaceLinkHandler.HandleConnect),
		),
	)
	mux.Handle(
		"DELETE /gworkspace/connection",
		firebaseAuthenticator.Authenticate(
			http.HandlerFunc(gworkspaceLinkHandler.HandleDisconnect),
		),
	)
	// 8. Spotify
	spotifyLinkHandler := spotifylink.NewHandler(spotifyClient, []byte(oauthStateSecret))
	mux.Handle(
		"GET /spotify/auth-url",
		firebaseAuthenticator.Authenticate(
			http.HandlerFunc(spotifyLinkHandler.HandleAuthURL),
		),
	)
	mux.Handle(
		"GET /spotify/connection",
		firebaseAuthenticator.Authenticate(
			http.HandlerFunc(spotifyLinkHandler.HandleStatus),
		),
	)
	mux.Handle(
		"POST /spotify/connection",
		firebaseAuthenticator.Authenticate(
			http.HandlerFunc(spotifyLinkHandler.HandleConnect),
		),
	)
	mux.Handle(
		"DELETE /spotify/connection",
		firebaseAuthenticator.Authenticate(
			http.HandlerFunc(spotifyLinkHandler.HandleDisconnect),
		),
	)
	tuyaLinkHandler := tuyalink.NewHandler(tuyaAppClient)
	mux.Handle(
		"GET /tuya/connection",
		firebaseAuthenticator.Authenticate(
			http.HandlerFunc(tuyaLinkHandler.HandleStatus),
		),
	)
	// 9. Session (episodic)
	sessionStore := sessionzep.NewStore(zepClient)
	sessionService := session.NewService(sessionStore)
	sessionHandler := session.NewHandler(sessionService)
	mux.Handle(
		"GET /sessions/messages",
		firebaseAuthenticator.Authenticate(
			http.HandlerFunc(sessionHandler.HandleGetMessages),
		),
	)
	mux.Handle(
		"DELETE /sessions/messages",
		firebaseAuthenticator.Authenticate(
			http.HandlerFunc(sessionHandler.HandleClearMessages),
		),
	)
	// 10. Knowledge (semantic)
	knowledgeStore := knowledgezep.NewStore(zepClient)
	knowledgeService := knowledge.NewService(knowledgeStore)
	knowledgeHandler := knowledge.NewHandler(knowledgeService)
	mux.Handle(
		"GET /knowledge",
		firebaseAuthenticator.Authenticate(
			http.HandlerFunc(knowledgeHandler.HandleGet),
		),
	)
	mux.Handle(
		"DELETE /knowledge",
		firebaseAuthenticator.Authenticate(
			http.HandlerFunc(knowledgeHandler.HandleDelete),
		),
	)
	// 11. Postera (prospective)
	posteraHandler := internalpostera.NewHandler(postarius)
	mux.Handle(
		"GET /postera",
		firebaseAuthenticator.Authenticate(
			http.HandlerFunc(posteraHandler.HandleListUpcoming),
		),
	)
	mux.Handle(
		"DELETE /postera/{posterumID}",
		firebaseAuthenticator.Authenticate(
			http.HandlerFunc(posteraHandler.HandleCancel),
		),
	)
	// 12. Preflight
	mux.HandleFunc("OPTIONS /", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, session-id, time-zone")
		w.Header().Set("Access-Control-Max-Age", "300")
		w.WriteHeader(http.StatusNoContent)
	})
	// ---- START SERVER ----
	corsAllowedOrigin := os.Getenv("CORS_ALLOWED_ORIGIN")
	if corsAllowedOrigin == "" {
		log.Fatal("fatal: CORS_ALLOWED_ORIGIN is required")
	}
	cors := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if origin := r.Header.Get("Origin"); origin != "" {
				w.Header().Add("Vary", "Origin")
				if origin == corsAllowedOrigin {
					w.Header().Set("Access-Control-Allow-Origin", origin)
				}
			}
			next.ServeHTTP(w, r)
		})
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           cors(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      310 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	log.Printf("In the name of Allah, The Most Compassionate, The Most Merciful")
	log.Printf("Starting %s HTTP server [%s] on port %s", svcName, svcEnv, port)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("fatal: failed to start server: %v", err)
	}
}
