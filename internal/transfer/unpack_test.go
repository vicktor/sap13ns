package transfer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sap13ns/internal/backup"
)

func tarGz(t *testing.T, trailing int, members ...[2]string) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, m := range members {
		if err := tw.WriteHeader(&tar.Header{Name: m[0], Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(m[1]))}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(m[1]))
	}
	tw.Close()
	gz.Write(make([]byte, trailing))
	gz.Close()
	return &buf
}

// An archive unpacks to no more than its budget, and the member that
// would pass it is not written at all.
func TestUnpackBudget(t *testing.T) {
	s := &Staged{dir: t.TempDir()}
	archive := tarGz(t, 0, [2]string{"files/uploads/a.bin", strings.Repeat("0", 600)}, [2]string{"files/uploads/b.bin", strings.Repeat("0", 600)})
	err := s.unpack(context.Background(), archive, 1000)
	if !errors.Is(err, ErrCorrupt) || !strings.Contains(err.Error(), "unpacks to more") {
		t.Fatalf("unpack: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.dir, "files", "uploads", "b.bin")); err == nil {
		t.Error("the member past the budget was written")
	}
}

// Nothing but a little padding follows the end of the archive.
func TestUnpackTrailingData(t *testing.T) {
	s := &Staged{dir: t.TempDir()}
	err := s.unpack(context.Background(), tarGz(t, 1<<20, [2]string{"files/uploads/a.bin", "x"}), 1<<30)
	if !errors.Is(err, ErrCorrupt) || !strings.Contains(err.Error(), "after the end") {
		t.Fatalf("unpack: %v", err)
	}
}

// The record of the migrations is not a table an archive brings.
func TestManifestRefusesMigrationsTable(t *testing.T) {
	data := backup.Member{Name: dirDB + schemaMigrations + copySuffix, Size: 2, SHA256: "x"}
	m := &Manifest{Format: FormatVersion, SecretKey: strings.Repeat("ab", 32), Migrations: []int{1},
		Tables: []Table{{Name: schemaMigrations, Columns: []string{"version"}, Rows: 1, Data: data}}}
	if err := m.check(map[string]backup.Member{data.Name: data}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("check: %v", err)
	}
}
