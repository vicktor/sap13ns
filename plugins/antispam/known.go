package main

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"

	"github.com/vicktor/sap13ns/pluginsdk"
)

// Known commenters are kept as hashes of their names in one key, newest
// last, one per line (17 bytes each: 8000 take 136 KB of the 256 KiB a
// value may hold).
const (
	keyKnown = "known"
	maxKnown = 8000
)

// nameKey hashes a commenter's name, folded, with the site address, so the
// store holds no names.
func nameKey(siteURL, name string) string {
	sum := sha256.Sum256([]byte(siteURL + "\x00" + Fold(name)))
	return base64.RawURLEncoding.EncodeToString(sum[:12])
}

func loadKnown() ([]string, error) {
	raw, err := pluginsdk.KVGet(keyKnown)
	switch {
	case errors.Is(err, pluginsdk.ErrNotFound):
		return nil, nil
	case err != nil:
		return nil, err
	}
	return strings.Fields(string(raw)), nil
}

// isKnown reports whether a comment by this name was approved before.
func isKnown(siteURL, name string) (bool, error) {
	if Fold(name) == "" {
		return false, nil
	}
	keys, err := loadKnown()
	if err != nil {
		return false, err
	}
	k := nameKey(siteURL, name)
	for _, x := range keys {
		if x == k {
			return true, nil
		}
	}
	return false, nil
}

// learn remembers a name, moving it to the end when it is already known;
// the oldest names are forgotten first. It reports whether the name is new.
func learn(siteURL, name string) (bool, error) {
	if Fold(name) == "" {
		return false, nil
	}
	keys, err := loadKnown()
	if err != nil {
		return false, err
	}
	k := nameKey(siteURL, name)
	isNew := true
	for i, x := range keys {
		if x == k {
			keys = append(keys[:i], keys[i+1:]...)
			isNew = false
			break
		}
	}
	keys = append(keys, k)
	if len(keys) > maxKnown {
		keys = keys[len(keys)-maxKnown:]
	}
	return isNew, pluginsdk.KVSet(keyKnown, []byte(strings.Join(keys, "\n")))
}
