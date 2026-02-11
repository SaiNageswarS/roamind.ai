package controller

import (
	"encoding/json"
	"net/http"

	"github.com/SaiNageswarS/go-api-boot/auth"
	"github.com/SaiNageswarS/go-api-boot/logger"
	"github.com/SaiNageswarS/go-api-boot/server"
	"github.com/SaiNageswarS/roamind.ai/dashboard/handler"
	"go.uber.org/zap"
)

type ProfileCardController struct {
	saveProfileCardHandler *handler.ProfileCardHandler
}

func ProvideProfileCardController(saveProfileCardHandler *handler.ProfileCardHandler) *ProfileCardController {
	return &ProfileCardController{saveProfileCardHandler: saveProfileCardHandler}
}

type SaveProfileCardRequest struct {
	Key               string   `json:"key"`
	Title             string   `json:"title"`
	Aliases           []string `json:"aliases"`
	ContentMdFilePath string   `json:"content_md_file_path"`
}

type SaveProfileCardResponse struct {
	ProfileCardId string `json:"profile_card_id"`
	Message       string `json:"message"`
}

func (c *ProfileCardController) SaveProfileCard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userId, _ := auth.GetUserIdAndTenant(ctx)

	var request SaveProfileCardRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		logger.Error("Failed to decode request body", zap.Error(err))
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate required fields
	if request.Key == "" || request.Title == "" || request.ContentMdFilePath == "" {
		http.Error(w, "Key, title, and content_md_file_path are required", http.StatusBadRequest)
		return
	}

	result, err := c.saveProfileCardHandler.SaveProfileCard(
		ctx,
		userId,
		request.Key,
		request.Title,
		request.Aliases,
		request.ContentMdFilePath,
	)
	if err != nil {
		logger.Error("Failed to save profile card", zap.Error(err))
		http.Error(w, "Failed to save profile card", http.StatusInternalServerError)
		return
	}

	response := SaveProfileCardResponse{
		ProfileCardId: result,
		Message:       "Profile card saved successfully",
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(response)
}

func (c *ProfileCardController) Routes() []server.Route {
	return []server.Route{
		{
			Pattern: "/profile-cards",
			Method:  http.MethodPost,
			Handler: auth.VerifyTokenHttpMiddleware(c.SaveProfileCard),
		},
	}
}
