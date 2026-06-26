package api

import (
	"net/http"

	"zruvix/internal/changelog"
	"zruvix/internal/config"
	"zruvix/internal/version"
)

// handleDocs redirects /v1/docs to the hosted documentation site
// (docs.zruvix.com by default; override with DOCS_URL). The documentation now
// lives in a dedicated Fumadocs site instead of being served inline here.
func handleDocs(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, config.C.DocsURL, http.StatusFound)
}

// handleVersion returns the current version and full changelog as JSON.
// Dynamic entries (from POST /v1/changelog) appear first, followed by static.
func handleVersion(w http.ResponseWriter, _ *http.Request) {
	respondOK(w, map[string]any{
		"version":           version.Version,
		"changelog":         version.Changelog,
		"dynamic_changelog": changelog.List(),
	})
}
