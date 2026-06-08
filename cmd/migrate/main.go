package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"

	"github.com/pressly/goose/v3"
	"github.com/vpramatarov/micro-blog/internal/auth"
	"github.com/vpramatarov/micro-blog/internal/config"
	"github.com/vpramatarov/micro-blog/internal/migrate"

	_ "modernc.org/sqlite"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Migration Tool")
		fmt.Println("Usage: go run cmd/migrate/main.go <command>")
		fmt.Println("Commands: up, down, status, seed")
		os.Exit(1)
	}

	command := os.Args[1]

	cfg := config.Load()
	// Use cfg.DSN() so the CLI runs with the same WAL/foreign_keys/busy_timeout
	// pragmas as cmd/server — a destructive `down` now triggers ON DELETE
	// CASCADE, and BUSY_TIMEOUT avoids SQLITE_BUSY when the server is running.
	db, err := sql.Open("sqlite", cfg.DatabaseDSN())
	if err != nil {
		log.Fatal("Failed to connect to database:", err)
	}
	defer db.Close()

	if err := migrate.Setup(); err != nil {
		log.Fatalf("goose setup: %v", err)
	}

	switch command {
	case "seed":
		runSeedData(db, cfg.ADMIN_SEED_PASSWORD)
		return
	case "seed-demo":
		runSeedDemo(db, cfg, os.Args[2:])
		return
	}

	fmt.Printf("Running migration command: '%s'...\n", command)

	err = goose.RunContext(context.Background(), command, db, migrate.DIR)
	if err != nil {
		log.Fatalf("Goose execution failed: %v\n", err)
	}

	fmt.Println("Database operation complete!")
}

func runSeedData(db *sql.DB, adminPassword string) {
	if adminPassword == "" {
		log.Fatal("ADMIN_SEED_PASSWORD must be set to seed the admin user; refusing to fall back to a known default")
	}

	fmt.Println("Adding seed data...")

	hash, err := auth.Hash(adminPassword)
	if err != nil {
		log.Fatalf("Failed to hash admin password: %v", err)
	}

	_, err = db.Exec(`
		INSERT OR IGNORE INTO users (username, email, password_hash, role_id) 
		VALUES ('admin', 'admin@epic.com', ?, 1)
	`, hash)
	if err != nil {
		log.Printf("Failed to insert admin user: %v\n", err)
	}

	fmt.Println("Seeding complete!")
}
