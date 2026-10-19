package web

import (
	"net/http"

	"sap13ns/internal/auth"
)

func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	c := s.cached
	admin := func(pattern string, cap auth.Cap, h http.HandlerFunc) { mux.HandleFunc(pattern, s.requireCap(cap, h)) }

	// Public site.
	mux.HandleFunc("GET /{$}", c(s.handleHome))
	mux.HandleFunc("GET /blog", c(s.handleBlog))
	mux.HandleFunc("GET /blog/{slug}", c(s.handlePost))
	mux.HandleFunc("POST /blog/{slug}/comments", s.handleComment)
	mux.HandleFunc("GET /category/{slug}", c(s.handleTermArchive("category")))
	mux.HandleFunc("GET /category/{slug}/{file}", c(s.handleScopedFeed("category")))
	mux.HandleFunc("GET /tag/{slug}", c(s.handleTermArchive("tag")))
	mux.HandleFunc("GET /tag/{slug}/{file}", c(s.handleScopedFeed("tag")))
	mux.HandleFunc("GET /author/{username}", c(s.handleAuthorArchive))
	mux.HandleFunc("GET /author/{slug}/{file}", c(s.handleScopedFeed("author")))
	mux.HandleFunc("GET /archive/{year}/{month}", c(s.handleDateArchive))
	mux.HandleFunc("GET /search", c(s.handleSearch))
	mux.HandleFunc("GET /ask", c(s.handleAsk))
	mux.HandleFunc("POST /ask", s.handleAsk)
	mux.HandleFunc("POST /api/assistant", s.apiAssistant)
	for _, m := range []string{"POST", "GET", "DELETE"} {
		mux.HandleFunc(m+" /mcp", s.handleMCP)
	}
	for file := range feedTypes {
		mux.HandleFunc("GET /"+file, c(s.handleSiteFeed(file)))
	}
	mux.HandleFunc("GET /sitemap.xml", c(s.handleSitemap))
	mux.HandleFunc("GET /robots.txt", s.handleRobots)
	mux.HandleFunc("GET /manifest.webmanifest", s.handleManifest)
	mux.HandleFunc("GET /og/{file}", s.handleOGImage)
	mux.HandleFunc("GET /llms.txt", c(s.handleLLMs))
	mux.HandleFunc("GET /llms-full.txt", c(s.handleLLMsFull))
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /static/{path...}", s.serveStatic)
	mux.HandleFunc("GET /themes/{theme}/static/{path...}", s.handleThemeStatic)
	mux.HandleFunc("GET /plugin-assets/{id}/{version}/{path...}", s.handlePluginAsset)
	mux.HandleFunc("POST /plugin-forms/{plugin}/{form}", s.handlePluginForm)
	if s.cfg.ServeUploads {
		mux.HandleFunc("GET /uploads/{path...}", s.handleUploads)
	}
	mux.HandleFunc("GET /{slug}", c(s.handlePage))

	// Authentication.
	mux.HandleFunc("GET /login", s.handleLoginForm)
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("GET /login/2fa", s.handleMFAForm)
	mux.HandleFunc("POST /login/2fa", s.handleMFA)
	mux.HandleFunc("POST /login/passkey/begin", s.loginPasskeyBegin)
	mux.HandleFunc("POST /login/passkey/finish", s.loginPasskeyFinish)
	mux.HandleFunc("POST /logout", s.handleLogout)
	mux.HandleFunc("GET /register", s.handleRegisterForm)
	mux.HandleFunc("POST /register", s.handleRegister)
	mux.HandleFunc("GET /forgot-password", s.handleForgotForm)
	mux.HandleFunc("POST /forgot-password", s.handleForgot)
	mux.HandleFunc("GET /reset-password", s.handleResetForm)
	mux.HandleFunc("POST /reset-password", s.handleReset)
	mux.HandleFunc("GET /setup", s.handleSetupForm)
	mux.HandleFunc("POST /setup", s.handleSetup)

	// Single sign-on through the network portal (sso.go): a tenant site
	// starts it and receives the code, the portal issues the code.
	if s.sso != nil {
		mux.HandleFunc("GET /sso/start", s.handleSSOStart)
		mux.HandleFunc("GET "+ssoCallback, s.handleSSOCallback)
		admin("POST /admin/profile/network/link", auth.CapAdminPanel, s.adminNetworkLink)
	}
	if s.portal != nil {
		mux.HandleFunc("GET /sso/authorize", s.handleSSOAuthorize)

		// The network admin area (admin_network.go), for network admins
		// with a second factor, and every account's sites.
		network := func(pattern string, h http.HandlerFunc) { admin(pattern, auth.CapAdminPanel, s.networkAdmin(h)) }
		network("GET /admin/network/{$}", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/admin/network/sites", http.StatusSeeOther)
		})
		network("GET /admin/network/sites", s.adminNetSites)
		network("GET /admin/network/sites/{id}", s.adminNetSite)
		network("POST /admin/network/sites/{id}/members", s.adminNetMemberSet)
		network("POST /admin/network/sites/{id}/members/{account}/delete", s.adminNetMemberRemove)
		network("POST /admin/network/sites/{id}/{action}", s.adminNetSiteAction)
		network("GET /admin/network/admins", s.adminNetAdmins)
		network("POST /admin/network/admins", s.adminNetAdminAdd)
		network("POST /admin/network/admins/{account}/delete", s.adminNetAdminRemove)
		network("GET /admin/network/plans", s.adminNetPlans)
		if s.reviews != nil {
			network("GET /admin/network/plugins", s.adminNetPlugins)
			network("POST /admin/network/plugins/review", s.adminNetPluginReview)
		}
		admin("GET /admin/sites", auth.CapAdminPanel, s.adminMySites)
		admin("POST /admin/sites/request", auth.CapAdminPanel, s.adminSiteRequest)

		// Billing (billing.go), with a Stripe key only. Stripe calls the
		// webhook without a session; the signature authenticates it.
		if s.billing != nil {
			mux.HandleFunc("POST /billing/webhook", s.handleBillingWebhook)
			admin("POST /admin/sites/{id}/upgrade", auth.CapAdminPanel, s.adminSiteUpgrade)
			admin("POST /admin/sites/{id}/billing", auth.CapAdminPanel, s.adminSiteBilling)
			network("GET /admin/network/billing", s.adminNetBilling)
			network("POST /admin/network/billing/{event}/retry", s.adminNetBillingRetry)
		}
	}

	// Administration.
	mux.HandleFunc("GET /admin", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusMovedPermanently)
	})
	admin("GET /admin/{$}", auth.CapAdminPanel, s.adminDashboard)
	admin("GET /admin/tools/import", auth.CapManageSettings, s.adminImport)
	admin("POST /admin/tools/import", auth.CapManageSettings, s.adminImportStart)
	admin("GET /admin/tools/redirects", auth.CapManageSettings, s.adminRedirects)
	admin("POST /admin/tools/redirects", auth.CapManageSettings, s.adminRedirectSave)
	admin("POST /admin/tools/redirects/{id}", auth.CapManageSettings, s.adminRedirectSave)
	admin("POST /admin/tools/redirects/{id}/delete", auth.CapManageSettings, s.adminRedirectDelete)

	for _, typ := range []string{"posts", "pages"} {
		cp := auth.CapEditOwnPosts
		if typ == "pages" {
			cp = auth.CapManagePages
		}
		admin("GET /admin/"+typ, cp, s.adminPostList)
		admin("GET /admin/"+typ+"/new", cp, s.adminPostNew)
		admin("POST /admin/"+typ+"/new", cp, s.adminPostCreate)
		admin("POST /admin/"+typ+"/bulk", cp, s.adminPostBulk)
		admin("POST /admin/"+typ+"/empty-trash", cp, s.adminEmptyTrash)
	}
	admin("GET /admin/posts/{id}", auth.CapEditOwnPosts, s.adminPostEdit)
	admin("POST /admin/posts/{id}", auth.CapEditOwnPosts, s.adminPostUpdate)
	admin("POST /admin/posts/{id}/status", auth.CapEditOwnPosts, s.adminPostStatus)
	admin("POST /admin/posts/{id}/delete", auth.CapEditOwnPosts, s.adminPostDelete)
	admin("POST /admin/posts/{id}/translate", auth.CapEditOwnPosts, s.adminPostTranslate)
	admin("POST /admin/posts/{id}/unlink", auth.CapEditOwnPosts, s.adminPostUnlink)
	admin("GET /admin/posts/{id}/preview", auth.CapEditOwnPosts, s.adminPostPreview)
	admin("GET /admin/posts/{id}/revisions", auth.CapEditOwnPosts, s.adminRevisions)
	admin("POST /admin/revisions/{id}/restore", auth.CapEditOwnPosts, s.adminRevisionRestore)
	admin("POST /admin/api/preview", auth.CapEditOwnPosts, s.apiPreview)
	admin("POST /admin/api/ai/image", auth.CapEditOwnPosts, s.apiAIImage)
	admin("POST /admin/api/ai/translate", auth.CapEditOwnPosts, s.apiAITranslate)
	admin("GET /admin/api/ai/jobs/{job}", auth.CapEditOwnPosts, s.apiAIJob)
	admin("POST /admin/api/ai/{task}", auth.CapEditOwnPosts, s.apiAI)
	admin("GET /admin/settings/ai", auth.CapManageSettings, s.adminAISettings)
	admin("POST /admin/settings/ai", auth.CapManageSettings, s.adminAISettingsSave)
	admin("GET /admin/settings/assistant", auth.CapManageSettings, s.adminAssistantSettings)
	admin("POST /admin/settings/assistant", auth.CapManageSettings, s.adminAssistantSettingsSave)
	admin("POST /admin/settings/assistant/rebuild", auth.CapManageSettings, s.adminAssistantRebuild)
	admin("POST /admin/api/posts/{id}/heartbeat", auth.CapEditOwnPosts, s.apiHeartbeat)
	admin("GET /admin/api/posts/{id}/autosave", auth.CapEditOwnPosts, s.apiAutosave)
	admin("DELETE /admin/api/posts/{id}/autosave", auth.CapEditOwnPosts, s.apiAutosave)
	admin("POST /admin/api/posts/{id}/unlock", auth.CapEditOwnPosts, s.apiUnlock)
	admin("POST /admin/posts/{id}/takeover", auth.CapEditOwnPosts, s.adminPostTakeover)
	admin("POST /admin/api/slug", auth.CapEditOwnPosts, s.apiSlug)

	admin("GET /admin/terms/{taxonomy}", auth.CapManageTerms, s.adminTerms)
	admin("POST /admin/terms/{taxonomy}", auth.CapManageTerms, s.adminTermCreate)
	admin("GET /admin/terms/{taxonomy}/{id}", auth.CapManageTerms, s.adminTermEdit)
	admin("POST /admin/terms/{taxonomy}/{id}", auth.CapManageTerms, s.adminTermUpdate)
	admin("POST /admin/terms/{taxonomy}/{id}/delete", auth.CapManageTerms, s.adminTermDelete)

	admin("GET /admin/media", auth.CapUploadMedia, s.adminMedia)
	admin("POST /admin/media", auth.CapUploadMedia, s.adminMediaUpload)
	admin("GET /admin/media/{id}", auth.CapUploadMedia, s.adminMediaEdit)
	admin("POST /admin/media/{id}", auth.CapUploadMedia, s.adminMediaUpdate)
	admin("POST /admin/media/{id}/delete", auth.CapUploadMedia, s.adminMediaDelete)
	admin("GET /admin/api/media", auth.CapEditOwnPosts, s.apiMediaList)

	admin("GET /admin/comments", auth.CapModerate, s.adminComments)
	admin("POST /admin/comments/{id}/{action}", auth.CapModerate, s.adminCommentAction)
	admin("POST /admin/comments/bulk", auth.CapModerate, s.adminCommentBulk)
	admin("POST /admin/comments/empty/{status}", auth.CapModerate, s.adminCommentEmpty)

	admin("GET /admin/appearance/themes", auth.CapAppearance, s.adminThemes)
	admin("POST /admin/appearance/themes/{slug}/activate", auth.CapAppearance, s.adminThemeActivate)
	admin("GET /admin/appearance/themes/{slug}/options", auth.CapAppearance, s.adminThemeOptions)
	admin("POST /admin/appearance/themes/{slug}/options", auth.CapAppearance, s.adminThemeOptionsSave)
	s.customRoutes(mux, admin) // theme customiser (customize.go)
	admin("GET /admin/appearance/menus", auth.CapAppearance, s.adminMenus)
	admin("POST /admin/appearance/menus", auth.CapAppearance, s.adminMenuAdd)
	admin("POST /admin/appearance/menus/{id}", auth.CapAppearance, s.adminMenuUpdate)
	admin("POST /admin/appearance/menus/{id}/{action}", auth.CapAppearance, s.adminMenuAction)
	admin("GET /admin/appearance/widgets", auth.CapAppearance, s.adminWidgets)
	admin("POST /admin/appearance/widgets", auth.CapAppearance, s.adminWidgetAdd)
	admin("POST /admin/appearance/widgets/{id}", auth.CapAppearance, s.adminWidgetUpdate)
	admin("POST /admin/appearance/widgets/{id}/{action}", auth.CapAppearance, s.adminWidgetAction)

	admin("GET /admin/plugins", auth.CapPlugins, s.adminPlugins)
	admin("POST /admin/plugins/reload", auth.CapPlugins, s.adminPluginsReload)
	admin("POST /admin/plugins/upload", auth.CapPlugins, s.adminPluginUpload)
	if s.limits != nil {
		admin("POST /admin/plugins/catalog/{id}/install", auth.CapPlugins, s.adminPluginCatalogInstall)
	}
	admin("GET /admin/plugins/{id}", auth.CapPlugins, s.adminPlugin)
	admin("POST /admin/plugins/{id}/approve", auth.CapPlugins, s.adminPluginApprove)
	admin("POST /admin/plugins/{id}/disable", auth.CapPlugins, s.adminPluginDisable)
	admin("POST /admin/plugins/{id}/settings", auth.CapPlugins, s.adminPluginSettings)
	admin("POST /admin/plugins/{id}/scan", auth.CapPlugins, s.adminPluginScan)
	admin("POST /admin/plugins/{id}/uninstall", auth.CapPlugins, s.adminPluginUninstall)

	admin("GET /admin/users", auth.CapManageUsers, s.adminUsers)
	admin("GET /admin/users/new", auth.CapManageUsers, s.adminUserNew)
	admin("POST /admin/users/new", auth.CapManageUsers, s.adminUserCreate)
	admin("GET /admin/users/{id}", auth.CapManageUsers, s.adminUserEdit)
	admin("POST /admin/users/{id}", auth.CapManageUsers, s.adminUserUpdate)
	admin("POST /admin/users/{id}/{action}", auth.CapManageUsers, s.adminUserAction)

	admin("GET /admin/profile", auth.CapAdminPanel, s.adminProfile)
	admin("POST /admin/profile", auth.CapAdminPanel, s.adminProfileUpdate)
	admin("POST /admin/profile/password", auth.CapAdminPanel, s.adminPasswordChange)
	admin("POST /admin/profile/sessions/revoke", auth.CapAdminPanel, s.adminRevokeSessions)
	admin("GET /admin/profile/2fa", auth.CapAdminPanel, s.admin2FA)
	admin("POST /admin/profile/2fa/enable", auth.CapAdminPanel, s.admin2FAEnable)
	admin("POST /admin/profile/2fa/disable", auth.CapAdminPanel, s.admin2FADisable)
	admin("POST /admin/profile/passkeys/begin", auth.CapAdminPanel, s.adminPasskeyBegin)
	admin("POST /admin/profile/passkeys/finish", auth.CapAdminPanel, s.adminPasskeyFinish)
	admin("POST /admin/profile/passkeys/{id}/rename", auth.CapAdminPanel, s.adminPasskeyRename)
	admin("POST /admin/profile/passkeys/{id}/delete", auth.CapAdminPanel, s.adminPasskeyDelete)

	admin("GET /admin/settings", auth.CapManageSettings, s.adminSettings)
	admin("POST /admin/settings", auth.CapManageSettings, s.adminSettingsSave)
	admin("GET /admin/audit", auth.CapAudit, s.adminAudit)
	admin("GET /admin/system", auth.CapManageSettings, s.adminSystem)
	admin("GET /admin/analytics", auth.CapEditOthersPosts, s.adminStats)
	admin("POST /admin/system/update-check", auth.CapManageSettings, s.adminUpdateCheck)

	return mux
}
