package auth

// Capabilities follow the WordPress role model, simplified.
type Cap string

const (
	CapRead            Cap = "read"
	CapComment         Cap = "comment"
	CapEditOwnPosts    Cap = "posts.edit_own"
	CapPublishOwnPosts Cap = "posts.publish_own"
	CapEditOthersPosts Cap = "posts.edit_others"
	CapManagePages     Cap = "pages.manage"
	CapManageTerms     Cap = "terms.manage"
	CapUploadMedia     Cap = "media.upload"
	CapManageMedia     Cap = "media.manage"
	CapModerate        Cap = "comments.moderate"
	CapManageUsers     Cap = "users.manage"
	CapManageSettings  Cap = "settings.manage"
	CapAppearance      Cap = "appearance.manage"
	CapPlugins         Cap = "plugins.manage"
	CapAudit           Cap = "audit.view"
	CapAdminPanel      Cap = "admin.access"
)

const (
	RoleAdmin       = "admin"
	RoleEditor      = "editor"
	RoleAuthor      = "author"
	RoleContributor = "contributor"
	RoleSubscriber  = "subscriber"
)

// Roles lists roles from most to least privileged.
var Roles = []string{RoleAdmin, RoleEditor, RoleAuthor, RoleContributor, RoleSubscriber}

var roleCaps = map[string][]Cap{
	RoleSubscriber:  {CapRead, CapComment, CapAdminPanel},
	RoleContributor: {CapRead, CapComment, CapAdminPanel, CapEditOwnPosts},
	RoleAuthor:      {CapRead, CapComment, CapAdminPanel, CapEditOwnPosts, CapPublishOwnPosts, CapUploadMedia},
	RoleEditor: {CapRead, CapComment, CapAdminPanel, CapEditOwnPosts, CapPublishOwnPosts, CapUploadMedia,
		CapEditOthersPosts, CapManagePages, CapManageTerms, CapManageMedia, CapModerate},
}

func init() {
	all := append([]Cap{}, roleCaps[RoleEditor]...)
	all = append(all, CapManageUsers, CapManageSettings, CapAppearance, CapPlugins, CapAudit)
	roleCaps[RoleAdmin] = all
}

func ValidRole(role string) bool {
	_, ok := roleCaps[role]
	return ok
}

func Can(role string, c Cap) bool {
	for _, have := range roleCaps[role] {
		if have == c {
			return true
		}
	}
	return false
}

// RoleRank orders roles; higher is more privileged.
func RoleRank(role string) int {
	for i, r := range Roles {
		if r == role {
			return len(Roles) - i
		}
	}
	return 0
}
