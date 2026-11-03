// Command setup-webhook (re)creates the Hatchet webhooks that turn inbound
// requests into Hatchet events, and prints the URLs to give their senders:
//
//   - slack, when SLACK_SIGNING_SECRET is set: Slack Events API requests,
//     for the Slack app's Event Subscriptions;
//
//   - gmail, when ROAMIND_MAIL_SECRET is set: mail from each Gmail
//     account's Apps Script (deploy/apps-script), signed with that secret,
//     for the script's WEBHOOK_URL property.
//
//     SLACK_SIGNING_SECRET=... ROAMIND_MAIL_SECRET=... ROAMIND_PUBLIC_URL=https://host.tailnet.ts.net go run ./cmd/setup-webhook
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
	"roamind.ai/worker/workflows"
)

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	slackSecret, mailSecret := os.Getenv("SLACK_SIGNING_SECRET"), os.Getenv("ROAMIND_MAIL_SECRET")
	if slackSecret == "" && mailSecret == "" {
		return fmt.Errorf("SLACK_SIGNING_SECRET or ROAMIND_MAIL_SECRET is required")
	}
	client, err := hatchet.NewClient()
	if err != nil {
		return err
	}
	tenant, err := tenantID()
	if err != nil {
		return err
	}
	prefix := fmt.Sprintf("/api/v1/stable/tenants/%s/webhooks", tenant)
	base := strings.TrimRight(os.Getenv("ROAMIND_PUBLIC_URL"), "/") + prefix
	if slackSecret != "" {
		if err := recreate(ctx, client.Webhooks(), features.CreateWebhookOpts{
			Name:               "slack",
			SourceName:         rest.SLACK,
			EventKeyExpression: slack.EventKeyExpression,
			Auth: features.HMACAuth{
				SigningSecret:       slackSecret,
				SignatureHeaderName: "X-Slack-Signature",
				Algorithm:           rest.SHA256,
				Encoding:            rest.HEX,
			},
		}); err != nil {
			return err
		}
		fmt.Println("Slack Request URL:", base+"/slack")
	}
	if mailSecret != "" {
		// Hatchet checks a generic webhook's signature over the raw body,
		// which Apps Script signs with Utilities.computeHmacSha256Signature.
		if err := recreate(ctx, client.Webhooks(), features.CreateWebhookOpts{
			Name:               "gmail",
			SourceName:         rest.GENERIC,
			EventKeyExpression: "'" + workflows.MailEventKey + "'",
			Auth: features.HMACAuth{
				SigningSecret:       mailSecret,
				SignatureHeaderName: "X-Roamind-Signature",
				Algorithm:           rest.SHA256,
				Encoding:            rest.HEX,
			},
		}); err != nil {
			return err
		}
		fmt.Println("Apps Script WEBHOOK_URL:", base+"/gmail")
	}
	fmt.Println("Funnel path prefix:", prefix)
	return nil
}

// recreate deletes the webhook named in opts, if there is one, and creates
// it: its secret can only be set on create, and must follow .env.
func recreate(ctx context.Context, hooks *features.WebhooksClient, opts features.CreateWebhookOpts) error {
	if wh, err := hooks.Get(ctx, opts.Name); err == nil && wh != nil {
		if err := hooks.Delete(ctx, opts.Name); err != nil {
			return fmt.Errorf("delete webhook %s: %w", opts.Name, err)
		}
	}
	if _, err := hooks.Create(ctx, opts); err != nil {
		return fmt.Errorf("create webhook %s: %w", opts.Name, err)
	}
	fmt.Println("registered webhook", opts.Name)
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
