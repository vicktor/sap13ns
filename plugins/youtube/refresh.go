package main

import (
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/vicktor/sap13ns/pluginsdk"
)

const (
	refreshEvery = 55 * time.Minute // a little under the hourly run
	metaEvery    = 24 * time.Hour   // names and uploads playlists (Data API)
	maxBackoff   = 24 * time.Hour
)

// due reports whether the source should be fetched now. A failing source
// waits 1, 2, 4… hours (at most a day) between attempts; a source that
// was never fetched is fetched at once.
func (r record) due(t time.Time) bool {
	if r.Fails > 0 {
		wait := time.Hour << min(r.Fails-1, 5)
		return t.Sub(time.Unix(r.Tried, 0)) >= min(wait, maxBackoff)-5*time.Minute
	}
	return r.Fetched == 0 || t.Sub(time.Unix(r.Fetched, 0)) >= refreshEvery
}

// Refresh fetches the sources that are due, the stalest first, within the
// host's four requests per call. With onlyNew (after a post is published
// or updated) it fetches only sources that never had a good fetch, so a
// gallery added to a post shows up without waiting for the hourly run.
// Without onlyNew (cron.hourly) it also frees the slots that no gallery
// used for 30 days.
func Refresh(defaults []Source, key string, onlyNew bool) {
	t := now()
	slotOf, seen, err := registered()
	if err != nil {
		pluginsdk.Log(pluginsdk.Warn, "the plugin's storage is not available: "+err.Error())
		return
	}
	var sources []string
	for _, s := range defaults {
		if k := s.Key(); k != "" && !slices.Contains(sources, k) {
			sources = append(sources, k)
		}
	}
	for s := range slotOf {
		if slices.Contains(sources, s) {
			continue
		}
		if !onlyNew && t.Sub(time.Unix(seen[s], 0)) > keepFor {
			_ = pluginsdk.KVDelete(slotOf[s])
			_ = pluginsdk.KVDelete(recordKey(s))
			pluginsdk.Log(pluginsdk.Info, s+": no gallery showed it for 30 days; forgotten")
			continue
		}
		sources = append(sources, s)
	}
	type job struct {
		src Source
		rec record
	}
	var jobs []job
	for _, s := range sources {
		src, ok := SourceFromKey(s)
		if !ok {
			continue
		}
		rec, _, err := loadRecord(s)
		if err != nil {
			pluginsdk.Log(pluginsdk.Warn, "the plugin's storage is not available: "+err.Error())
			return
		}
		rec.S = s
		if !rec.due(t) || onlyNew && rec.Fetched != 0 {
			continue
		}
		jobs = append(jobs, job{src, rec})
	}
	// The stalest first; never-fetched sources (Fetched 0) lead.
	slices.SortStableFunc(jobs, func(a, b job) int { return int(a.rec.Fetched - b.rec.Fetched) })
	c := &client{left: httpBudget, key: key}
	for _, j := range jobs {
		if c.left < cost(j.src, j.rec, key) {
			break // the next run continues
		}
		fetchInto(c, j.src, &j.rec, t)
		if err := saveRecord(j.rec); err != nil {
			pluginsdk.Log(pluginsdk.Warn, j.rec.S+": could not store the list: "+err.Error())
		}
	}
}

// cost is the number of requests a fetch of src needs at most.
func cost(src Source, rec record, key string) int {
	if key == "" {
		return 1
	}
	n := 2 // playlistItems + videos
	if rec.Meta == 0 || now().Sub(time.Unix(rec.Meta, 0)) >= metaEvery {
		n++ // channels or playlists
	}
	return n
}

