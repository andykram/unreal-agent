package repl

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.design/x/clipboard"
)

type fakeClipboard struct {
	image    []byte
	text     []byte
	imageErr error
	textErr  error
}

func (fakeClipboard) Init() error { return nil }
func (source fakeClipboard) Read(_ context.Context, format clipboard.Format) ([]byte, error) {
	if format == clipboard.FmtImage {
		return source.image, source.imageErr
	}
	return source.text, source.textErr
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	picture := image.NewRGBA(image.Rect(0, 0, 2, 3))
	picture.Set(0, 0, color.RGBA{R: 255, A: 255})
	var output bytes.Buffer
	if err := png.Encode(&output, picture); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestImageAttachmentValidationAndOwnedPath(t *testing.T) {
	root := t.TempDir()
	state, err := OpenSessionState(filepath.Join(root, "state"), root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.New(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	data := testPNG(t)
	attachment, err := saveAttachment(state, data, true)
	if err != nil {
		t.Fatal(err)
	}
	if attachment.Width != 2 || attachment.Height != 3 || attachment.Format != "png" || !strings.Contains(attachment.Path, state.Current.SessionID) {
		t.Fatalf("attachment = %#v", attachment)
	}
	info, err := os.Stat(attachment.Path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("owned mode = %v, %v", info, err)
	}
	prompt := attachmentPrompt("describe", []Attachment{attachment})
	if !strings.Contains(prompt, "Call ViewImage") || !strings.Contains(prompt, attachment.Path) {
		t.Fatalf("prompt = %q", prompt)
	}
	if _, _, _, err := validateImage([]byte("not png"), true); err == nil {
		t.Fatal("invalid PNG was accepted")
	}
	if _, _, _, err := validateImage(data[:len(data)-2], true); err == nil {
		t.Fatal("truncated PNG was accepted")
	}
	restored := restoreAttachment(state, state.Current.SessionID, attachment.ID)
	if restored.Missing || !restored.Submitted || restored.Path != attachment.Path {
		t.Fatalf("restored attachment = %#v", restored)
	}
	if err := os.Remove(attachment.Path); err != nil {
		t.Fatal(err)
	}
	if restored := restoreAttachment(state, state.Current.SessionID, attachment.ID); !restored.Missing {
		t.Fatalf("missing image not detected: %#v", restored)
	}
}

func TestClipboardImageFirstAndTextFallback(t *testing.T) {
	imageData := testPNG(t)
	image := readClipboard(t.Context(), fakeClipboard{image: imageData, text: []byte("other")}, "session")
	if !bytes.Equal(image.image, imageData) || image.text != "" {
		t.Fatalf("image read = %#v", image)
	}
	text := readClipboard(t.Context(), fakeClipboard{imageErr: clipboard.ErrNoData, text: []byte("hello")}, "session")
	if text.text != "hello" || text.err != nil {
		t.Fatalf("text fallback = %#v", text)
	}
	failure := readClipboard(t.Context(), fakeClipboard{imageErr: clipboard.ErrUnavailable, text: []byte("stale")}, "session")
	if !errors.Is(failure.err, clipboard.ErrUnavailable) || failure.text != "" {
		t.Fatalf("backend failure = %#v", failure)
	}
}
