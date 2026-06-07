package services

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/SaiNageswarS/roamind.ai/gateway/db"
)

// ProfileService handles gateway-owned /mode_* Telegram commands so that
// the agent LLM is not involved in simple mode changes.
type ProfileService struct {
	profile *db.ProfileRepo
}

// NewProfileService returns nil when no profile repo is available.
func NewProfileService(profile *db.ProfileRepo) *ProfileService {
	if profile == nil {
		return nil
	}
	return &ProfileService{profile: profile}
}

// ParseAndExecute routes a raw inbound text line. Returns handled=false when
// the text does not begin with a recognized /mode_* command so the caller
// can fall through to the LLM path.
func (s *ProfileService) ParseAndExecute(ctx context.Context, userID, raw string) (reply string, handled bool, err error) {
	if s == nil {
		return "", false, nil
	}
	text := strings.TrimSpace(raw)
	if !strings.HasPrefix(text, "/mode") {
		return "", false, nil
	}
	if userID == "" {
		return "mode: missing user", true, nil
	}

	cmd, rest := splitCmd(text)
	switch cmd {
	case "/mode_set":
		return s.cmdSet(ctx, userID, rest)
	case "/mode_get":
		return s.cmdGet(ctx, userID)
	case "/mode_help", "/mode":
		return modeHelp(), true, nil
	default:
		return "mode: unknown command. " + modeHelp(), true, nil
	}
}

// --- command handlers ---------------------------------------------------

func (s *ProfileService) cmdSet(ctx context.Context, userID, rest string) (string, bool, error) {
	mode := strings.ToLower(strings.TrimSpace(rest))
	if mode == "" {
		return "usage: /mode_set <mode>\n" + modeHelp(), true, nil
	}
	if err := s.profile.SetMode(ctx, userID, mode); err != nil {
		return fmt.Sprintf("mode_set: %s", err.Error()), true, nil
	}
	return fmt.Sprintf("Mode set to '%s'.", mode), true, nil
}

func (s *ProfileService) cmdGet(ctx context.Context, userID string) (string, bool, error) {
	mode := s.profile.GetMode(ctx, userID)
	return fmt.Sprintf("Current mode: %s", mode), true, nil
}

// --- help ---------------------------------------------------------------

func modeHelp() string {
	validList := sortedModes()
	return strings.Join([]string{
		"mode commands:",
		"  /mode_set <mode>   — set scheduling mode",
		"  /mode_get          — show current mode",
		"",
		"valid modes: " + strings.Join(validList, ", "),
	}, "\n")
}

func sortedModes() []string {
	modes := make([]string, 0, len(db.ValidModes))
	for m := range db.ValidModes {
		modes = append(modes, m)
	}
	sort.Strings(modes)
	return modes
}
