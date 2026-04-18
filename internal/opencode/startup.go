package opencode

import (
	"context"
	"fmt"
	"log"
	"time"
)

// WaitForHealthy polls the OpenCode health endpoint at the given interval
// until it returns 200 OK or the context is cancelled. An immediate check
// is performed before the first tick to minimise latency when the server
// is already up.
func (c *Client) WaitForHealthy(ctx context.Context, interval time.Duration) error {
	if err := c.HealthCheck(); err == nil {
		return nil
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	attempt := 0
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("opencode not healthy: %w", ctx.Err())
		case <-ticker.C:
			attempt++
			if err := c.HealthCheck(); err == nil {
				log.Printf("opencode: healthy after %d retries", attempt)
				return nil
			}
			log.Printf("opencode: waiting for health (attempt %d)...", attempt)
		}
	}
}

// InjectAPIKey waits for OpenCode to be healthy, then injects the Anthropic API key
// via POST /auth. If apiKey is empty, the injection step is skipped. SetAuth failures
// are logged but not fatal — the user can re-inject via the Settings page.
func (c *Client) InjectAPIKey(ctx context.Context, pollInterval time.Duration, apiKey string) error {
	if err := c.WaitForHealthy(ctx, pollInterval); err != nil {
		return err
	}

	if apiKey == "" {
		log.Printf("opencode: no API key configured, skipping auth injection")
		return nil
	}

	if err := c.SetAuth("anthropic", apiKey); err != nil {
		log.Printf("opencode: failed to inject API key: %v (user can re-inject via Settings)", err)
		return nil // non-fatal
	}

	log.Printf("opencode: API key injected successfully")
	return nil
}
