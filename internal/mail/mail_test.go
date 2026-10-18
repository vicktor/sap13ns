package mail

import (
	"bufio"
	"bytes"
	"net/textproto"
	"strings"
	"testing"
)

func TestCRLF(t *testing.T) {
	for in, want := range map[string]string{
		"one\ntwo":           "one\r\ntwo",
		"one\r\ntwo":         "one\r\ntwo",
		"smuggle\r.\r\nMAIL": "smuggle\r\n.\r\nMAIL",
		"bare\rcr\n\r\n":     "bare\r\ncr\r\n\r\n",
	} {
		if got := crlf(in); got != want {
			t.Errorf("crlf(%q) = %q, want %q", in, got, want)
		}
	}

	// Through the SMTP DATA writer, the line that would end the message
	// early is dot-stuffed.
	var buf bytes.Buffer
	bw := bufio.NewWriter(&buf)
	dw := textproto.NewWriter(bw).DotWriter()
	dw.Write([]byte(crlf("hi\r.\r\nMAIL FROM:<x@example.test>")))
	dw.Close()
	bw.Flush()
	if out := buf.String(); !strings.Contains(out, "\r\n..\r\n") || strings.Count(out, "\r\n.\r\n") != 1 {
		t.Fatalf("DATA %q", out)
	}
}
