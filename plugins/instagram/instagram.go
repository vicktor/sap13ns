package main

import (
	"encoding/json"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/vicktor/sap13ns/pluginsdk"
	"github.com/vicktor/sap13ns/pluginsdk/autopost"
)

// graphBase is the Instagram API with Instagram Login (Graph API
// version; keep plugin.json's userinfo_url in sync). Meta supports each
// version for about two years.
const (
	graphVersion = "v26.0"
	graphBase    = "https://graph.instagram.com/" + graphVersion + "/"
)

// publishCost is the requests of one post: the container, its status and
// media_publish.
const publishCost = 3

// The waits for Instagram to process the image of a container. Meta
// processes a JPEG in seconds; a container that is still not ready is
// published on a later run (a new container, which costs nothing).
var (
	processWait = 2 * time.Second
	retryWait   = 3 * time.Second
)

// Caption limits: 2200 characters, 30 hashtags, 20 @ tags.
const (
	captionMax  = 2200
	maxTags     = 30
	maxMentions = 20
)

var (
	tagRe     = regexp.MustCompile(`(?:^|\s)#[\p{L}\p{M}\p{N}_]+`)
	mentionRe = regexp.MustCompile(`(?:^|[^\p{L}\p{N}_.@])@[A-Za-z0-9._]+`)
)

// captionLen measures a caption in UTF-16 units (an upper bound of the
// characters Instagram counts), and over the limit when it has too many
// hashtags or @ tags, so that Compose drops some.
func captionLen(s string) int {
	if len(tagRe.FindAllString(s, -1)) > maxTags || len(mentionRe.FindAllString(s, -1)) > maxMentions {
		return captionMax + 1
	}
	return len(utf16.Encode([]rune(s)))
}

var captionLimit = autopost.Limit{Max: captionMax, Len: captionLen}

// skipPost explains why a post cannot be published to Instagram: its
// posts need an image, and the Content Publishing API takes only JPEG.
func skipPost(p pluginsdk.Post) string {
	if strings.TrimSpace(p.Image) == "" {
		return "it has no featured image (an Instagram post needs one)"
	}
	u, err := url.Parse(p.Image)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "its featured image has no public address"
	}
	switch ext := strings.ToLower(path.Ext(u.Path)); ext {
	case ".jpg", ".jpeg":
		return ""
	case "":
		return "its featured image is not a JPEG; Instagram publishes only JPEG images"
	default:
		return "its featured image is a " + strings.ToUpper(ext[1:]) + " file; Instagram publishes only JPEG images"
	}
}

// graphRequest builds a request to the Graph API with the token in the
// Authorization header, as Meta's examples send it.
func graphRequest(method, endpoint, token string, body any, timeout int) pluginsdk.HTTPRequest {
	r := pluginsdk.HTTPRequest{Method: method, URL: endpoint, TimeoutMS: timeout, Headers: map[string]string{
		"Authorization": "Bearer " + token,
		"Accept":        "application/json",
	}}
	if body != nil {
		b, _ := json.Marshal(body)
		r.Body = string(b)
		r.Headers["Content-Type"] = "application/json"
	}
	return r
}

// Container is the body of POST /<IG_ID>/media for a single image.
type Container struct {
	ImageURL string `json:"image_url"`
	Caption  string `json:"caption,omitempty"`
}

// publish posts the featured image with the caption: a container, its
// status, then media_publish.
func publish(conn connection, p pluginsdk.Post, caption string) autopost.Result {
	base := graphBase + url.PathEscape(conn.account)
	resp, err := pluginsdk.Fetch(graphRequest("POST", base+"/media", conn.token, Container{ImageURL: p.Image, Caption: caption}, 6000))
	if r, bad := graphProblem(resp, err); bad {
		r.Detail = "creating the media container: " + r.Detail
		if !r.Done && !r.Failed {
			// No answer to the container: nothing can have been published.
			r.Failed = true
		}
		return r
	}
	var created struct {
		ID string `json:"id"`
	}
	if json.Unmarshal([]byte(resp.Body), &created) != nil || created.ID == "" {
		return autopost.Result{Failed: true, Detail: "Instagram did not return a media container id"}
	}

	sleep(processWait)
	switch state, detail := containerStatus(conn, created.ID); state {
	case "FINISHED":
	case "ERROR":
		return autopost.Result{Failed: true, Detail: "Instagram could not process the featured image (" + cmpOr(detail, "status ERROR") + ")" + imageHint}
	case "EXPIRED":
		return autopost.Result{Failed: true, Detail: "the media container expired before it was published"}
	case "PUBLISHED":
		return autopost.Result{Done: true, URL: "media container " + created.ID + " (already published)", Link: profile(conn.name)}
	default: // IN_PROGRESS, or the status could not be read
		sleep(retryWait)
	}

	resp, err = pluginsdk.Fetch(graphRequest("POST", base+"/media_publish", conn.token, map[string]string{"creation_id": created.ID}, 0))
	if r, bad := graphProblem(resp, err); bad {
		if r.Failed && r.Later.IsZero() && metaCodes(resp).Subcode == 2207027 {
			r.Later = now().Add(10 * time.Minute)
			r.Detail = "Instagram is still processing the featured image (" + strings.TrimPrefix(r.Detail, "HTTP 400: ") + ")"
		}
		return r
	}
	var posted struct {
		ID string `json:"id"`
	}
	json.Unmarshal([]byte(resp.Body), &posted)
	if posted.ID == "" {
		return autopost.Result{Done: true, URL: "(Instagram did not return the media id)", Link: profile(conn.name)}
	}
	return autopost.Result{Done: true, URL: "media " + posted.ID, Link: profile(conn.name)}
}

