package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"loop-interview/backend/internal/controller"
	"loop-interview/backend/internal/service"
	"loop-interview/backend/internal/storage"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	path := flag.String("db", "booking.sqlite", "SQLite database file")
	address := flag.String("addr", "127.0.0.1:8080", "HTTP listen address")
	seed := flag.Bool("seed", false, "Seed demo users, room, and slots")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	db, err := storage.Open(startup, *path)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := storage.Migrate(startup, db); err != nil {
		return err
	}
	store := storage.New(db)
	if *seed {
		if err := store.SeedDemo(startup); err != nil {
			return err
		}
	}
	cancel()
	server := &http.Server{Addr: *address, Handler: controller.New(service.New(store)), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	stopped := make(chan error, 1)
	go func() { stopped <- server.ListenAndServe() }()
	log.Printf("Booking API listening at http://%s", *address)
	select {
	case err := <-stopped:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			server.Close()
			return err
		}
		return nil
	}
}
