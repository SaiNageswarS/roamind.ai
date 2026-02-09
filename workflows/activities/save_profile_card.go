package activities

import (
	"context"
	"os"

	"github.com/SaiNageswarS/go-api-boot/odm"
	"github.com/SaiNageswarS/go-collection-boot/async"
	"github.com/SaiNageswarS/roamind.ai/memory/db"
)

func (a *Activities) SaveProfileCard(ctx context.Context, userId, key, title string, aliases []string, contentMdFilePath string) (string, error) {
	// read content from file
	contentMd, err := readContentFromFile(contentMdFilePath)
	if err != nil {
		return "", err
	}

	profileCard := db.ProfileCardModel{
		UserId:    userId,
		Key:       key,
		Title:     title,
		Aliases:   aliases,
		ContentMd: []string{contentMd},
	}

	// save to db
	_, err = async.Await(
		odm.CollectionOf[db.ProfileCardModel](a.mongo, Tenant).Save(ctx, profileCard))

	return profileCard.Id(), err
}

func readContentFromFile(filePath string) (string, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	return string(content), nil
}
