package cli

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/spf13/cobra"
)

func siegeCmd() *cobra.Command {
	var (
		concurrent int
		total      int
		method     string
		path       string
		baseURL    string
		key        string
		timeout    time.Duration
	)

	cmd := &cobra.Command{
		Use:   "siege <prefix>",
		Short: "Lay siege to a route (load test)",
		Long: `Lay siege to the gateway by firing concurrent requests at a route.

Reports status-code distribution, requests per second, and latency
percentiles. Useful for demonstrating the rate limiter under load.

The gateway key can be supplied via --key or the PORTCULLIS_GATEWAY_KEY
environment variable.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			prefix := args[0]

			// Resolve the gateway key. Flag overrides env.
			if key == "" {
				key = os.Getenv("PORTCULLIS_GATEWAY_KEY")
			}
			if key == "" {
				return fmt.Errorf("gateway key required (--key or PORTCULLIS_GATEWAY_KEY)")
			}

			// Build the target URL.
			targetURL, err := buildSiegeURL(baseURL, prefix, path)
			if err != nil {
				return fmt.Errorf("invalid URL: %w", err)
			}

			cfg := SiegeConfig{
				URL:        targetURL,
				Method:     method,
				GatewayKey: key,
				Concurrent: concurrent,
				Total:      total,
				Timeout:    timeout,
			}

			fmt.Printf("laying siege to /proxy/%s%s\n", prefix, path)
			fmt.Printf("  target:     %s\n", baseURL)
			fmt.Printf("  workers:    %d\n", concurrent)
			fmt.Printf("  total:      %d\n", total)
			fmt.Printf("  banner:     %s...\n", truncateKey(key))
			fmt.Println()

			var progressOut = os.Stdout
			if !isTerminal(os.Stdout) {
				progressOut = nil
			}

			result, err := runSiege(cmd.Context(), cfg, progressOut)
			if err != nil {
				return err
			}

			formatReport(os.Stdout, result)
			return nil
		},
	}

	cmd.Flags().IntVarP(&concurrent, "concurrent", "c", 10, "concurrent worker count")
	cmd.Flags().IntVarP(&total, "total", "n", 100, "total request count")
	cmd.Flags().StringVarP(&method, "method", "X", "GET", "HTTP method")
	cmd.Flags().StringVar(&path, "path", "/", "path appended to /proxy/<prefix>")
	cmd.Flags().StringVar(&baseURL, "url", "http://127.0.0.1:8080", "gateway base URL")
	cmd.Flags().StringVarP(&key, "key", "k", "", "gateway key (or set PORTCULLIS_GATEWAY_KEY)")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "per-request timeout")

	return cmd
}

// buildSiegeURL composes the full target URL from the base, the route
// prefix, and the optional sub-path.
func buildSiegeURL(baseURL, prefix, path string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("base URL must include scheme and host")
	}

	u.Path = "/proxy/" + prefix
	if path != "" && path != "/" {
		if path[0] != '/' {
			path = "/" + path
		}
		u.Path += path
	}
	return u.String(), nil
}

// truncateKey returns a display-safe truncated form of a gateway key.
// Shows enough to be recognisable in logs ("pck_4a8f2c1d...") without
// exposing the secret half.
func truncateKey(k string) string {
	if len(k) <= 16 {
		return k
	}
	return k[:16]
}

// We need context propagation from main's SIGINT context. Cobra exposes
// it via cmd.Context(). No special wiring here — the context is set up
// in cmd/portcullis/main.go via signal.NotifyContext.
var _ = context.Background
