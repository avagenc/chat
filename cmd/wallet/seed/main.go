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
	if err := godotenv.Load(); err != nil {
		log.Println("info: .env file not found, using system environment variables")
	}
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
		log.Fatal("fatal: amount must be greater than 0")
	}
	walletDBURL := os.Getenv("WALLET_DB_URL")
	if walletDBURL == "" {
		log.Fatal("fatal: WALLET_DB_URL is required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	walletDBPool, err := pgxpool.New(ctx, walletDBURL)
	if err != nil {
		log.Fatalf("fatal: init wallet db pool: %v", err)
	}
	defer walletDBPool.Close()
	walletLedger, err := postgres.NewLedger(ctx, walletDBPool)
	if err != nil {
		log.Fatalf("fatal: init wallet ledger: %v", err)
	}
	micros := int64(amountIDRFlag * 1_000_000)
	if micros <= 0 {
		log.Fatal("fatal: amount is too small after converting to micro-rupiah")
	}
	userAccount := wallet.UserAccountID(userIDFlag)
	balBefore, err := walletLedger.Balance(ctx, userAccount)
	if err != nil {
		log.Fatalf("fatal: get current balance for %s: %v", userAccount, err)
	}
	metadataBytes, err := json.Marshal(map[string]any{
		"source":        "seed_script",
		"seeded_at":     time.Now().UTC().Format(time.RFC3339),
		"amount_idr":    amountIDRFlag,
		"amount_micros": micros,
	})
	if err != nil {
		log.Fatalf("fatal: marshal metadata: %v", err)
	}
	ref := fmt.Sprintf("seed-%s-%d", userIDFlag, time.Now().UnixNano())
	log.Printf("Seeding user %s with %.2f IDR (%d micro-rupiah)...", userIDFlag, amountIDRFlag, micros)
	txn, err := walletLedger.Transact(ctx, wallet.Spec{
		Kind:     "topup",
		Ref:      ref,
		Metadata: json.RawMessage(metadataBytes),
		Postings: []wallet.Posting{
			{AccountID: userAccount, Amount: micros},
			{AccountID: "pending", Amount: -micros},
		},
	})
	if err != nil {
		log.Fatalf("fatal: record seed transaction: %v", err)
	}
	balAfter, err := walletLedger.Balance(ctx, userAccount)
	if err != nil {
		log.Fatalf("fatal: get balance after seeding: %v", err)
	}
	log.Printf("Seeding transaction recorded successfully: txn ID %s", txn.ID)
	log.Printf("Balance before : %.2f IDR (%d micro-rupiah)", float64(balBefore)/1_000_000, balBefore)
	log.Printf("Balance after  : %.2f IDR (%d micro-rupiah)", float64(balAfter)/1_000_000, balAfter)
}
