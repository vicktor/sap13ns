package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"sap13ns/internal/auth"
	"sap13ns/internal/content"
	"sap13ns/internal/netguard"
	"sap13ns/internal/store"
	"sap13ns/internal/webpush"
)

// Notifications of new posts (Web Push). With Settings → Site →
// "Offer notifications of new posts", and once the site has a published
// post, the foot of every page offers "Notify me of new posts". The
// browser asks its own permission only after that click; what it then
// gives the site (the address of its push service and two keys, no name
// or e-mail) is kept in push_subscriptions. When a post goes live, the
// site sends its title, summary and address to each one, encrypted for
// that browser (internal/webpush), through the push service the browser
// chose: the services of Google, Mozilla, Apple and Microsoft, and no
// other host (webpush.Hosts, behind the SSRF guard).

// pushState is the site's key pair and whether it has posts, cached.
type pushState struct {
	mu        sync.Mutex
	priv, pub []byte
	posts     atomic.Int32 // 0 not known, 1 the site has published posts, 2 none
	limiter   *auth.Limiter
	client    *http.Client // tests; nil uses pushClient
}

// pushClient reaches push services: public addresses only, https, no
// redirect followed.
var pushClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		DialContext:           netguard.Dialer(nil, nil, 5*time.Second),
		Proxy:                 nil,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       30 * time.Second,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// pushOn reports whether the site offers notifications now: the setting,
// and a published post to tell about.
func (s *Server) pushOn() bool {
	if !s.settings().Push || s.store == nil {
		return false
	}
	switch s.push.posts.Load() {
	case 1:
		return true
	case 2:
		return false
	}
	ok, err := s.store.HasPublishedPosts(context.Background())
	if err != nil {
		return false
	}
	if ok {
		s.push.posts.Store(1)
	} else {
		s.push.posts.Store(2)
	}
	return ok
}

// pushKeys returns the site's VAPID keys, made on first use.
func (s *Server) pushKeys(ctx context.Context) (private, public []byte, err error) {
	p := &s.push
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.priv != nil {
		return p.priv, p.pub, nil
	}
	enc, pub, err := s.store.PushKeys(ctx)
	if err != nil {
		return nil, nil, err
	}
	if enc == nil {
		priv, pub, err := webpush.GenerateKey()
		if err != nil {
			return nil, nil, err
		}
		sealed, err := s.keys.Encrypt(priv, []byte("push-vapid"))
		if err != nil {
			return nil, nil, err
		}
		if err := s.store.InitPushKeys(ctx, sealed, pub); err != nil {
			return nil, nil, err
		}
		// Another process may have been first: its pair is the site's.
		if enc, pub, err = s.store.PushKeys(ctx); err != nil || enc == nil {
			return nil, nil, errors.Join(err, errors.New("push keys were not kept"))
		}
		_ = pub
	}
	if enc, pub, err = s.store.PushKeys(ctx); err != nil {
		return nil, nil, err
	}
	priv, err := s.keys.Decrypt(enc, []byte("push-vapid"))
	if err != nil {
		return nil, nil, err
	}
	p.priv, p.pub = priv, pub
	return priv, pub, nil
}

// PushView is what shared/head gives push.js.
type PushView struct {
	Key string // the site's public key, base64url
}

func (s *Server) pushView() *PushView {
	if !s.pushOn() {
		return nil
	}
	_, pub, err := s.pushKeys(context.Background())
	if err != nil {
		s.log.Error("push keys", "err", err)
		return nil
	}
	return &PushView{Key: base64.RawURLEncoding.EncodeToString(pub)}
}

func (s *Server) pushLimiter() *auth.Limiter {
	s.push.mu.Lock()
	defer s.push.mu.Unlock()
	if s.push.limiter == nil {
		s.push.limiter = auth.NewLimiter(30, time.Hour)
	}
	return s.push.limiter
}

func pushBytes(v string) []byte {
	b, _ := base64.RawURLEncoding.DecodeString(strings.TrimRight(v, "="))
	return b
}

