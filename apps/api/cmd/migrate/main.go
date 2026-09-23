package main

import (
	"errors"
	"flag"
	"log"
	"os"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/shopkeet/api/migrations"
)

func main() {
	dir := flag.String("dir", "up", "migration direction: up or down")
	steps := flag.Int("steps", 0, "number of migrations to apply (0 = all)")
	flag.Parse()

	url := os.Getenv("DATABASE_URL")
	if url == "" {
		log.Fatal("DATABASE_URL is required")
	}

	src, err := iofs.New(migrations.Files, ".")
	if err != nil {
		log.Fatalf("failed to load embedded migrations: %v", err)
	}

	m, err := migrate.NewWithSourceInstance("iofs", src, url)
	if err != nil {
		log.Fatalf("failed to initialize migrate: %v", err)
	}
	defer m.Close()

	switch *dir {
	case "up":
		if *steps > 0 {
			err = m.Steps(*steps)
		} else {
			err = m.Up()
		}
	case "down":
		if *steps > 0 {
			err = m.Steps(-*steps)
		} else {
			err = m.Down()
		}
	default:
		log.Fatalf("unknown direction %q (expected up or down)", *dir)
	}

	if err != nil && !errors.Is(err, migrate.ErrNoChange) {
		log.Fatalf("migration failed: %v", err)
	}

	log.Printf("migrations %s: done", *dir)
}