package daemon

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// endless produces bytes for ever, standing in for a response that simply
// keeps sending. Reading it without a bound never returns.
type endless struct{ read int64 }

func (e *endless) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'A'
	}
	e.read += int64(len(p))
	return len(p), nil
}

func TestReadBoundedAcceptsNormalBody(t *testing.T) {
	want := bytes.Repeat([]byte("x"), 4096)
	got, err := readBounded(int64(len(want)), bytes.NewReader(want))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Error("body came back altered")
	}
}

func TestReadBoundedRejectsDeclaredOversize(t *testing.T) {
	// Declares too much: refused without reading anything at all.
	body := &endless{}
	_, err := readBounded(maxDownloadBytes+1, body)
	if !errors.Is(err, errAttachmentTooLarge) {
		t.Fatalf("want errAttachmentTooLarge, got %v", err)
	}
	if body.read != 0 {
		t.Errorf("nothing should have been read, got %d bytes", body.read)
	}
}

// The important one: a response that lies about its size, or declares none,
// must still not be able to allocate without bound.
func TestReadBoundedRejectsUndeclaredOversize(t *testing.T) {
	for _, declared := range []int64{-1, 0, 1024} {
		body := &endless{}
		_, err := readBounded(declared, body)
		if !errors.Is(err, errAttachmentTooLarge) {
			t.Fatalf("declared=%d: want errAttachmentTooLarge, got %v", declared, err)
		}
		// Bounded by the cap, not by what the far side claimed.
		if body.read > maxDownloadBytes+4096 {
			t.Errorf("declared=%d: read %d bytes, far past the %d cap",
				declared, body.read, maxDownloadBytes)
		}
	}
}

func TestReadBoundedAcceptsExactlyTheCap(t *testing.T) {
	// Landing exactly on the cap is legal; one byte more is not.
	at := io.LimitReader(&endless{}, maxDownloadBytes)
	got, err := readBounded(-1, at)
	if err != nil {
		t.Fatalf("a body exactly at the cap must be accepted, got %v", err)
	}
	if len(got) != maxDownloadBytes {
		t.Errorf("got %d bytes, want %d", len(got), maxDownloadBytes)
	}

	over := io.LimitReader(&endless{}, maxDownloadBytes+1)
	if _, err := readBounded(-1, over); !errors.Is(err, errAttachmentTooLarge) {
		t.Errorf("one byte over the cap must be refused, got %v", err)
	}
}
