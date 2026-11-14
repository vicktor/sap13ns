// Package transfer moves one site, whole, to another install of sap13ns:
// a site of a network becomes a single-site install of its own on another
// server, or a single site moves to a new server.
//
// Unlike a site backup (internal/sitebackup), which leaves every secret out
// and restores content into a running site, a transfer archive is the site
// itself: every row of every table of its schema (users with their
// password hashes, second factors and passkeys, plugin data and secrets,
// the AI, assistant and SMTP settings with their keys, analytics, the
// audit log), its uploads, plugins and themes, the themes of the network
// it could use, and the site's secret key, which opens what its tables
// hold encrypted. The SMTP server it sent through, the network's or the
// server's, travels too, so that the new install sends mail at once.
//
// The archive is a tar.gz, always encrypted with a passphrase in the
// format of the backups (internal/backup):
//
//	db/<table>.copy    the rows of a table in COPY text format, with the
//	                   columns the manifest lists for it
//	files/uploads/...  the uploaded files
//	files/plugins/...  the installed plugins
//	files/themes/...   the site's themes and those of its network
//	manifest.json      written last: format, source, secret key, SMTP,
//	                   the migrations of the schema, and the size and
//	                   SHA-256 of every other member
//
// The import (Open, then Staged.Apply) runs on a single-site install. It
// recreates the schema at the migrations the site had, loads every table
// in one transaction, clears what tied the site to its network (the
// accounts linked to the portal, the sessions), puts the files in place,
// and leaves the newer migrations of the new binary to its next start.
package transfer

import (
	"errors"
	"time"

	"sap13ns/internal/backup"
)

// FormatVersion is the version of the archive layout (manifest "format").
const FormatVersion = 1

// Members and prefixes of an archive.
const (
	NameManifest = "manifest.json"
	dirDB        = "db/"
	dirFiles     = "files/"
	copySuffix   = ".copy"
)

// The directories of a site that an archive carries, under files/.
const (
	DirUploads = "uploads"
	DirPlugins = "plugins"
	DirThemes  = "themes"
)

var fileDirs = []string{DirUploads, DirPlugins, DirThemes}

// Limits enforced when an archive is read.
var (
	maxMembers       = 1_000_000
	maxManifestBytes = int64(256 << 20)
	maxMemberBytes   = int64(1 << 40)
)

var (
	// ErrNotEmpty: the database of the install already has a site.
	ErrNotEmpty = errors.New("transfer: the database already holds a site")
	// ErrCorrupt: the archive does not match its manifest.
	ErrCorrupt = errors.New("transfer: the archive is damaged")
	// ErrNewer: the archive comes from a newer sap13ns than this one.
	ErrNewer = errors.New("transfer: the archive was made by a newer sap13ns")
)

// Manifest describes an archive.
type Manifest struct {
	Format     int       `json:"format"`
	AppVersion string    `json:"app_version"`
	Created    time.Time `json:"created"`
	Source     Source    `json:"source"`
	// SecretKey is the site's key (hex): the SAP13NS_SECRET_KEY of the new
	// install. It decrypts the second factors, the SMTP, AI and plugin
	// secrets in the tables.
	SecretKey string `json:"secret_key"`
	// SMTP is the server the site sent mail through when it has none of
	// its own (Settings → E-mail): the network's or the server's.
	SMTP *SMTP `json:"smtp,omitempty"`
	// NetworkAI is the network's AI provider, carried only when asked for
	// (a site whose AI came from its network's plan).
	NetworkAI *NetworkAI `json:"network_ai,omitempty"`
	// Migrations are the site migrations its schema had.
	Migrations []int  `json:"migrations"`
	PGVersion  string `json:"postgres_version"`
	// Embedding is the type of the assistant's embedding column, such as
	// "vector(768)", "" without one.
	Embedding     string          `json:"embedding,omitempty"`
	Tables        []Table         `json:"tables"`
	Files         []backup.Member `json:"files"`
	NetworkThemes []string        `json:"network_themes,omitempty"`
	TotalBytes    int64           `json:"total_bytes"`
}

// Source is where the site comes from.
type Source struct {
	URL     string   `json:"url"`
	Title   string   `json:"title"`
	Label   string   `json:"label"`             // the slug on a network, the host otherwise
	Network string   `json:"network,omitempty"` // the host of the network
	SiteID  int64    `json:"site_id,omitempty"`
	Domains []string `json:"domains,omitempty"` // primary first
	RPID    string   `json:"passkey_rp_id"`     // passkeys work only under this host
}

// SMTP is a mail server; the password is in clear inside the encrypted
// archive.
type SMTP struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	From     string `json:"from"`
}

// NetworkAI is the AI provider of a network (config.NetworkAI).
type NetworkAI struct {
	Provider      string `json:"provider"`
	APIKey        string `json:"api_key"`
	BaseURL       string `json:"base_url,omitempty"`
	TextModel     string `json:"text_model,omitempty"`
	ImageProvider string `json:"image_provider,omitempty"`
	ImageAPIKey   string `json:"image_api_key,omitempty"`
	ImageBaseURL  string `json:"image_base_url,omitempty"`
	ImageModel    string `json:"image_model,omitempty"`
}

// Table is one table of the schema.
type Table struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Rows    int64    `json:"rows"`
	// Data is the member db/<name>.copy.
	Data backup.Member `json:"data"`
}
