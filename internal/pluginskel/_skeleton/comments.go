package main

import (
	"strconv"
	"strings"

	"github.com/vicktor/sap13ns/pluginsdk"
)

// comment.check runs before a visitor's comment is stored. Answer "pending"
// to send it to moderation or "spam" to file it as spam; the reason is
// written to this plugin's activity log, never shown to the visitor.
func init() {
	commentChecks = append(commentChecks, func(req *pluginsdk.Request, c pluginsdk.CommentCheck) (string, string) {
		limit, err := strconv.Atoi(req.Setting("max_links"))
		if err != nil {
			limit = 3
		}
		return CheckLinks(c.Content, limit)
	})
}

// CheckLinks holds comments with more than limit links.
func CheckLinks(content string, limit int) (verdict, reason string) {
	n := strings.Count(content, "http://") + strings.Count(content, "https://")
	if n > limit {
		return pluginsdk.VerdictPending, strconv.Itoa(n) + " links"
	}
	return pluginsdk.VerdictOK, ""
}
