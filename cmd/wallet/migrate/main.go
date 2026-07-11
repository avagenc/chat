package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/avagenc/chat/internal/wallet/postgres"
	"github.com/joho/godotenv"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("info: .env file not found, using system environment variables")
	}
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: go run cmd/wallet/migrate/main.go [options] <command> [arguments]\n\n")
		fmt.Fprintf(os.Stderr, "Commands:\n")
		fmt.Fprintf(os.Stderr, "  up                   Migrate the database to the most recent version\n")
		fmt.Fprintf(os.Stderr, "  up-by-one            Migrate the database up by 1 version\n")
		fmt.Fprintf(os.Stderr, "  up-to VERSION        Migrate the database to a specific VERSION\n")
		fmt.Fprintf(os.Stderr, "  down                 Roll back the version by 1\n")
		fmt.Fprintf(os.Stderr, "  down-to VERSION      Roll back to a specific VERSION\n")
		fmt.Fprintf(os.Stderr, "  redo                 Re-run the latest migration\n")
		fmt.Fprintf(os.Stderr, "  reset                Roll back all migrations\n")
		fmt.Fprintf(os.Stderr, "  status               Dump the migration status for the current DB\n")
		fmt.Fprintf(os.Stderr, "  version              Print the current version of the database\n")
	}
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		args = []string{"up"}
	}
	command := args[0]
	var cmdArgs []string
	if len(args) > 1 {
		cmdArgs = args[1:]
	}
	walletDBURL := os.Getenv("WALLET_DB_URL")
	if walletDBURL == "" {
		log.Fatal("fatal: WALLET_DB_URL is required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	db, err := sql.Open("pgx", walletDBURL)
	if err != nil {
		log.Fatalf("fatal: open wallet db: %v", err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		log.Fatalf("fatal: ping wallet db: %v", err)
	}
	if err := goose.SetDialect("postgres"); err != nil {
		log.Fatalf("fatal: set goose dialect: %v", err)
	}
	goose.SetBaseFS(postgres.MigrationsFS)
	log.Printf("Executing database migration command: %s %v", command, cmdArgs)
	if err := goose.RunContext(ctx, command, db, "migrations", cmdArgs...); err != nil {
		if errors.Is(err, context.Canceled) {
			log.Fatal("fatal: migration canceled by user/system signal")
		}
		log.Fatalf("fatal: migration command %q failed: %v", command, err)
	}
	log.Printf("Migration command %q completed successfully (args=%v)", command, cmdArgs)
}
