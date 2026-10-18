// Command releasesign makes and checks the Ed25519 signature that
// `sap13ns update` requires on a release's SHA256SUMS (see internal/update
// and docs/RELEASES.md).
//
//	go run ./scripts/releasesign keygen -out sap13ns-signing.key
//	go run ./scripts/releasesign sign -key sap13ns-signing.key dist/SHA256SUMS
//	go run ./scripts/releasesign verify -pub BASE64 dist/SHA256SUMS dist/SHA256SUMS.sig
//
// Keys are standard base64: the private key is the 64-byte Ed25519 private
// key (a 32-byte seed is accepted too), the public key 32 bytes. The
// signature file holds the base64 signature over the exact SHA256SUMS bytes
// followed by a newline.
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"sap13ns/internal/update"
)

const usage = `Usage:
  releasesign keygen [-out FILE]
        Generate a signing key pair. With -out the private key is written to
        FILE (mode 0600, never overwritten) and only the public key is
        printed; without it both are printed.
  releasesign sign -key FILE [-o SIG] SHA256SUMS
        Sign SHA256SUMS with the private key in FILE and write the base64
        signature to SIG (default SHA256SUMS.sig next to it).
  releasesign verify -pub BASE64 SHA256SUMS SHA256SUMS.sig
        Check a signature against a public key, exactly as sap13ns update does.
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
	case "sign":
		err = signCmd(args)
	case "verify":
		err = verify(args)
	case "-h", "-help", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "releasesign: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "releasesign:", err)
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
	out := fl.String("out", "", "write the private key to this file (0600) instead of printing it")
	fl.Parse(args)
	if fl.NArg() != 0 {
		return errors.New("keygen takes no arguments")
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	privB64 := base64.StdEncoding.EncodeToString(priv)
	pubB64 := base64.StdEncoding.EncodeToString(pub)
	if *out == "" {
		fmt.Printf("private key: %s\npublic key:  %s\n", privB64, pubB64)
		return nil
	}
	f, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%s already exists; refusing to overwrite a signing key", *out)
	}
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(f, privB64); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Printf("private key: written to %s (keep it secret, store a copy offline)\npublic key:  %s\n", *out, pubB64)
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
		// The second half must be the public key derived from the seed.
		if !bytes.Equal(ed25519.NewKeyFromSeed(k.Seed()), k) {
			return nil, fmt.Errorf("%s: corrupt Ed25519 private key", path)
		}
		return k, nil
	}
	return nil, fmt.Errorf("%s: key is %d bytes, want %d or %d", path, len(raw), ed25519.SeedSize, ed25519.PrivateKeySize)
}

func signCmd(args []string) error {
	fl := newFlags("sign")
	keyFile := fl.String("key", "", "file with the base64 private key")
	sigOut := fl.String("o", "", "signature file (default <SHA256SUMS>.sig)")
	fl.Parse(args)
	if *keyFile == "" || fl.NArg() != 1 {
		fl.Usage()
		os.Exit(2)
	}
	sumsPath := fl.Arg(0)
	if *sigOut == "" {
		*sigOut = sumsPath + ".sig"
	}
	key, err := readPrivateKey(*keyFile)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(sumsPath)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return fmt.Errorf("%s is empty", sumsPath)
	}
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(key, data)) + "\n"
	pub := base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey))
	// Check with the same code path sap13ns update uses.
	if err := update.VerifySignature(pub, data, []byte(sig)); err != nil {
		return fmt.Errorf("self-check failed: %w", err)
	}
	if err := os.WriteFile(*sigOut, []byte(sig), 0o644); err != nil {
		return err
	}
	fmt.Printf("signed %s -> %s (public key %s)\n", sumsPath, *sigOut, pub)
	return nil
}

func verify(args []string) error {
	fl := newFlags("verify")
	pub := fl.String("pub", "", "base64 Ed25519 public key")
	fl.Parse(args)
	if *pub == "" || fl.NArg() != 2 {
		fl.Usage()
		os.Exit(2)
	}
	data, err := os.ReadFile(fl.Arg(0))
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(fl.Arg(1))
	if err != nil {
		return err
	}
	if err := update.VerifySignature(*pub, data, sig); err != nil {
		return err
	}
	fmt.Printf("OK: %s is signed by %s\n", fl.Arg(0), strings.TrimSpace(*pub))
	return nil
}
