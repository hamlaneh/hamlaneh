// Package healthcheck probes a running hamlaneh-server instance. It backs
// the "hamlaneh-server healthcheck" subcommand used by the container
// HEALTHCHECK, where no shell or curl exists in the distroless image.
package healthcheck

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
)

// Check performs an HTTP GET against url and returns nil when the response
// status is 200 OK. Any transport failure or other status is an error.
// Cancellation and timeouts come from ctx.
func Check(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build healthcheck request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("healthcheck request: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			slog.Warn("close healthcheck response body", "error", cerr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck: got status %d, want %d", resp.StatusCode, http.StatusOK)
	}
	return nil
}
