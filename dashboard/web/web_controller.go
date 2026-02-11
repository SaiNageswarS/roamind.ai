package web

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strings"

	"github.com/SaiNageswarS/go-api-boot/logger"
	"github.com/SaiNageswarS/go-api-boot/server"
	"github.com/SaiNageswarS/roamind.ai/dashboard/handler"
	"go.uber.org/zap"
)

// EmbeddedAssets holds the embedded file systems for templates and static files.
type EmbeddedAssets struct {
	TemplateFS embed.FS
	StaticFS   embed.FS
}

// PageData is the common data passed to every page template.
type PageData struct {
	ActivePage string
	UserID     string
	Data       interface{}
}

const userIDCookie = "roamind_user_id"

// WebController serves the web dashboard UI using server-side rendering.
type WebController struct {
	assets    *EmbeddedAssets
	handler   *handler.ProfileCardHandler
	templates map[string]*template.Template
}

// ProvideWebController is the DI factory for the web controller.
func ProvideWebController(assets *EmbeddedAssets, h *handler.ProfileCardHandler) *WebController {
	ctrl := &WebController{
		assets:  assets,
		handler: h,
	}
	ctrl.initTemplates()
	return ctrl
}

// initTemplates parses all page templates from the embedded FS.
func (c *WebController) initTemplates() {
	c.templates = make(map[string]*template.Template)

	funcMap := template.FuncMap{
		"join": strings.Join,
	}

	// Full-page templates: layout + partials + page
	pages := []string{"index", "profile-cards"}
	for _, page := range pages {
		t := template.Must(
			template.New("").Funcs(funcMap).ParseFS(
				c.assets.TemplateFS,
				"html-templates/layout.html",
				"html-templates/partials/*.html",
				fmt.Sprintf("html-templates/pages/%s.html", page),
			),
		)
		c.templates[page] = t
	}

	// Fragment-only templates (for HTMX responses)
	c.templates["card_rows"] = template.Must(
		template.New("").Funcs(funcMap).ParseFS(
			c.assets.TemplateFS,
			"html-templates/partials/card_rows.html",
		),
	)
}

