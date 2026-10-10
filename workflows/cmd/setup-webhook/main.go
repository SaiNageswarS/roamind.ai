// Command setup-webhook (re)creates the Hatchet Slack webhook that
// turns Slack Events API requests into Hatchet events, and prints the URL to
// paste into the Slack app's Event Subscriptions.
//
//	SLACK_SIGNING_SECRET=... ROAMIND_PUBLIC_URL=https://host.tailnet.ts.net go run ./cmd/setup-webhook
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/hatchet-dev/hatchet/pkg/client/rest"
	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	"github.com/hatchet-dev/hatchet/sdks/go/features"

	"roamind.ai/channels/slack"
)

const webhookName = "slack"

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	secret := os.Getenv("SLACK_SIGNING_SECRET")
	if secret == "" {
		return fmt.Errorf("SLACK_SIGNING_SECRET is required")
	}
	client, err := hatchet.NewClient()
	if err != nil {
		return err
	}
	hooks := client.Webhooks()

	// Recreate rather than update: the signing secret can only be set on
	// create, and it must follow the Slack app's current secret.
	if wh, err := hooks.Get(ctx, webhookName); err == nil && wh != nil {
		if err := hooks.Delete(ctx, webhookName); err != nil {
			return fmt.Errorf("delete webhook: %w", err)
		}
	}
	_, err = hooks.Create(ctx, features.CreateWebhookOpts{
		Name:               webhookName,
		SourceName:         rest.SLACK,
		EventKeyExpression: slack.EventKeyExpression,
		Auth: features.HMACAuth{
			SigningSecret:       secret,
			SignatureHeaderName: "X-Slack-Signature",
			Algorithm:           rest.SHA256,
			Encoding:            rest.HEX,
		},
	})
	if err != nil {
		return fmt.Errorf("create webhook: %w", err)
	}
	fmt.Println("registered webhook", webhookName)

	tenant, err := tenantID()
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/api/v1/stable/tenants/%s/webhooks/%s", tenant, webhookName)
	base := strings.TrimRight(os.Getenv("ROAMIND_PUBLIC_URL"), "/")
	fmt.Println("Slack Request URL:", base+path)
	fmt.Println("Funnel path prefix:", fmt.Sprintf("/api/v1/stable/tenants/%s/webhooks", tenant))
	return nil
}

// tenantID reads HATCHET_CLIENT_TENANT_ID, falling back to the "sub" claim of
// the HATCHET_CLIENT_TOKEN JWT, which is how the SDK derives it.
func tenantID() (string, error) {
	if t := os.Getenv("HATCHET_CLIENT_TENANT_ID"); t != "" {
		return t, nil
	}
	parts := strings.Split(os.Getenv("HATCHET_CLIENT_TOKEN"), ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("HATCHET_CLIENT_TOKEN is not a JWT; set HATCHET_CLIENT_TENANT_ID")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("decode token claims: %w", err)
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil || claims.Sub == "" {
		return "", fmt.Errorf("token has no sub claim; set HATCHET_CLIENT_TENANT_ID")
	}
	return claims.Sub, nil
}
