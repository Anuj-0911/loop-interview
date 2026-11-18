package main

import (
	"context"
	"flag"
	"log"
	"time"

	"loop-interview/backend/internal/storage"
)

func main() {
	path := flag.String("db", "booking.sqlite", "SQLite database file")
	seed := flag.Bool("seed", false, "Create demo users, a room, and slots")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := storage.Open(ctx, *path)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	if err := storage.Migrate(ctx, db); err != nil {
		log.Fatal(err)
	}
	if *seed {
		if err := storage.New(db).SeedDemo(ctx); err != nil {
			log.Fatal(err)
		}
	}
	log.Printf("SQLite database ready: %s", *path)
}