// render executes a full-page template wrapped in layout.
func (c *WebController) render(w http.ResponseWriter, page string, data PageData) {
	tmpl, ok := c.templates[page]
	if !ok {
		http.Error(w, "Template not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "layout", data); err != nil {
		logger.Error("Template render error", zap.String("page", page), zap.Error(err))
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// renderFragment executes a standalone template fragment (for HTMX swaps).
func (c *WebController) renderFragment(w http.ResponseWriter, name string, data interface{}) {
	tmpl, ok := c.templates[name]
	if !ok {
		http.Error(w, "Fragment not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, name, data); err != nil {
		logger.Error("Fragment render error", zap.String("fragment", name), zap.Error(err))
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// ── Cookie helpers ──────────────────────────────────────────────────

func getUserID(r *http.Request) string {
	cookie, err := r.Cookie(userIDCookie)
	if err != nil || cookie.Value == "" {
		return ""
	}
	return cookie.Value
}

func setUserID(w http.ResponseWriter, userID string) {
	http.SetCookie(w, &http.Cookie{
		Name:     userIDCookie,
		Value:    userID,
		Path:     "/",
		MaxAge:   86400 * 365, // 1 year
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// ── Page Handlers ───────────────────────────────────────────────────

// IndexPage serves the dashboard home page.
func (c *WebController) IndexPage(w http.ResponseWriter, r *http.Request) {
	// Only serve the exact /web/ path; return 404 for sub-paths handled here.
	if r.URL.Path != "/web/" && r.URL.Path != "/web" {
		http.NotFound(w, r)
		return
	}
	c.render(w, "index", PageData{
		ActivePage: "home",
		UserID:     getUserID(r),
	})
}

// ProfileCardsPage serves the profile cards management page.
func (c *WebController) ProfileCardsPage(w http.ResponseWriter, r *http.Request) {
	c.render(w, "profile-cards", PageData{
		ActivePage: "profile-cards",
		UserID:     getUserID(r),
	})
}

// ── API Handlers (return HTML fragments for HTMX) ──────────────────

// ListProfileCards returns the profile card table rows as an HTML fragment.
func (c *WebController) ListProfileCards(w http.ResponseWriter, r *http.Request) {
	userID := getUserID(r)
	if userID == "" {
		c.renderFragment(w, "card_rows", map[string]interface{}{
			"Cards": nil,
			"Error": "Set your User ID in the top bar to view profile cards.",
		})
		return
	}

	cards, err := c.handler.ListProfileCards(r.Context(), userID)
	if err != nil {
		logger.Error("Failed to list profile cards", zap.Error(err))
		c.renderFragment(w, "card_rows", map[string]interface{}{
			"Cards": nil,
			"Error": "Failed to load profile cards.",
		})
		return
	}

	c.renderFragment(w, "card_rows", map[string]interface{}{
		"Cards": cards,
	})
}

// SaveProfileCard handles the create form submission and returns updated table rows.
func (c *WebController) SaveProfileCard(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	userID := r.FormValue("user_id")
	if userID == "" {
		userID = getUserID(r)
	}
	if userID == "" {
		http.Error(w, "User ID is required. Set it in the top bar.", http.StatusBadRequest)
		return
	}

	// Persist for future requests
	setUserID(w, userID)

	key := r.FormValue("key")
	title := r.FormValue("title")
	aliasesRaw := r.FormValue("aliases")
	contentMd := r.FormValue("content_md")

	if key == "" || title == "" || contentMd == "" {
		http.Error(w, "Key, title, and content are required.", http.StatusBadRequest)
		return
	}

	var aliases []string
	if aliasesRaw != "" {
		for _, a := range strings.Split(aliasesRaw, ",") {
			if trimmed := strings.TrimSpace(a); trimmed != "" {
				aliases = append(aliases, trimmed)
			}
		}
	}

	_, err := c.handler.SaveProfileCard(r.Context(), userID, key, title, aliases, contentMd)
	if err != nil {
		logger.Error("Failed to save profile card", zap.Error(err))
		http.Error(w, "Failed to save profile card: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Return updated card list
	cards, _ := c.handler.ListProfileCards(r.Context(), userID)

	// Tell HTMX to show toast and close modal
	w.Header().Set("HX-Trigger", `{"showToast":"Profile card saved successfully","closeModal":"createCardModal"}`)

	c.renderFragment(w, "card_rows", map[string]interface{}{
		"Cards": cards,
	})
}

// SetUserID stores the user ID in a cookie.
func (c *WebController) SetUserID(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	userID := r.FormValue("user_id")
	if userID == "" {
		http.Error(w, "User ID is required.", http.StatusBadRequest)
		return
	}

	setUserID(w, userID)
	w.WriteHeader(http.StatusOK)
}

// ServeStatic serves embedded static files (CSS, JS).
func (c *WebController) ServeStatic(w http.ResponseWriter, r *http.Request) {
	staticSub, err := fs.Sub(c.assets.StaticFS, "static")
	if err != nil {
		http.Error(w, "Static files not available", http.StatusInternalServerError)
		return
	}
	http.StripPrefix("/static/", http.FileServerFS(staticSub)).ServeHTTP(w, r)
}

// ── Routes ──────────────────────────────────────────────────────────

func (c *WebController) Routes() []server.Route {
	return []server.Route{
		// Pages
		{Pattern: "/web/", Method: http.MethodGet, Handler: c.IndexPage},
		{Pattern: "/web/profile-cards", Method: http.MethodGet, Handler: c.ProfileCardsPage},

		// HTMX fragment APIs
		{Pattern: "/web/api/profile-cards/list", Method: http.MethodGet, Handler: c.ListProfileCards},
		{Pattern: "/web/api/profile-cards/save", Method: http.MethodPost, Handler: c.SaveProfileCard},
		{Pattern: "/web/api/user/set", Method: http.MethodPost, Handler: c.SetUserID},

		// Static assets
		{Pattern: "/static/", Method: http.MethodGet, Handler: c.ServeStatic},
	}
}
