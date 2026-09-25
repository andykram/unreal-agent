package workflow

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

type runtimeRoundTripper func(*http.Request) (*http.Response, error)

func (f runtimeRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func runtimeFixture(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	for name, body := range files {
		f, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(f, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}
func runtimeFixtureArchive(data []byte, destination string) runtimeArchive {
	digest := sha256.Sum256(data)
	return runtimeArchive{"https://runtime.test/archive", hex.EncodeToString(digest[:]), destination}
}
func TestRuntimeInstallAndOfflineReuse(t *testing.T) {
	data := runtimeFixture(t, map[string]string{
		"python.wasm": "wasm", "lib/python3.14/encodings/__init__.py": "encoding",
		runtimeSite + "/pydantic/__init__.py": "pydantic", runtimeSite + "/typing_extensions.py": "typing",
	})
	var requests atomic.Int32
	client := &http.Client{Transport: runtimeRoundTripper(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data)), Header: make(http.Header)}, nil
	})}
	cache := t.TempDir()
	root, err := ensureRuntime(context.Background(), cache, client, []runtimeArchive{runtimeFixtureArchive(data, "")})
	if err != nil {
		t.Fatal(err)
	}
	if !runtimeComplete(root) {
		t.Fatal("published runtime incomplete")
	}
	offline := &http.Client{Transport: runtimeRoundTripper(func(*http.Request) (*http.Response, error) {
		t.Fatal("cache reuse performed network access")
		return nil, nil
	})}
	again, err := ensureRuntime(context.Background(), cache, offline, nil)
	if err != nil || again != root || requests.Load() != 1 {
		t.Fatalf("reuse %q %v requests=%d", again, err, requests.Load())
	}
	// Existing shell installations have an empty versioned marker.
	if err := os.WriteFile(filepath.Join(root, runtimeSite, runtimeMarker), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if !runtimeComplete(root) {
		t.Fatal("legacy shell runtime rejected")
	}
}
func TestRuntimeChecksumFailureNeverPublishes(t *testing.T) {
	data := runtimeFixture(t, map[string]string{"python.wasm": "untrusted"})
	client := &http.Client{Transport: runtimeRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data))}, nil
	})}
	cache := t.TempDir()
	archive := runtimeFixtureArchive(data, "")
	archive.sha256 = strings.Repeat("0", 64)
	if _, err := ensureRuntime(context.Background(), cache, client, []runtimeArchive{archive}); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("got %v", err)
	}
	entries, err := os.ReadDir(cache)
	if err != nil || len(entries) != 0 {
		t.Fatalf("partial installation retained: %v %v", entries, err)
	}
}
func TestRuntimeRejectsArchiveTraversalAndLinks(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute", "dir/../../escape", `dir\escape`, "C:/escape"} {
		t.Run(name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "test.zip")
			if err := os.WriteFile(archive, runtimeFixture(t, map[string]string{name: "bad"}), 0600); err != nil {
				t.Fatal(err)
			}
			if err := extractRuntimeArchive(context.Background(), archive, t.TempDir()); err == nil {
				t.Fatal("unsafe path accepted")
			}
		})
	}
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	header := &zip.FileHeader{Name: "link"}
	header.SetMode(os.ModeSymlink | 0777)
	file, err := writer.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(file, "../escape")
	writer.Close()
	archive := filepath.Join(t.TempDir(), "link.zip")
	os.WriteFile(archive, data.Bytes(), 0600)
	if err := extractRuntimeArchive(context.Background(), archive, t.TempDir()); err == nil {
		t.Fatal("symlink accepted")
	}
}
func TestRuntimeDownloadCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	client := &http.Client{Transport: runtimeRoundTripper(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	done := make(chan error, 1)
	go func() {
		done <- downloadRuntimeArchive(ctx, client, runtimeArchive{url: "https://runtime.test/slow"}, filepath.Join(t.TempDir(), "archive"))
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}
func TestRuntimeIncompleteCachePreserved(t *testing.T) {
	cache := t.TempDir()
	root := filepath.Join(cache, runtimeDirectory)
	os.Mkdir(root, 0700)
	filename := filepath.Join(root, "user-file")
	os.WriteFile(filename, []byte("preserve"), 0600)
	if _, err := ensureRuntime(context.Background(), cache, nil, nil); err == nil || !strings.Contains(err.Error(), "move it aside") {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(filename); err != nil {
		t.Fatal("existing files lost")
	}
}
