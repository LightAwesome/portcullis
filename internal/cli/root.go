// Package cli builds the Cobra command tree that backs the portcullis binary.
//
// The tree:
//
//	portcullis            — root, prints help with no args
//	  ├── raise           — start the gateway server
//	  ├── muster          — list registered garrisons (clients)
//	  ├── garrison        — manage clients
//	  │     └── add       — register a new client
//	  ├── route           — manage upstream routes
//	  ├── policy          — manage rate-limit policies
//	  ├── chronicle       — view request logs
//	  └── status          — health-check the gateway
//
// Subcommands beyond raise and muster are stubbed in this ticket and
// implemented in Phase 6.
package cli

import (
	"context"
	"github.com/spf13/cobra"
)

// Execute builds the command tree and runs whatever subcommand the user invoked.
// Returns any error from command execution; callers (typically main) should
// translate non-nil errors into a non-zero exit code.
func Execute() error {
	return ExecuteContext(context.Background())
}

// ExecuteContext is like Execute but uses ctx as the base context for all
// subcommands. Cancelling ctx (e.g. on SIGTERM) signals running subcommands
// to wind down.
func ExecuteContext(ctx context.Context) error {
	return root().ExecuteContext(ctx)
}

// root constructs the top-level command and attaches subcommands.
//
// Each subcommand is a function returning *cobra.Command — same pattern as
// the http handler-returning closures we use in the server. Dependencies
// captured at construction time, used at execution time.
func root() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "portcullis",
		Short: "Portcullis — a self-hosted API gateway",
		Long: `Portcullis is a self-hosted API gateway written in Go.

It sits between your applications and external APIs, providing
authentication, rate limiting, request logging, and observability
through a single trusted intermediary.

Run 'portcullis raise' to start the gateway. Other subcommands
manage configuration via the admin API.`,
		// SilenceUsage prevents Cobra from printing the full usage block
		// on every error. Errors from subcommands are usually about runtime
		// problems (database down, etc.), not about wrong invocation —
		// printing the help text in those cases is noise.
		SilenceUsage: true,
		// SilenceErrors lets us format errors ourselves in main.go.
		SilenceErrors: true,
	}

	cmd.AddCommand(
		raiseCmd(),
		musterCmd(),
	)

	return cmd
}