// fetchInto refreshes rec. A failure keeps the last good list.
func fetchInto(c *client, src Source, rec *record, t time.Time) {
	rec.Tried = t.Unix()
	list, via, err := fetch(c, src, rec, t)
	if err != nil {
		rec.Fails++
		rec.Err = cleanText(err.Error(), 300)
		level := pluginsdk.Warn
		if rec.Fails > 1 {
			level = pluginsdk.Debug // already reported
		}
		kept := "nothing to show yet"
		if len(rec.Videos) > 0 {
			kept = "the last good list (" + strconv.Itoa(len(rec.Videos)) + " videos) stays"
		}
		pluginsdk.Log(level, describe(src)+": fetch failed ("+rec.Err+"); "+kept)
		return
	}
	if rec.Fails > 0 || rec.Fetched == 0 {
		pluginsdk.Log(pluginsdk.Info, describe(src)+": "+strconv.Itoa(len(list.Videos))+" videos from the "+viaName(via))
	}
	// Keep what this fetch could not know (durations come from the Data
	// API only, Shorts from the feed only).
	old := map[string]Video{}
	for _, v := range rec.Videos {
		old[v.ID] = v
	}
	for i, v := range list.Videos {
		if o, ok := old[v.ID]; ok {
			if v.Duration == 0 && via == "feed" {
				list.Videos[i].Duration = o.Duration
			}
			if !v.Short && via == "api" {
				list.Videos[i].Short = o.Short
			}
		}
	}
	rec.Videos, rec.Via, rec.Fetched, rec.Fails, rec.Err = list.Videos, via, t.Unix(), 0, ""
	if list.Title != "" {
		rec.Title = list.Title
	}
	if list.Author != "" {
		rec.Author = list.Author
	}
	if list.Channel != "" {
		rec.Channel = list.Channel
	}
}

func viaName(via string) string {
	if via == "api" {
		return "YouTube Data API"
	}
	return "feed"
}

func describe(s Source) string {
	switch s.Kind {
	case kindHandle:
		return "@" + s.ID
	case kindPlaylist:
		return "playlist " + s.ID
	}
	return "channel " + s.ID
}

var errNeedsKey = errors.New("a handle can only be looked up with a YouTube Data API key; set one, or use the channel id (UC…)")

// fetch gets the newest videos of src: from the Data API when a key is
// set, falling back to the feed when the API fails for another reason
// than a missing channel (an exhausted quota, a revoked key).
func fetch(c *client, src Source, rec *record, t time.Time) (List, string, error) {
	if c.key != "" {
		list, err := fetchAPI(c, src, rec, t)
		if err == nil {
			return list, "api", nil
		}
		feedID := feedSource(src, rec)
		if errors.Is(err, errNotFound) || feedID == "" || c.left == 0 {
			return List{}, "", err
		}
		pluginsdk.Log(pluginsdk.Warn, describe(src)+": the Data API failed ("+cleanText(err.Error(), 200)+"); trying the feed")
		list, ferr := c.Feed(feedKind(src), feedID)
		if ferr != nil {
			return List{}, "", errors.Join(err, ferr)
		}
		return list, "feed", nil
	}
	id := feedSource(src, rec)
	if id == "" {
		return List{}, "", errNeedsKey
	}
	list, err := c.Feed(feedKind(src), id)
	return list, "feed", err
}

// feedSource is the id the feed takes for src ("" for a handle that was
// never looked up).
func feedSource(src Source, rec *record) string {
	if src.Kind == kindHandle {
		return rec.Channel
	}
	return src.ID
}

func feedKind(src Source) string {
	if src.Kind == kindPlaylist {
		return kindPlaylist
	}
	return kindChannel
}

func fetchAPI(c *client, src Source, rec *record, t time.Time) (List, error) {
	stale := rec.Meta == 0 || t.Sub(time.Unix(rec.Meta, 0)) >= metaEvery
	list := List{Title: rec.Title, Author: rec.Author, Channel: rec.Channel}
	playlist := src.ID
	if src.Kind == kindPlaylist {
		if stale {
			title, author, err := c.Playlist(src.ID)
			if err != nil {
				return List{}, err
			}
			list.Title, list.Author, rec.Meta = title, author, t.Unix()
		}
	} else {
		if stale || rec.Uploads == "" {
			id, title, uploads, err := c.Channel(src)
			if err != nil {
				return List{}, err
			}
			list.Channel, list.Title, rec.Uploads, rec.Meta = id, title, uploads, t.Unix()
		}
		playlist = rec.Uploads
	}
	videos, err := c.Items(playlist, maxVideos)
	if err != nil {
		return List{}, err
	}
	if err := c.Details(videos); err != nil {
		// The list is still good without durations.
		pluginsdk.Log(pluginsdk.Debug, describe(src)+": no durations ("+cleanText(err.Error(), 200)+")")
	}
	list.Videos = videos
	return list, nil
}
