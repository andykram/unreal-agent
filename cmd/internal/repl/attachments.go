package repl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/unreallabsai/unreal-agent/harness/session"
	"golang.design/x/clipboard"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
	"uuid"
)

const maxAttachmentBytes = 32 << 20
const maxAttachmentPixels = 32_000_000

type Attachment struct {
	ID        string
	Path      string
	Width     int
	Height    int
	Format    string
	Submitted bool
	Missing   bool
}

func restoreAttachment(state *SessionState, sessionID, attachmentID string) Attachment {
	missing := Attachment{ID: attachmentID, Submitted: true, Missing: true}
	paths, err := filepath.Glob(filepath.Join(state.Directory, "attachments", sessionID, attachmentID+".*"))
	if err != nil || len(paths) != 1 {
		return missing
	}
	info, err := os.Stat(paths[0])
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxAttachmentBytes {
		return missing
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		return missing
	}
	format, width, height, err := validateImage(data, false)
	if err != nil {
		return missing
	}
	return Attachment{ID: attachmentID, Path: paths[0], Width: width, Height: height, Format: format, Submitted: true}
}

func validateImage(data []byte, clipboardPNG bool) (string, int, int, error) {
	if len(data) == 0 || len(data) > maxAttachmentBytes {
		return "", 0, 0, fmt.Errorf("image must contain 1 to %d bytes", maxAttachmentBytes)
	}
	if clipboardPNG && !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
		return "", 0, 0, errors.New("clipboard image is not PNG")
	}
	decoded, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", 0, 0, fmt.Errorf("decode image header: %w", err)
	}
	if clipboardPNG && format != "png" {
		return "", 0, 0, errors.New("clipboard image is not PNG")
	}
	if decoded.Width < 1 || decoded.Height < 1 || int64(decoded.Width)*int64(decoded.Height) > maxAttachmentPixels {
		return "", 0, 0, fmt.Errorf("image dimensions %d x %d exceed the 32-million-pixel limit", decoded.Width, decoded.Height)
	}
	switch format {
	case "png", "jpeg", "bmp", "tiff", "webp":
		if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
			return "", 0, 0, fmt.Errorf("decode image: %w", err)
		}
		return format, decoded.Width, decoded.Height, nil
	default:
		return "", 0, 0, fmt.Errorf("unsupported image format %q", format)
	}
}

func saveAttachment(state *SessionState, data []byte, clipboardPNG bool) (Attachment, error) {
	format, width, height, err := validateImage(data, clipboardPNG)
	if err != nil {
		return Attachment{}, err
	}
	if state.Current.SessionID == "" {
		return Attachment{}, errors.New("select a session before attaching an image")
	}
	directory := filepath.Join(state.Directory, "attachments", state.Current.SessionID)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return Attachment{}, err
	}
	id := uuid.New().String()
	extension := format
	if format == "jpeg" {
		extension = "jpg"
	}
	path := filepath.Join(directory, id+"."+extension)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return Attachment{}, err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		os.Remove(path)
		return Attachment{}, err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		os.Remove(path)
		return Attachment{}, err
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		return Attachment{}, err
	}
	return Attachment{ID: id, Path: path, Width: width, Height: height, Format: format}, nil
}

func attachLocalPath(state *SessionState, raw string) (Attachment, error) {
	path := strings.TrimSpace(raw)
	if strings.HasPrefix(path, `"`) {
		decoded, err := strconv.Unquote(path)
		if err != nil {
			return Attachment{}, fmt.Errorf("parse quoted image path: %w", err)
		}
		path = decoded
	}
	if path == "" {
		return Attachment{}, errors.New("image path is empty")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(state.Workspace, path)
	}
	file, err := os.Open(path)
	if err != nil {
		return Attachment{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Attachment{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxAttachmentBytes {
		return Attachment{}, errors.New("image must be a regular file of at most 32 MiB")
	}
	data := make([]byte, info.Size())
	if _, err := io.ReadFull(file, data); err != nil {
		return Attachment{}, err
	}
	return saveAttachment(state, data, false)
}

func attachmentPrompt(raw string, attachments []Attachment) string {
	if len(attachments) == 0 {
		return raw
	}
	var builder strings.Builder
	builder.WriteString(raw)
	for index, attachment := range attachments {
		fmt.Fprintf(&builder, "\n\n[Attached image %d: %s; %d x %d; %s]\nCall ViewImage with this local path before interpreting the image. This is a file attachment, not an inline image message.", index+1, strconv.Quote(attachment.Path), attachment.Width, attachment.Height, attachment.Format)
	}
	return builder.String()
}

type clipboardPayload struct {
	sessionID string
	image     []byte
	text      string
	err       error
}

type clipboardReader interface {
	Init() error
	Read(context.Context, clipboard.Format) ([]byte, error)
}

type nativeClipboard struct{}

func (nativeClipboard) Init() error { return clipboard.Init() }
func (nativeClipboard) Read(ctx context.Context, format clipboard.Format) ([]byte, error) {
	return clipboard.Read(ctx, format)
}

func readClipboard(ctx context.Context, source clipboardReader, id session.ID) clipboardPayload {
	result := clipboardPayload{sessionID: string(id)}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := source.Init(); err != nil {
		result.err = fmt.Errorf("initialize clipboard: %w", err)
		return result
	}
	imageData, err := source.Read(ctx, clipboard.FmtImage)
	if err == nil {
		result.image = imageData
		return result
	}
	if !errors.Is(err, clipboard.ErrNoData) {
		result.err = fmt.Errorf("read clipboard image: %w", err)
		return result
	}
	textData, err := source.Read(ctx, clipboard.FmtText)
	if err != nil {
		result.err = fmt.Errorf("read clipboard text: %w", err)
		return result
	}
	if !utf8.Valid(textData) {
		result.err = errors.New("clipboard text is not UTF-8")
		return result
	}
	result.text = string(textData)
	return result
}
