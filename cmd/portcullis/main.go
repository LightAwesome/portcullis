// Command portcullis is the API gateway server and CLI.
package main

import (
	"context"
	"fmt"
	"github.com/LightAwesome/portcullis/internal/config"
	"github.com/LightAwesome/portcullis/internal/store"
	"os"
	// "time"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	ctx := context.Background()

	db, err := store.New(ctx, cfg.DatabaseURL, cfg.RedisURL)
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

	/* TESTING THE CACHE*/
	// if err := db.CacheSet(ctx, "test:smoke", []byte("hello"), 30*time.Second); err != nil {
	// 	fmt.Fprintf(os.Stderr, "cache set: %v\n", err)
	// 	os.Exit(1)
	// }
	// val, err := db.CacheGet(ctx, "test:smoke")
	// if err != nil {
	// 	fmt.Fprintf(os.Stderr, "cache get: %v\n", err)
	// 	os.Exit(1)
	// }
	// fmt.Printf("  cache smoke: got %q\n", val)
	//
	// _, err = db.CacheGet(ctx, "test:does-not-exist")
	// fmt.Printf("  cache miss: %v\n", err)

	fmt.Printf("portcullis: connected to database (%s environment)\n", cfg.Env)
	fmt.Printf("  registered clients: %d\n", len(clients))
}
