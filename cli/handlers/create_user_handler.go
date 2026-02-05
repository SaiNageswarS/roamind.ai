package handlers

import (
	"context"
	"fmt"

	"github.com/SaiNageswarS/go-api-boot/logger"
	"github.com/SaiNageswarS/go-api-boot/odm"
	"github.com/SaiNageswarS/go-collection-boot/async"
	"github.com/SaiNageswarS/roamind.ai/memory/db"
	"go.uber.org/zap"
)

func (h *Handlers) HandleCreateUser(ctx context.Context, email, name, phoneNumber string) error {
	// Validate required fields
	if email == "" {
		return fmt.Errorf("email is required")
	}
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if phoneNumber == "" {
		return fmt.Errorf("phoneNumber is required")
	}

	user := db.UserModel{
		Email:       email,
		Name:        name,
		PhoneNumber: phoneNumber,
	}

	logger.Info("Creating user", zap.String("email", email), zap.String("UserId", user.Id()))
	_, err := async.Await(
		odm.CollectionOf[db.UserModel](h.Mongo, "roamind").Save(ctx, user))

	return err
}
