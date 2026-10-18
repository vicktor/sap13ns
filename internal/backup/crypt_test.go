package backup

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"testing"
	"testing/iotest"
)

// testKDF keeps Argon2id cheap in tests; the header records it, so readers
// need nothing special.
var testKDF = kdfParams{Time: 1, Memory: 256, Threads: 1}

const testPassphrase = "correct horse battery staple"

func encrypt(t *testing.T, plain []byte, pass string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := newEncrypter(&buf, pass, testKDF)
	if err != nil {
		t.Fatal(err)
	}
	// Odd write sizes exercise the chunk boundaries.
	for p := plain; len(p) > 0; {
		n := min(len(p), 7919)
		if _, err := w.Write(p[:n]); err != nil {
			t.Fatal(err)
		}
		p = p[n:]
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func decrypt(enc []byte, pass string) ([]byte, error) {
	r, err := newDecrypter(bytes.NewReader(enc), pass)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

func TestEncryptRoundTrip(t *testing.T) {
	for _, n := range []int{0, 1, chunkSize - 1, chunkSize, chunkSize + 1, 3 * chunkSize, 3*chunkSize + 123} {
		plain := randBytes(n)
		enc := encrypt(t, plain, testPassphrase)
		chunks := max(1, (n+chunkSize-1)/chunkSize)
		if want := headerSize + n + chunks*tagSize; len(enc) != want {
			t.Errorf("n=%d: ciphertext is %d bytes, want %d", n, len(enc), want)
		}
		got, err := decrypt(enc, testPassphrase)
		if err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		if !bytes.Equal(got, plain) {
			t.Fatalf("n=%d: round trip mismatch", n)
		}
		// Reading one byte at a time takes the same path.
		r, err := newDecrypter(iotest.OneByteReader(bytes.NewReader(enc)), testPassphrase)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := io.ReadAll(r); err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("n=%d: one-byte reader: %v", n, err)
		}
	}
}

func TestEncryptIsRandomized(t *testing.T) {
	plain := []byte("same plaintext, same passphrase")
	a, b := encrypt(t, plain, testPassphrase), encrypt(t, plain, testPassphrase)
	if bytes.Equal(a[saltOffset:saltOffset+saltSize], b[saltOffset:saltOffset+saltSize]) ||
		bytes.Equal(a[prefixOffset:prefixOffset+prefixSize], b[prefixOffset:prefixOffset+prefixSize]) ||
		bytes.Equal(a[headerSize:], b[headerSize:]) {
		t.Fatal("two encryptions share salt, nonce prefix or ciphertext")
	}
}

func TestWrongPassphrase(t *testing.T) {
	enc := encrypt(t, randBytes(1000), testPassphrase)
	if _, err := decrypt(enc, "correct horse battery stapler"); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("err = %v, want ErrWrongPassphrase", err)
	}
}

func TestEncryptTamper(t *testing.T) {
	plain := randBytes(3*chunkSize + 500)
	enc := encrypt(t, plain, testPassphrase)
	full := chunkSize + tagSize
	offsets := map[string]int{
		"salt":              saltOffset + 3,
		"nonce prefix":      prefixOffset,
		"kdf memory":        17,
		"key check":         checkOffset + 31,
		"first chunk":       headerSize,
		"second chunk":      headerSize + full + 1000,
		"first chunk tag":   headerSize + full - 1,
		"final chunk":       headerSize + 3*full + 10,
		"final chunk tag":   len(enc) - 1,
		"reserved":          22,
		"chunk size":        11,
		"format version":    8,
		"magic":             0,
		"middle of stream":  len(enc) / 2,
		"last byte of full": headerSize + 2*full - 1,
	}
	for name, off := range offsets {
		bad := bytes.Clone(enc)
		bad[off] ^= 0x01
		_, err := decrypt(bad, testPassphrase)
		if err == nil {
			t.Errorf("%s: flipped byte went unnoticed", name)
			continue
		}
		if !errors.Is(err, ErrCorrupt) && !errors.Is(err, ErrWrongPassphrase) {
			t.Errorf("%s: err = %v", name, err)
		}
		if off >= headerSize && !errors.Is(err, ErrCorrupt) {
			t.Errorf("%s: body damage reported as %v", name, err)
		}
	}
}

func TestEncryptTruncate(t *testing.T) {
	plain := randBytes(3 * chunkSize) // the final chunk is full-size
	enc := encrypt(t, plain, testPassphrase)
	full := chunkSize + tagSize
	for _, n := range []int{0, 10, headerSize - 1, headerSize, headerSize + 5, headerSize + full,
		headerSize + full + 100, headerSize + 2*full, len(enc) - 1} {
		_, err := decrypt(enc[:n], testPassphrase)
		if !errors.Is(err, ErrCorrupt) {
			t.Errorf("truncated to %d of %d bytes: err = %v, want ErrCorrupt", n, len(enc), err)
		}
	}
}

func TestEncryptReorder(t *testing.T) {
	plain := randBytes(4*chunkSize + 77)
	enc := encrypt(t, plain, testPassphrase)
	full := chunkSize + tagSize
	hdr, body := enc[:headerSize], enc[headerSize:]
	chunk := func(i int) []byte { return body[i*full : min((i+1)*full, len(body))] }
	join := func(idx ...int) []byte {
		out := bytes.Clone(hdr)
		for _, i := range idx {
			out = append(out, chunk(i)...)
		}
		return out
	}
	cases := map[string][]byte{
		"swap 0 and 1":      join(1, 0, 2, 3, 4),
		"swap 2 and 3":      join(0, 1, 3, 2, 4),
		"drop middle":       join(0, 1, 3, 4),
		"duplicate chunk":   join(0, 1, 1, 2, 3, 4),
		"drop final":        join(0, 1, 2, 3),
		"final moved first": join(4, 0, 1, 2, 3),
		"appended bytes":    append(join(0, 1, 2, 3, 4), 0),
		"appended chunk":    append(join(0, 1, 2, 3, 4), chunk(1)...),
	}
	for name, bad := range cases {
		if _, err := decrypt(bad, testPassphrase); !errors.Is(err, ErrCorrupt) {
			t.Errorf("%s: err = %v, want ErrCorrupt", name, err)
		}
	}
	// A chunk from another archive under the same passphrase is refused:
	// different salt, so different key and header.
	other := encrypt(t, plain, testPassphrase)
	spliced := append(bytes.Clone(enc[:headerSize+full]), other[headerSize+full:]...)
	if _, err := decrypt(spliced, testPassphrase); !errors.Is(err, ErrCorrupt) {
		t.Errorf("spliced archives: err = %v, want ErrCorrupt", err)
	}
}

func TestDecryptBoundsKDF(t *testing.T) {
	enc := encrypt(t, []byte("x"), testPassphrase)
	for name, set := range map[string]func(h []byte){
		"memory 4 GiB": func(h []byte) { binary.BigEndian.PutUint32(h[16:], 4<<20) },
		"time 1000":    func(h []byte) { binary.BigEndian.PutUint32(h[12:], 1000) },
		"no lanes":     func(h []byte) { h[20] = 0 },
	} {
		bad := bytes.Clone(enc)
		set(bad)
		if _, err := decrypt(bad, testPassphrase); !errors.Is(err, ErrCorrupt) {
			t.Errorf("%s: err = %v, want ErrCorrupt before key derivation", name, err)
		}
	}
}

func TestSpoolRoundTrip(t *testing.T) {
	sp, err := newSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer sp.remove()
	plain := append([]byte("PGDMP"), randBytes(2*chunkSize+9)...)
	w, err := sp.writer()
	if err != nil {
		t.Fatal(err)
	}
	w.Write(plain)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	onDisk, err := io.ReadAll(io.NewSectionReader(sp.f, 0, 1<<30))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(onDisk, plain[:64]) {
		t.Fatal("spool holds plaintext")
	}
	for range 2 { // the archive may be written more than once
		r, err := sp.reader()
		if err != nil {
			t.Fatal(err)
		}
		if got, err := io.ReadAll(r); err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("spool round trip: %v", err)
		}
	}
}
