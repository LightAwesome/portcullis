package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

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
	//TODO: actually listen to and serve requests

	srv := &http.Server{
		Addr:    cfg.Addr,
		Handler: handler,

		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		// ReadTimeout and WriteTimeout intentionally unset:
		// the gateway proxies streaming responses (e.g. LLM SSE) which can
		// legitimately take minutes. Per-handler timeouts via context are
		// the right granularity, not server-level limits.
	}

	// serverErr receives ListenAndServe's exit reason.
	// It's a buffered channel of size 1 so the goroutine can deliver its
	// error and exit even if no one is listening yet (e.g. immediate signal).
	serverErr := make(chan error, 1)
	go func() {
		fmt.Printf("portcullis: the gate is being raised (%s)\n", cfg.Env)
		fmt.Printf("  addr:      %s\n", cfg.Addr)
		fmt.Printf("  log_level: %s\n", cfg.LogLevel)
		// ListenAndServe always returns a non-nil error. The expected one
		// after Shutdown is http.ErrServerClosed; anything else is a real
		// problem.
		serverErr <- srv.ListenAndServe()
	}()

	// Wait for either:
	//   1. The server to fail (rare — usually port already in use)
	//   2. The context to cancel (Ctrl-C, SIGTERM)

	select {
	case err := <-serverErr:
		return fmt.Errorf("server failed: %w", err)
	case <-ctx.Done():
		fmt.Println("portcullis: the gate falls")
	}

	// Graceful shutdown. Use a fresh context with a deadline; we can't reuse
	// ctx because it's already cancelled — Shutdown would return immediately
	// without giving in-flight requests time to drain.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		// Shutdown returned before all connections drained — either the
		// 30-second deadline elapsed, or shutdown encountered an error.
		// Either way, fall through to Close to force-terminate the rest.
		fmt.Fprintf(os.Stderr, "shutdown deadline exceeded: %v; forcing close\n", err)
		_ = srv.Close()
	}

	fmt.Println("portcullis: the gate has fallen")
	//TODO: Shutdown gracefully the http server

	// For now, just block on the context so Ctrl-C does something.
	return nil
}