// containerStatus reads status_code (and status, which explains an
// ERROR). It returns "" when the status could not be read.
func containerStatus(conn connection, id string) (state, detail string) {
	resp, err := pluginsdk.Fetch(graphRequest("GET", graphBase+url.PathEscape(id)+"?fields=status_code,status", conn.token, nil, 3000))
	if err != nil || resp == nil || resp.Status < 200 || resp.Status > 299 {
		return "", ""
	}
	var s struct {
		StatusCode string `json:"status_code"`
		Status     string `json:"status"`
	}
	json.Unmarshal([]byte(resp.Body), &s)
	return s.StatusCode, autopost.Shorten(autopost.OneLine(s.Status), 160)
}

func profile(name string) string {
	if !usernameRe.MatchString(name) {
		return ""
	}
	return "https://www.instagram.com/" + name + "/"
}

var usernameRe = regexp.MustCompile(`^[A-Za-z0-9._]{1,30}$`)

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// meta is the error code of a Graph API answer.
type meta struct{ Code, Subcode int }

func metaCodes(resp *pluginsdk.HTTPResponse) meta {
	if resp == nil {
		return meta{}
	}
	var e struct {
		Error struct {
			Code    int `json:"code"`
			Subcode int `json:"error_subcode"`
		} `json:"error"`
	}
	json.Unmarshal([]byte(resp.Body), &e)
	return meta{e.Error.Code, e.Error.Subcode}
}

const imageHint = ". The image must be a public JPEG of at most 8 MB, between 4:5 (portrait) and 1.91:1 (landscape)"

// graphProblem classifies a Graph API answer (see autopost.FetchProblem)
// and adds what Meta's error codes mean. Rate limits wait an hour, and do
// not count as attempts.
func graphProblem(resp *pluginsdk.HTTPResponse, err error) (autopost.Result, bool) {
	r, bad := autopost.FetchProblem(resp, err, graphBase, "")
	if !bad || resp == nil || err != nil {
		return r, bad
	}
	m := metaCodes(resp)
	switch {
	case resp.Status == 429, m.Code == 4 && m.Subcode != 2207051, m.Code == 17, m.Code == 32, m.Code == 613:
		r.Later = now().Add(time.Hour)
		r.Detail = "rate limited (" + r.Detail + ")"
	case m.Subcode == 2207042:
		r.Later = now().Add(time.Hour)
		r.Detail = "the account reached Instagram's limit of published posts in 24 hours (" + r.Detail + ")"
	default:
		r.Detail += hint(m)
	}
	return r, bad
}

// hint explains the refusals an administrator can fix (Meta's error codes
// for the Instagram Platform).
func hint(m meta) string {
	switch {
	case m.Code == 190:
		return ". The token is no longer valid: connect Instagram again on the plugin's page"
	case m.Code == 10 || m.Code == 200 || m.Code == 3:
		return ". The connection lacks instagram_business_content_publish, or the account is not a professional account: check it, then connect again"
	case m.Subcode == 2207052, m.Subcode == 2207003:
		return ". Instagram could not download the featured image: the site must be reachable from the internet" + imageHint
	case m.Subcode == 2207004, m.Subcode == 2207005, m.Subcode == 2207009:
		return imageHint
	case m.Subcode == 2207010, m.Subcode == 2207040:
		return ". The caption is over Instagram's limits (2200 characters, 30 hashtags, 20 @ tags): shorten the caption template"
	case m.Subcode == 2207050:
		return ". The Instagram account is inactive or restricted: sign in to Instagram to check it"
	case m.Subcode == 2207051:
		return ". Instagram flagged the activity as spam and restricted it"
	}
	return ""
}

// itoa is strconv.Itoa, for the log lines.
func itoa(n int) string { return strconv.Itoa(n) }
