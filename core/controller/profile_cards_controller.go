package controller

import (
	"net/http"
	"strings"

	"github.com/SaiNageswarS/go-api-boot/auth"
	"github.com/SaiNageswarS/go-api-boot/server"
	"github.com/SaiNageswarS/roamind.ai/core/handler"
)

type ProfileCardsController struct {
	profileCardSearchHandler *handler.ProfileCardSearchHandler
}

func ProvideProfileCardsController(profileCardSearchHandler *handler.ProfileCardSearchHandler) *ProfileCardsController {
	return &ProfileCardsController{profileCardSearchHandler: profileCardSearchHandler}
}

func (c *ProfileCardsController) QueryProfileCards(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userId, _ := auth.GetUserIdAndTenant(ctx)

	query := r.URL.Query().Get("query")
	if query == "" {
		http.Error(w, "Query is required", http.StatusBadRequest)
		return
	}

	topK := 10 // default value

	results, err := c.profileCardSearchHandler.SearchProfileCards(ctx, userId, query, topK)
	if err != nil {
		http.Error(w, "Failed to search profile cards", http.StatusInternalServerError)
		return
	}

	// return markdown content as response
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	var markdownResponse strings.Builder
	for _, card := range results {
		markdownResponse.WriteString("## " + card.Title + "\n\n")

		for _, para := range card.ContentMd {
			markdownResponse.WriteString(para + "\n\n")
		}
		markdownResponse.WriteString("---\n\n") // separator between cards
	}

	w.Write([]byte(markdownResponse.String()))
}

func (c *ProfileCardsController) Routes() []server.Route {
	return []server.Route{
		{
			Pattern: "/profile-cards/search",
			Method:  http.MethodGet,
			Handler: auth.VerifyTokenHttpMiddleware(c.QueryProfileCards),
		},
	}
}
