package backup

// Encrypted archives
//
// An encrypted archive (.tar.gz.enc) is the gzip-compressed tar stream sealed
// with AES-256-GCM in 64 KiB chunks, using the STREAM construction (Hoang,
// Reyhanitabar, Rogaway and Vizár, "Online Authenticated-Encryption and its
// Nonce-Reuse Misuse-Resistance", CRYPTO 2015), as age and Tink do.
//
// The file starts with an 80-byte header. Integers are big-endian.
//
//	offset size field
//	     0    8 magic "SAP13NSE"
//	     8    1 format version: 1
//	     9    1 key derivation: 1 = Argon2id (version 0x13)
//	    10    1 cipher: 1 = AES-256-GCM
//	    11    1 log2 of the chunk size: 16 (64 KiB)
//	    12    4 Argon2id passes (t)
//	    16    4 Argon2id memory in KiB (m)
//	    20    1 Argon2id lanes (p)
//	    21    3 reserved, zero
//	    24   16 salt, random for every archive
//	    40    7 nonce prefix, random for every archive
//	    47    1 reserved, zero
//	    48   32 key check: HMAC-SHA256(Kc, header[0:48])
//
// Keys:
//
//	IKM = Argon2id(passphrase, salt, t, m, p, 32 bytes)
//	Ke  = HKDF-SHA256(IKM, info "sap13ns backup v1 encryption")  AES-256 key
//	Kc  = HKDF-SHA256(IKM, info "sap13ns backup v1 key check")   header MAC key
//
// Before decrypting anything, a reader checks the header MAC in constant
// time. A mismatch means a wrong passphrase or a damaged header, which keeps
// that case apart from a damaged body. Because the MAC is computed under a
// key derived from the passphrase, it also commits the archive to one key;
// AES-GCM alone does not. Readers refuse Argon2id parameters outside sane
// bounds, so a crafted header cannot make them allocate gigabytes.
//
// Body. The plaintext is cut into chunks of exactly 64 KiB, and only the last
// chunk may be shorter. The last chunk is empty only when the whole
// plaintext is empty. Chunk i (counting from 0) is sealed as
//
//	nonce_i = prefix (7 bytes) || uint32(i) (4 bytes) || last (1 byte: 1 for the final chunk, else 0)
//	C_i     = AES-256-GCM-Seal(Ke, nonce_i, P_i, additional data = the whole 80-byte header)
//
// and stored as ciphertext||tag, len(P_i)+16 bytes, with no framing. Chunk
// boundaries follow from the fixed size, and a chunk is final when the file
// ends right after it. The index and the final flag are inside the nonce, so
// a chunk that was reordered, dropped or duplicated fails authentication.
// The same holds for a file cut at a chunk boundary, whose new last chunk
// was not sealed as final, and for a file with bytes appended after its
// final chunk. Every chunk also authenticates the header, which binds the
// body to its salt, parameters and prefix. Each archive gets a fresh salt,
// so it also gets a fresh key. The random prefix adds defence in depth
// against nonce reuse. A stream can have at most 2^32 chunks (256 TiB).

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"

	"golang.org/x/crypto/argon2"
)

const (
	encMagic         = "SAP13NSE"
	encVersion       = 1
	kdfArgon2id      = 1
	cipherAES256GCM  = 1
	chunkLog2        = 16
	chunkSize        = 1 << chunkLog2
	tagSize          = 16
	headerSize       = 80
	saltOffset       = 24
	saltSize         = 16
	prefixOffset     = 40
	prefixSize       = 7
	checkOffset      = 48
	minPassphraseLen = 12
)

// kdfParams are the Argon2id cost parameters stored in the header.
type kdfParams struct {
	Time    uint32
	Memory  uint32 // KiB
	Threads uint8
}

// defaultKDF is the second recommended option of RFC 9106 (t=3, m=64 MiB,
// p=4). It runs once per archive, so it can cost far more than a login hash.
var defaultKDF = kdfParams{Time: 3, Memory: 64 * 1024, Threads: 4}

// valid bounds what a header may ask of a reader.
func (p kdfParams) valid() bool {
	return p.Time >= 1 && p.Time <= 16 && p.Threads >= 1 && p.Threads <= 16 &&
		p.Memory >= 8*uint32(p.Threads) && p.Memory <= 1<<20
}

func deriveKeys(passphrase string, salt []byte, p kdfParams) (ke, kc []byte) {
	ikm := argon2.IDKey([]byte(passphrase), salt, p.Time, p.Memory, p.Threads, 32)
	defer clear(ikm)
	// hkdf.Key only fails for lengths beyond 255*32 bytes.
	ke, _ = hkdf.Key(sha256.New, ikm, nil, "sap13ns backup v1 encryption", 32)
	kc, _ = hkdf.Key(sha256.New, ikm, nil, "sap13ns backup v1 key check", 32)
	return ke, kc
}

