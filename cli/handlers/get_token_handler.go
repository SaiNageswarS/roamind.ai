package handlers

import (
	"fmt"

	"github.com/SaiNageswarS/go-api-boot/auth"
	"github.com/SaiNageswarS/go-api-boot/logger"
	"github.com/SaiNageswarS/roamind.ai/memory/db"
	"go.uber.org/zap"
)

func (h *Handlers) HandleGetToken(email string) error {
	if email == "" {
		return fmt.Errorf("email is required")
	}

	userId := db.UserModel{Email: email}.Id()
	token, err := auth.GetToken("roamind", userId, "default")
	if err != nil {
		logger.Error("Failed to get token for user", zap.String("userId", userId), zap.Error(err))
		return err
	}

	logger.Info("Token retrieved successfully", zap.String("userId", userId), zap.String("token", token))
	return nil
}
