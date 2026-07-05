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
	// Attempt to load .env file for local development (ignore error if file doesn't exist)
	_ = godotenv.Load()

	// Parse custom flags if any, or just goose commands
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
		args = []string{"up"} // Default command
	}

	command := args[0]
	var cmdArgs []string
	if len(args) > 1 {
		cmdArgs = args[1:]
	}

	dbURL := os.Getenv("WALLET_DB_URL")
	if dbURL == "" {
		log.Fatalf("Error: WALLET_DB_URL environment variable is not set. Please set it or create a .env file.")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, dbURL, command, cmdArgs); err != nil {
		log.Fatalf("Migration failed: %v", err)
	}
}

func run(ctx context.Context, dbURL, command string, args []string) error {
	// Connect to database using pgx stdlib compatibility layer
	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer db.Close()

	// Ping database to ensure connection is healthy
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("failed to ping database: %w", err)
	}

	// Set dialect for goose
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("failed to set goose dialect: %w", err)
	}

	// Use embedded migrations
	goose.SetBaseFS(postgres.MigrationsFS)

	log.Printf("Executing database migration command: %s %v", command, args)

	// Run migration command using goose
	// The directory is "migrations" because goose.SetBaseFS sets the base fs,
	// and the SQL files in MigrationsFS are embedded with the prefix "migrations/"
	if err := goose.RunContext(ctx, command, db, "migrations", args...); err != nil {
		if errors.Is(err, context.Canceled) {
			return errors.New("migration canceled by user/system signal")
		}
		return err
	}

	log.Printf("Migration command %q completed successfully (args=%v)", command, args)
	return nil
}