func headerMAC(kc, header []byte) []byte {
	m := hmac.New(sha256.New, kc)
	m.Write(header[:checkOffset])
	return m.Sum(nil)
}

// newEncrypter writes the header to w and returns a writer that seals what
// it is given. Close must be called to write the final chunk; it does not
// close w.
func newEncrypter(w io.Writer, passphrase string, p kdfParams) (io.WriteCloser, error) {
	if !p.valid() {
		return nil, errors.New("invalid key derivation parameters")
	}
	var h [headerSize]byte
	copy(h[:], encMagic)
	h[8], h[9], h[10], h[11] = encVersion, kdfArgon2id, cipherAES256GCM, chunkLog2
	binary.BigEndian.PutUint32(h[12:], p.Time)
	binary.BigEndian.PutUint32(h[16:], p.Memory)
	h[20] = p.Threads
	rand.Read(h[saltOffset : saltOffset+saltSize])
	rand.Read(h[prefixOffset : prefixOffset+prefixSize])
	ke, kc := deriveKeys(passphrase, h[saltOffset:saltOffset+saltSize], p)
	copy(h[checkOffset:], headerMAC(kc, h[:]))
	if _, err := w.Write(h[:]); err != nil {
		return nil, err
	}
	return newSealer(w, ke, h[prefixOffset:prefixOffset+prefixSize], h[:])
}

// newDecrypter reads and checks the header from r and returns a reader of the
// plaintext. The reader returns io.EOF only after the final chunk has been
// authenticated and nothing follows it. Callers must read it to the end
// before trusting what they read.
func newDecrypter(r io.Reader, passphrase string) (io.Reader, error) {
	br := asBufio(r)
	var h [headerSize]byte
	if _, err := io.ReadFull(br, h[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, fmt.Errorf("%w: truncated encryption header", ErrCorrupt)
		}
		return nil, err
	}
	if string(h[:8]) != encMagic {
		return nil, fmt.Errorf("%w: not an encrypted sap13ns backup", ErrCorrupt)
	}
	if h[8] != encVersion || h[9] != kdfArgon2id || h[10] != cipherAES256GCM || h[11] != chunkLog2 {
		return nil, fmt.Errorf("%w: unsupported encryption format %d/%d/%d/%d", ErrCorrupt, h[8], h[9], h[10], h[11])
	}
	if !allZero(h[21:24]) || h[47] != 0 {
		return nil, fmt.Errorf("%w: reserved header bytes are set", ErrCorrupt)
	}
	p := kdfParams{Time: binary.BigEndian.Uint32(h[12:]), Memory: binary.BigEndian.Uint32(h[16:]), Threads: h[20]}
	if !p.valid() {
		return nil, fmt.Errorf("%w: key derivation parameters out of range (t=%d m=%d p=%d)", ErrCorrupt, p.Time, p.Memory, p.Threads)
	}
	ke, kc := deriveKeys(passphrase, h[saltOffset:saltOffset+saltSize], p)
	if !hmac.Equal(headerMAC(kc, h[:]), h[checkOffset:]) {
		return nil, ErrWrongPassphrase
	}
	return newOpener(br, ke, h[prefixOffset:prefixOffset+prefixSize], h[:])
}

