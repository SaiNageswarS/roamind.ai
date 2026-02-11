package main

import (
	"embed"

	"github.com/SaiNageswarS/roamind.ai/dashboard/web"
)

//go:embed html-templates
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// ProvideEmbeddedAssets creates an EmbeddedAssets struct from the
// compile-time embedded file systems. This is registered with the
// DI container so the WebController can serve templates and static
// files from the single binary.
func ProvideEmbeddedAssets() *web.EmbeddedAssets {
	return &web.EmbeddedAssets{
		TemplateFS: templateFS,
		StaticFS:   staticFS,
	}
}
