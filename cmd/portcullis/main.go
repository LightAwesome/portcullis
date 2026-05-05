// Command portcullis is the API gateway server and CLI.
package main

import (
	"fmt"
	"github.com/LightAwesome/portcullis/internal/config"
	"os"
)

func main() {
	cfg, err := config.Load()
	// fmt.Println("portcullis: not yet implemented")

	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Printf("portcullis: loaded config for %s environment\n", cfg.Env)
	fmt.Printf("  addr:      %s\n", cfg.Addr)
	fmt.Printf("  log_level: %s\n", cfg.LogLevel)
	fmt.Printf("  database:  %s\n", redact(cfg.DatabaseURL))
	fmt.Printf("  redis:     %s\n", redact(cfg.RedisURL))
}

func redact(text string) string {
	return "redacted"
}
