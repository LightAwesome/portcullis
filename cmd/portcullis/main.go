// Command portcullis is the API gateway server and CLI.
//
// Run with no arguments for help. The most common subcommand is `raise`,
// which starts the gateway server. See `portcullis --help` for the full
// command tree.
package main

import (
	"context"
	"fmt"
	"github.com/LightAwesome/portcullis/internal/cli"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	// signal.NotifyContext returns a context that cancels on the given
	// signals. We pass this down through every subcommand via cmd.SetContext;
	// any operation that respects context cancellation (HTTP servers, DB
	// queries, the eventual log worker) will wind down cleanly on Ctrl-C
	// or SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := cli.ExecuteContext(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "portcullis: %v\n", err)
		os.Exit(1)
	}
}
