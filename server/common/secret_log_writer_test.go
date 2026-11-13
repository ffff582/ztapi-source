package common

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestSecretLogWriterRedactsWholeEventAndReturnsInputLength(t *testing.T) {
	var output bytes.Buffer
	writer := NewSecretLogWriter(&output)
	input := []byte("X-Api-Key: fictional-writer-credential\nCookie: session=fictional-cookie-credential\ninput_tokens=123 cost=0.125 request_id=req-writer-17\n")
	original := append([]byte(nil), input...)
	n, err := writer.Write(input)
	if err != nil || n != len(input) {
		t.Fatalf("Write = (%d, %v), want (%d, nil)", n, err, len(input))
	}
	if !bytes.Equal(input, original) {
		t.Fatal("writer mutated the caller's evidence")
	}
	if strings.Contains(output.String(), "fictional-") {
		t.Fatal("writer leaked a credential")
	}
	for _, safe := range []string{"input_tokens=123", "cost=0.125", "req-writer-17"} {
		if !strings.Contains(output.String(), safe) {
			t.Errorf("writer lost %s", safe)
		}
	}
	if NewSecretLogWriter(writer) != writer {
		t.Fatal("redacting writers must not be nested")
	}
}

type failingSecretLogSink struct {
	partial bool
	err     error
}

func (w failingSecretLogSink) Write(p []byte) (int, error) {
	if w.partial {
		return len(p) / 2, w.err
	}
	return len(p), w.err
}

func TestSecretLogWriterPreservesErrorsAndRejectsShortWrites(t *testing.T) {
	failure := errors.New("fictional log sink failure")
	for _, test := range []struct {
		name    string
		sink    failingSecretLogSink
		wantN   int
		wantErr error
	}{
		{"short_without_error", failingSecretLogSink{partial: true}, 0, io.ErrShortWrite},
		{"partial_with_error", failingSecretLogSink{partial: true, err: failure}, 0, failure},
		{"full_with_error", failingSecretLogSink{err: failure}, len("api_key=fictional-input"), failure},
	} {
		t.Run(test.name, func(t *testing.T) {
			n, err := NewSecretLogWriter(test.sink).Write([]byte("api_key=fictional-input"))
			if n != test.wantN || !errors.Is(err, test.wantErr) {
				t.Fatalf("Write = (%d, %v), want (%d, %v)", n, err, test.wantN, test.wantErr)
			}
		})
	}
}