// pushBody reads the JSON body of a push request into v. JSON only: a
// form of another site cannot send it.
func (s *Server) pushBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		http.Error(w, "application/json only", http.StatusUnsupportedMediaType)
		return false
	}
	if !s.pushLimiter().Allow(ipKey(clientIP(r))) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(v); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return false
	}
	return true
}

// apiPushSubscribe keeps the subscription a browser made (POST
// /api/push/subscribe).
func (s *Server) apiPushSubscribe(w http.ResponseWriter, r *http.Request) {
	if !s.pushOn() {
		http.NotFound(w, r)
		return
	}
	var in struct {
		Endpoint string `json:"endpoint"`
		Keys     struct {
			P256dh string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
		Lang string `json:"lang"`
	}
	if !s.pushBody(w, r, &in) {
		return
	}
	sub := store.PushSubscription{Endpoint: in.Endpoint, P256dh: pushBytes(in.Keys.P256dh), Auth: pushBytes(in.Keys.Auth), Lang: in.Lang}
	if !webpush.ValidEndpoint(sub.Endpoint) || !webpush.ValidKeys(sub.P256dh, sub.Auth) {
		http.Error(w, "not a push subscription", http.StatusBadRequest)
		return
	}
	if !slices.Contains(s.siteLangs(), sub.Lang) {
		sub.Lang = s.lang()
	}
	ok, err := s.store.SavePushSubscription(r.Context(), sub)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !ok {
		http.Error(w, "no room for more subscriptions", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// apiPushUnsubscribe forgets a subscription (POST /api/push/unsubscribe).
// Whoever knows the endpoint is the browser it belongs to.
func (s *Server) apiPushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Endpoint string `json:"endpoint"`
	}
	if !s.pushBody(w, r, &in) {
		return
	}
	if webpush.ValidEndpoint(in.Endpoint) {
		if err := s.store.DeletePushSubscription(r.Context(), in.Endpoint); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// pushPublished tells the subscribed browsers that p went live: those of
// its language on a site with several, all of them otherwise. It runs in
// the background; a subscription its service no longer knows is forgotten.
func (s *Server) pushPublished(p *store.Post) {
	if p.Type != store.TypePost || !s.pushOn() {
		return
	}
	st := s.settings()
	icon := siteIconURL(st.SiteIcon, 192)
	if icon == "" {
		icon = s.assetURL("brand/icon-192.png")
	}
	payload, _ := json.Marshal(map[string]string{
		"title": p.Title, "body": content.Summary(p.Excerpt, p.Content, 140), "url": s.postURL(p),
		"icon": icon, "tag": "post-" + p.Slug, "lang": p.Lang,
	})
	var langs []string
	if s.multilingual() {
		langs = []string{p.Lang}
	}
	subject := s.cfg.AbsURL("/")
	s.goJob(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		priv, _, err := s.pushKeys(ctx)
		if err != nil {
			s.log.Error("push keys", "err", err)
			return
		}
		client := s.push.client
		if client == nil {
			client = pushClient
		}
		var sent, gone, failed atomic.Int64
		work := make(chan store.PushSubscription)
		var wg sync.WaitGroup
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for sub := range work {
					err := webpush.Send(ctx, client, webpush.Subscription{Endpoint: sub.Endpoint, P256dh: sub.P256dh, Auth: sub.Auth}, payload, subject, priv, 24*time.Hour)
					switch {
					case err == nil:
						sent.Add(1)
					case errors.Is(err, webpush.Gone):
						gone.Add(1)
						_ = s.store.DeletePushSubscription(ctx, sub.Endpoint)
					default:
						failed.Add(1)
					}
				}
			}()
		}
		var after int64
		for ctx.Err() == nil {
			subs, err := s.store.PushSubscriptions(ctx, after, 500, langs)
			if err != nil {
				s.log.Error("push subscriptions", "err", err)
				break
			}
			if len(subs) == 0 {
				break
			}
			for _, sub := range subs {
				after = sub.ID
				work <- sub
			}
		}
		close(work)
		wg.Wait()
		if n := sent.Load() + gone.Load() + failed.Load(); n > 0 {
			s.log.Info("new post notified", "post", p.ID, "sent", sent.Load(), "gone", gone.Load(), "failed", failed.Load())
		}
	})
}
