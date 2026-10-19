package sitebackup

import (
	"strings"
	"time"
)

// readme is README.txt: what the archive holds and how to use it, in
// WordPress or in sap13ns. It is plain English, as WordPress's own files
// are; the admin pages and docs/MOVING-TO-WORDPRESS.md carry the
// translated and longer versions.
func readme(m *Manifest) string {
	var b strings.Builder
	w := func(s ...string) {
		for _, l := range s {
			b.WriteString(l)
			b.WriteByte('\n')
		}
	}
	w("Backup of "+m.Site.URL+" ("+m.Site.Title+")",
		"Made by sap13ns "+m.AppVersion+" on "+m.Created.UTC().Format(time.DateTime)+" UTC.",
		"",
		"This archive works in WordPress as it is, and restores the site in sap13ns.",
		"",
		"WHAT IS INSIDE",
		"",
		"  export.xml            WordPress export file (WXR 1.2): posts, pages, media,",
		"                        comments, authors, categories, tags and menus",
		"  wp-content/uploads/   the uploaded files, laid out as WordPress keeps them",
		"  sap13ns/              what only sap13ns uses (settings, users and roles,",
		"                        widgets, designs, layouts, redirects, plugins);",
		"                        WordPress ignores it",
		"",
		"It holds no passwords, two-factor secrets, passkeys or API keys. It does hold",
		"personal data: the e-mail addresses of users and commenters, and the IP",
		"addresses of commenters. Keep it as safe as the site itself.",
		"",
		"MOVING TO WORDPRESS",
		"",
	)
	if m.WPBase != "" {
		w("This archive was made for "+m.WPBase+": its media addresses point there.",
			"",
			"  1. Install WordPress at "+m.WPBase+" and sign in as an administrator.",
			"  2. Copy the folder wp-content/uploads of this archive into the",
			"     wp-content/uploads folder of WordPress (SFTP, or your host's file",
			"     manager), keeping the year/month folders.",
			"  3. Tools > Import > WordPress: install the importer if asked, then",
			"     \"Run Importer\".",
			"  4. Choose export.xml, assign or create the authors, tick \"Download and",
			"     import file attachments\", and submit.",
			"  5. Appearance > Menus: assign each imported menu to a location of your",
			"     theme.",
			"",
			"WordPress copies each file once more under a new name (photo-1.jpg) and",
			"points the posts at the copy; the files of step 2 can then be deleted.",
		)
	} else {
		w("The media addresses in this archive point at "+m.Site.URL+", so that site",
			"must still be online while WordPress imports.",
			"",
			"  1. Install WordPress and sign in as an administrator.",
			"  2. Tools > Import > WordPress: install the importer if asked, then",
			"     \"Run Importer\".",
			"  3. Choose export.xml, assign or create the authors, tick \"Download and",
			"     import file attachments\", and submit. WordPress downloads every file",
			"     from the old site.",
			"  4. Appearance > Menus: assign each imported menu to a location of your",
			"     theme.",
			"",
			"If the old site will be gone, make the backup with the address of the new",
			"WordPress site instead (\"Move to WordPress\" on the Backups page, or",
			"sap13ns backup create -site SITE -wp-base https://new.example).",
		)
	}
	w("",
		"With WP-CLI: wp plugin install wordpress-importer --activate, then",
		"wp import export.xml --authors=create.",
		"",
		"Imported users have no password: they set one with \"Lost your password?\".",
		"",
		"RESTORING IN SAP13NS",
		"",
		"  Admin > Tools > Backups lists the backups of the site; Restore replaces its",
		"  content with one of them after a safety backup. From the command line:",
		"",
		"    sap13ns backup restore -site SITE -replace THIS-FILE.zip",
		"",
		"  (without -site on a single site). Restored users keep the password they",
		"  have on the site; users the site does not have yet set one with \"Forgot",
		"  your password?\", or sign in through the network.",
		"",
		"sap13ns/manifest.json lists the SHA-256 of every file of the archive.",
	)
	return b.String()
}
