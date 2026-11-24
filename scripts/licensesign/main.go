// Command licensesign is the licensor's tool for licence files
// (internal/license): it makes the signing key pair and issues licences
// with the private key, which never goes into the repository, the binary
// or a server.
//
//	go run ./scripts/licensesign keygen -out ~/.config/sap13ns/license-signing.key
//	go run ./scripts/licensesign issue -key ~/.config/sap13ns/license-signing.key \
//	    -licensee "Socratex" -domain socratex.org -expires 2027-11-24 -out socratex.license
//
// The public key keygen prints goes in license.PublicKey. Check an issued
// file with `sap13ns license show -file FILE`.
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"sap13ns/internal/license"
)

const usage = `Usage:
  licensesign keygen -out FILE
        Generate the licence-signing key pair: the private key goes to FILE
        (mode 0600, never overwritten), the public key is printed for
        internal/license.PublicKey.
  licensesign issue -key FILE -licensee NAME -domain HOST [-domain HOST…]
                    -expires YYYY-MM-DD [-type white-label] -out LICENSE
        Issue a licence, valid from now until the start of the expiry day
        (UTC), for the installs whose SAP13NS_BASE_URL host is one of the
        domains (www.HOST included).
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch cmd, args := os.Args[1], os.Args[2:]; cmd {
	case "keygen":
		err = keygen(args)
	case "issue":
		err = issue(args)
	case "-h", "-help", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "licensesign: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "licensesign:", err)
		os.Exit(1)
	}
}

func newFlags(name string) *flag.FlagSet {
	fl := flag.NewFlagSet(name, flag.ExitOnError)
	fl.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	return fl
}

func keygen(args []string) error {
	fl := newFlags("keygen")
	out := fl.String("out", "", "file for the private key (0600)")
	fl.Parse(args)
	if *out == "" || fl.NArg() != 0 {
		fl.Usage()
		os.Exit(2)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	if err := writeNew(*out, []byte(base64.StdEncoding.EncodeToString(priv)+"\n")); err != nil {
		return err
	}
	fmt.Printf("private key: written to %s (keep it secret, store a copy offline)\npublic key:  %s\n",
		*out, base64.StdEncoding.EncodeToString(pub))
	return nil
}

type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(v string) error { *l = append(*l, v); return nil }

func issue(args []string) error {
	fl := newFlags("issue")
	keyFile := fl.String("key", "", "file with the base64 private key")
	licensee := fl.String("licensee", "", "who the licence is issued to")
	expires := fl.String("expires", "", "expiry date, YYYY-MM-DD (UTC): the licence ends as that day starts")
	typ := fl.String("type", license.TypeWhiteLabel, "licence type")
	out := fl.String("out", "", "licence file to write (never overwritten)")
	var domains list
	fl.Var(&domains, "domain", "a host the licence is valid for (repeat for more)")
	fl.Parse(args)
	if *keyFile == "" || *out == "" || fl.NArg() != 0 {
		fl.Usage()
		os.Exit(2)
	}
	priv, err := readPrivateKey(*keyFile)
	if err != nil {
		return err
	}
	pub, err := license.DecodePublicKey(license.PublicKey)
	if err != nil {
		return err
	}
	if !bytes.Equal(priv.Public().(ed25519.PublicKey), pub) {
		return errors.New("this private key does not match internal/license.PublicKey: sap13ns would refuse the licence")
	}
	exp, err := time.Parse("2006-01-02", *expires)
	if err != nil {
		return fmt.Errorf("-expires: want YYYY-MM-DD: %w", err)
	}
	id := make([]byte, 8)
	rand.Read(id)
	l := &license.License{
		ID:       hex.EncodeToString(id),
		Type:     *typ,
		Licensee: strings.TrimSpace(*licensee),
		Issued:   time.Now().UTC().Truncate(time.Second),
		Expires:  exp.UTC(),
	}
	for _, d := range domains {
		l.Domains = append(l.Domains, strings.TrimSuffix(strings.ToLower(strings.TrimSpace(d)), "."))
	}
	data, err := license.Encode(l, priv)
	if err != nil {
		return err
	}
	if _, err := license.Parse(data, nil); err != nil {
		return fmt.Errorf("the issued licence does not verify: %w", err)
	}
	if err := writeNew(*out, data); err != nil {
		return err
	}
	fmt.Printf("%s: licence %s (%s) for %s, domains %s, until %s\n", *out, l.ID, l.Type, l.Licensee,
		strings.Join(l.Domains, ", "), l.Expires.Format("2006-01-02"))
	return nil
}

// readPrivateKey reads a base64 Ed25519 private key (64 bytes) or seed (32).
func readPrivateKey(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil {
		return nil, fmt.Errorf("%s: not a base64 key: %w", path, err)
	}
	switch len(raw) {
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(raw), nil
	case ed25519.PrivateKeySize:
		k := ed25519.PrivateKey(raw)
		if !bytes.Equal(ed25519.NewKeyFromSeed(k.Seed()), k) {
			return nil, fmt.Errorf("%s: corrupt Ed25519 private key", path)
		}
		return k, nil
	}
	return nil, fmt.Errorf("%s: key is %d bytes, want %d or %d", path, len(raw), ed25519.SeedSize, ed25519.PrivateKeySize)
}

// writeNew writes data to a file that must not exist yet, mode 0600.
func writeNew(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%s already exists; refusing to overwrite it", path)
	}
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
