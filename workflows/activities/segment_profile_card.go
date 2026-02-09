package activities

import (
	"context"
	"errors"
	"strings"

	"github.com/SaiNageswarS/go-api-boot/logger"
	"github.com/SaiNageswarS/go-api-boot/odm"
	"github.com/SaiNageswarS/go-collection-boot/async"
	"github.com/SaiNageswarS/go-collection-boot/linq"
	"github.com/SaiNageswarS/roamind.ai/memory/db"
	"go.uber.org/zap"
)

// Returns the profileCardId after segmentation. Segments are saved back to the ProfileCardModel.ContentMd as markdown sections.
func (a *Activities) SegmentProfileCard(ctx context.Context, profileCardId string) (string, error) {
	profileCard, err := async.Await(
		odm.CollectionOf[db.ProfileCardModel](a.mongo, Tenant).FindOneByID(ctx, profileCardId))

	if err != nil {
		logger.Error("Failed to find profile card", zap.String("profileCardId", profileCardId), zap.Error(err))
		return "", err
	}

	if len(profileCard.ContentMd) == 0 {
		return "", errors.New("profile card has no content to segment")
	}

	markdownContent := profileCard.ContentMd[0]
	chunks, err := a.parseMarkdownSections(ctx, markdownContent, 1000*4) // ~1000 tokens * 4 chars per token
	if err != nil {
		logger.Error("Failed to parse markdown sections", zap.String("profileCardId", profileCardId), zap.Error(err))
		return "", err
	}

	// Update ContentMd with the segmented chunks
	profileCard.ContentMd = chunks

	_, err = async.Await(
		odm.CollectionOf[db.ProfileCardModel](a.mongo, Tenant).Save(ctx, *profileCard))

	return profileCard.Id(), err
}

func (a *Activities) parseMarkdownSections(ctx context.Context, content string, minBytes int) ([]string, error) {
	if content == "" {
		return nil, errors.New("empty content")
	}

	// Split by ## headers (level 2 headings)
	parts := strings.Split(content, "\n## ")
	if len(parts) == 1 {
		// No ## headers found, return entire content as single chunk
		return []string{content}, nil
	}

	// Build sections
	var sections []string

	currSectionString := ""

	_, err := linq.Pipe3(
		linq.FromSlice(ctx, parts),
		// Remove empty parts before processing
		linq.Where(func(part string) bool {
			return strings.TrimSpace(part) != ""
		}),
		// Add ## prefix back to all sections except the first one
		linq.Select(func(part string) string {
			if strings.HasPrefix(part, "# ") {
				// If the part starts with a level 1 header, keep it as is (handles the first section case)
				return part
			}
			// Otherwise, add ## prefix back
			return "## " + strings.TrimSpace(part)
		}),
		linq.ForEach(func(section string) {
			currSectionString += section + "\n\n"

			if len(currSectionString) >= minBytes {
				sections = append(sections, strings.TrimSpace(currSectionString))
				currSectionString = ""
			}
		}),
	)

	if err != nil {
		return nil, err
	}

	// Add any remaining content as a section
	if strings.TrimSpace(currSectionString) != "" {
		sections = append(sections, strings.TrimSpace(currSectionString))
	}

	return sections, nil
}
