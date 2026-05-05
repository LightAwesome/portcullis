// Command portcullis is the API gateway server and CLI.
package main

import (
	"context"
	"fmt"
	"github.com/LightAwesome/portcullis/internal/config"
	"github.com/LightAwesome/portcullis/internal/store"
	"os"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	ctx := context.Background()

	db, err := store.New(ctx, cfg.DatabaseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect to database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := db.Ping(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "ping database: %v\n", err)
		os.Exit(1)
	}

	clients, err := db.ListClients(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "list clients: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("portcullis: connected to database (%s environment)\n", cfg.Env)
	fmt.Printf("  registered clients: %d\n", len(clients))
}
