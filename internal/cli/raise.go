package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/LightAwesome/portcullis/internal/config"
	"github.com/LightAwesome/portcullis/internal/server"
	"github.com/LightAwesome/portcullis/internal/store"
)

// raiseCmd returns the `portcullis raise` command — start the gateway server.
//
// "raise" is the themed verb for "raise the portcullis." Equivalent to
// `serve` or `start` in less-themed projects.
func raiseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "raise",
		Short: "Start the gateway server",
		Long: `Start the gateway server.

Reads configuration from environment variables (and a .env file in the
current directory if present). Connects to Postgres and Redis at
startup; misconfiguration fails immediately rather than at first request.

The server runs until interrupted (Ctrl-C, SIGTERM). Graceful shutdown
drains in-flight requests and the log queue before exiting.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServer(cmd.Context())
		},
	}
}

// runServer is the actual server-startup procedure, separated from the Cobra
// command for testability. Tests can call runServer with a context that
// cancels after a short delay and verify clean shutdown.
func runServer(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	db, err := store.New(ctx, cfg.DatabaseURL, cfg.RedisURL)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer db.Close()
	deps := &server.Dependencies{Config: cfg, Store: db}
	// TODO: construct the HTTP handler.
	// TODO: start the HTTP server, wait for ctx.Done().

	handler := server.NewServer(deps)

	fmt.Printf("portcullis: the gate is being raised (%s)\n", cfg.Env)
	fmt.Printf("  addr:      %s\n", cfg.Addr)
	fmt.Printf("  log_level: %s\n", cfg.LogLevel)

	_ = handler
	// For now, just block on the context so Ctrl-C does something.
	<-ctx.Done()
	fmt.Println("portcullis: the gate falls")
	return nil
}
