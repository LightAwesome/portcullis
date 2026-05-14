package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/LightAwesome/portcullis/internal/config"
	"github.com/LightAwesome/portcullis/internal/store"
)

// musterCmd returns the `portcullis muster` command — list registered garrisons.
//
// In the themed vocabulary, mustering a garrison means assembling the troops
// for inspection. Here it means "show me the registered clients."
func musterCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "muster",
		Short: "List registered garrisons (clients)",
		Long: `Muster the garrisons — list all clients registered with the gateway.

For each client, prints: ID, name, key ID (the public part of the
gateway key), active status, and creation date.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMuster(cmd.Context())
		},
	}
}

func runMuster(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	db, err := store.New(ctx, cfg.DatabaseURL, cfg.RedisURL, cfg.DefaultMaxRequests, cfg.DefaultWindowSeconds)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer db.Close()

	clients, err := db.ListClients(ctx)
	if err != nil {
		return fmt.Errorf("list clients: %w", err)
	}

	if len(clients) == 0 {
		fmt.Println("no garrisons registered.")
		return nil
	}

	// Tabular output. We'll fancy this up in P6.2 with a proper formatter.
	// TODO: Make fancy
	fmt.Printf("%-36s  %-20s  %-16s  %-7s  %s\n", "ID", "NAME", "KEY ID", "ACTIVE", "CREATED")
	for _, c := range clients {
		uuidStr, _ := c.ID.Value() // pgtype.UUID's Value() returns a driver.Value (interface{})
		fmt.Printf("%-36v  %-20s  %-16s  %-7v  %s\n",
			uuidStr, c.Name, c.KeyID, c.IsActive, c.CreatedAt.Format("2006-01-02 15:04"))
	}
	return nil
}