// sealer is the writing half of the chunked stream.
type sealer struct {
	w       io.Writer
	aead    cipher.AEAD
	aad     []byte
	nonce   [12]byte
	counter uint64
	buf     []byte // pending plaintext, at most chunkSize bytes
	out     []byte
	err     error
	closed  bool
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func newSealer(w io.Writer, key, prefix, aad []byte) (*sealer, error) {
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	s := &sealer{w: w, aead: aead, aad: bytes.Clone(aad),
		buf: make([]byte, 0, chunkSize), out: make([]byte, 0, chunkSize+tagSize)}
	copy(s.nonce[:prefixSize], prefix)
	return s, nil
}

func (s *sealer) Write(p []byte) (int, error) {
	if s.closed {
		return 0, errors.New("write to closed encrypter")
	}
	if s.err != nil {
		return 0, s.err
	}
	n := 0
	for len(p) > 0 {
		// A full buffer is sealed only once more data arrives, so that
		// Close always has a chunk to mark as final.
		if len(s.buf) == chunkSize {
			if s.err = s.seal(false); s.err != nil {
				return n, s.err
			}
		}
		k := copy(s.buf[len(s.buf):chunkSize], p)
		s.buf = s.buf[:len(s.buf)+k]
		p = p[k:]
		n += k
	}
	return n, nil
}

func (s *sealer) seal(last bool) error {
	if s.counter > math.MaxUint32 {
		return errors.New("encrypted stream too long")
	}
	binary.BigEndian.PutUint32(s.nonce[prefixSize:], uint32(s.counter))
	s.nonce[11] = 0
	if last {
		s.nonce[11] = 1
	}
	s.out = s.aead.Seal(s.out[:0], s.nonce[:], s.buf, s.aad)
	s.buf = s.buf[:0]
	s.counter++
	_, err := s.w.Write(s.out)
	return err
}

// Close seals the final chunk.
func (s *sealer) Close() error {
	if s.closed {
		return s.err
	}
	s.closed = true
	if s.err == nil {
		s.err = s.seal(true)
	}
	return s.err
}

// opener is the reading half of the chunked stream.
type opener struct {
	r       *bufio.Reader
	aead    cipher.AEAD
	aad     []byte
	nonce   [12]byte
	counter uint64
	buf     []byte // one sealed chunk
	plain   []byte // decrypted bytes not yet returned, inside buf
	done    bool   // the final chunk has been authenticated
	err     error
}

func newOpener(r io.Reader, key, prefix, aad []byte) (*opener, error) {
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	o := &opener{r: asBufio(r), aead: aead, aad: bytes.Clone(aad), buf: make([]byte, chunkSize+tagSize)}
	copy(o.nonce[:prefixSize], prefix)
	return o, nil
}

func (o *opener) Read(p []byte) (int, error) {
	for len(o.plain) == 0 {
		if o.err != nil {
			return 0, o.err
		}
		if o.done {
			return 0, io.EOF
		}
		o.err = o.next()
	}
	n := copy(p, o.plain)
	o.plain = o.plain[n:]
	return n, nil
}

func (o *opener) next() error {
	n, err := io.ReadFull(o.r, o.buf)
	last := false
	switch {
	case err == nil:
		// A full-size chunk is the final one only if the file ends here.
		if _, perr := o.r.Peek(1); perr == io.EOF {
			last = true
		} else if perr != nil {
			return perr
		}
	case errors.Is(err, io.ErrUnexpectedEOF):
		last = true
	case errors.Is(err, io.EOF):
		return fmt.Errorf("%w: encrypted stream ends without its final chunk", ErrCorrupt)
	default:
		return err
	}
	if n < tagSize {
		return fmt.Errorf("%w: encrypted stream truncated", ErrCorrupt)
	}
	if o.counter > math.MaxUint32 {
		return fmt.Errorf("%w: encrypted stream too long", ErrCorrupt)
	}
	binary.BigEndian.PutUint32(o.nonce[prefixSize:], uint32(o.counter))
	o.nonce[11] = 0
	if last {
		o.nonce[11] = 1
	}
	plain, err := o.aead.Open(o.buf[:0], o.nonce[:], o.buf[:n], o.aad)
	if err != nil {
		return fmt.Errorf("%w: chunk %d failed authentication (modified, reordered or truncated)", ErrCorrupt, o.counter)
	}
	o.plain = plain
	o.counter++
	o.done = last
	return nil
}

func asBufio(r io.Reader) *bufio.Reader {
	if br, ok := r.(*bufio.Reader); ok && br.Size() >= chunkSize {
		return br
	}
	return bufio.NewReaderSize(r, chunkSize+tagSize)
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// spool holds the database dump while the archive is assembled, because tar
// needs each member's size before its content. It is sealed with a random key
// that only lives in memory, so a file left behind by a crash is unreadable.
type spool struct {
	f      *os.File
	key    [32]byte
	prefix [prefixSize]byte
}

var spoolAAD = []byte("sap13ns backup spool")

func newSpool(dir string) (*spool, error) {
	f, err := os.CreateTemp(dir, ".sap13ns-dump-*.tmp")
	if err != nil {
		return nil, err
	}
	s := &spool{f: f}
	rand.Read(s.key[:])
	rand.Read(s.prefix[:])
	return s, nil
}

func (s *spool) writer() (io.WriteCloser, error) {
	return newSealer(s.f, s.key[:], s.prefix[:], spoolAAD)
}

func (s *spool) reader() (io.Reader, error) {
	if _, err := s.f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return newOpener(bufio.NewReaderSize(s.f, chunkSize+tagSize), s.key[:], s.prefix[:], spoolAAD)
}

func (s *spool) remove() {
	s.f.Close()
	os.Remove(s.f.Name())
	clear(s.key[:])
}
