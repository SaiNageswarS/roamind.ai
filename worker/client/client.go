// Package client creates the Hatchet client. Its settings come from the
// environment: deploy/bin/with-hatchet.sh loads them from the Hatchet CLI
// profile.
package client

import (
	"errors"
	"os"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
)

// HatchetClient returns a client for the tenant in the environment.
func HatchetClient() (*hatchet.Client, error) {
	if os.Getenv("HATCHET_CLIENT_TOKEN") == "" {
		return nil, errors.New("HATCHET_CLIENT_TOKEN is not set; run through deploy/bin/with-hatchet.sh")
	}
	return hatchet.NewClient()
}
