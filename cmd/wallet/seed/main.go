package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/avagenc/chat/internal/wallet"
	"github.com/avagenc/chat/internal/wallet/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

func main() {
	// Attempt to load .env file for local development (ignore error if file doesn't exist)
	_ = godotenv.Load()

	var (
		userIDFlag    string
		amountIDRFlag float64
	)

	flag.StringVar(&userIDFlag, "user", "", "The Firebase User ID (required)")
	flag.Float64Var(&amountIDRFlag, "amount", 50000, "The seed amount in Rupiah (IDR)")
	flag.Parse()

	if userIDFlag == "" {
		fmt.Fprintf(os.Stderr, "Usage: go run cmd/wallet/seed/main.go -user <userID> [options]\n\nOptions:\n")
		flag.PrintDefaults()
		os.Exit(1)
	}

	if amountIDRFlag <= 0 {
		log.Fatalf("Error: Amount must be greater than 0")
	}

	dbURL := os.Getenv("WALLET_DB_URL")
	if dbURL == "" {
		log.Fatalf("Error: WALLET_DB_URL environment variable is not set. Please set it or create a .env file.")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, dbURL, userIDFlag, amountIDRFlag); err != nil {
		log.Fatalf("Seeding failed: %v", err)
	}
}

func run(ctx context.Context, dbURL string, userID string, amountIDR float64) error {
	// Initialize pgx connection pool
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	defer pool.Close()

	// Initialize ledger using postgres adapter
	ledger, err := postgres.NewLedger(ctx, pool)
	if err != nil {
		return fmt.Errorf("failed to init wallet ledger: %w", err)
	}

	// Double-entry ledger: amounts are int64 micro-rupiah (1 IDR = 1_000_000 micros)
	micros := int64(amountIDR * 1_000_000)
	if micros <= 0 {
		return fmt.Errorf("amount is too small after converting to micro-rupiah")
	}

	userAccount := wallet.UserAccountID(userID)

	// Check current balance
	balBefore, err := ledger.Balance(ctx, userAccount)
	if err != nil {
		return fmt.Errorf("failed to get current balance for %s: %w", userAccount, err)
	}

	// Prepare metadata for transaction
	metadataMap := map[string]any{
		"source":        "seed_script",
		"seeded_at":     time.Now().UTC().Format(time.RFC3339),
		"amount_idr":    amountIDR,
		"amount_micros": micros,
	}
	metadataBytes, err := json.Marshal(metadataMap)
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	// Generate a unique idempotency reference
	ref := fmt.Sprintf("seed-%s-%d", userID, time.Now().UnixNano())

	// Transact: Credit user account (positive amount), Debit pending (negative amount)
	log.Printf("Seeding user %s with %.2f IDR (%d micro-rupiah)...", userID, amountIDR, micros)
	txn, err := ledger.Transact(ctx, wallet.Spec{
		Kind:     "topup",
		Ref:      ref,
		Metadata: json.RawMessage(metadataBytes),
		Postings: []wallet.Posting{
			{AccountID: userAccount, Amount: micros},
			{AccountID: "pending", Amount: -micros},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to record seed transaction: %w", err)
	}

	// Check balance after transaction
	balAfter, err := ledger.Balance(ctx, userAccount)
	if err != nil {
		return fmt.Errorf("failed to get balance after seeding: %w", err)
	}

	log.Printf("Seeding transaction recorded successfully: txn ID %s", txn.ID)
	log.Printf("Balance before : %.2f IDR (%d micro-rupiah)", float64(balBefore)/1_000_000, balBefore)
	log.Printf("Balance after  : %.2f IDR (%d micro-rupiah)", float64(balAfter)/1_000_000, balAfter)

	return nil
}
