package backup

import (
	"bufio"
	"bytes"
	"io"
	"time"
	"unicode/utf8"
)

// The encryption and the retention schedule of the database archives,
// for the other kinds of backup (internal/sitebackup): a per-site archive
// is encrypted in the same format (crypt.go) and pruned on the same
// grandfather-father-son schedule.

// EncryptedSuffix ends the name of an encrypted file.
const EncryptedSuffix = ".enc"

// MinPassphraseLen is the shortest passphrase an archive accepts.
const MinPassphraseLen = minPassphraseLen

// CheckPassphrase returns ErrWeakPassphrase for a passphrase that is too
// short to protect an archive.
func CheckPassphrase(p string) error {
	if utf8.RuneCountInString(p) < minPassphraseLen {
		return ErrWeakPassphrase
	}
	return nil
}

// Encrypt returns a writer that seals what it is given to w in the format
// of crypt.go, with a fresh salt and key. Close writes the final chunk; it
// does not close w. Readers must use Decrypt.
func Encrypt(w io.Writer, passphrase string) (io.WriteCloser, error) {
	if err := CheckPassphrase(passphrase); err != nil {
		return nil, err
	}
	return newEncrypter(w, passphrase, defaultKDF)
}

// Decrypt checks the header of an encrypted stream and returns a reader of
// its plaintext. A wrong passphrase is ErrWrongPassphrase, before anything
// is decrypted. The reader returns io.EOF only once the final chunk has
// been authenticated and nothing follows it, and an error wrapping
// ErrCorrupt for a stream that was modified, reordered or cut; callers
// must read it to the end before trusting what they read.
func Decrypt(r io.Reader, passphrase string) (io.Reader, error) {
	return newDecrypter(r, passphrase)
}

// IsEncrypted reports whether r starts with the magic of an encrypted
// stream, without consuming it.
func IsEncrypted(r *bufio.Reader) bool {
	b, _ := r.Peek(len(encMagic))
	return bytes.Equal(b, []byte(encMagic))
}

// Keep marks which of a list of backups a grandfather-father-son schedule
// keeps, as Prune does for database archives: the newest backup of each of
// the daily most recent days that have one, likewise for ISO weeks and
// calendar months (UTC), and always the newest. created must be sorted
// newest first.
func Keep(created []time.Time, daily, weekly, monthly int) []bool {
	all := make([]Entry, len(created))
	for i, t := range created {
		all[i].Created = t.UTC()
	}
	return retain(all, max(daily, 0), max(weekly, 0), max(monthly, 0))
}

// SafeName reports whether name is a canonical, relative, slash-separated
// path that cannot climb out of the directory it is extracted to: what
// archives accept as member names.
func SafeName(name string) bool { return safeName(name) }
