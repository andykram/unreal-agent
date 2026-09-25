package workflow

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const runtimeDirectory = "python-3.14.7"
const runtimeSite = "lib/python3.14/site-packages"
const runtimeMarker = ".workflow-pydantic-1.10.26-typing-4.16.0"
const runtimeManifest = "cpython-3.14.7:pydantic-1.10.26:typing-4.16.0\n"

type runtimeArchive struct{ url, sha256, destination string }

var pinnedRuntimeArchives = []runtimeArchive{
	{"https://github.com/brettcannon/cpython-wasi-build/releases/download/v3.14.7/python-3.14.7-wasi_sdk-24.zip", "2e064d3fb8172471d39d741348efa722349c40b96301f69968dff714999c584b", ""},
	{"https://files.pythonhosted.org/packages/1f/98/556e82f00b98486def0b8af85da95e69d2be7e367cf2431408e108bc3095/pydantic-1.10.26-py3-none-any.whl", "c43ad70dc3ce7787543d563792426a16fd7895e14be4b194b5665e36459dd917", runtimeSite},
	{"https://files.pythonhosted.org/packages/49/d3/b8441a820a491ddfc024b0b0cf0393375b75ea13866d9c66727e54c2fc80/typing_extensions-4.16.0-py3-none-any.whl", "481caa481374e813c1b176ada14e97f1f67a4539ce9cfeb3f350d78d6370c2e8", runtimeSite},
}

// EnsureRuntime installs the pinned WASI interpreter and pure Python dependencies
// without host tools. Completed installations are reused indefinitely.
func EnsureRuntime(ctx context.Context, cacheDir string) (string, error) {
	return ensureRuntime(ctx, cacheDir, &http.Client{Timeout: 10 * time.Minute}, pinnedRuntimeArchives)
}

func runtimeComplete(root string) bool {
	marker, err := os.ReadFile(filepath.Join(root, runtimeSite, runtimeMarker))
	// The shell installer used an empty completion marker with this versioned name.
	if err != nil || (len(marker) != 0 && string(marker) != runtimeManifest) {
		return false
	}
	for _, name := range []string{"python.wasm", "lib/python3.14/encodings/__init__.py", runtimeSite + "/pydantic/__init__.py", runtimeSite + "/typing_extensions.py"} {
		info, err := os.Lstat(filepath.Join(root, name))
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return false
		}
	}
	return true
}

func ensureRuntime(ctx context.Context, cacheDir string, client *http.Client, archives []runtimeArchive) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if cacheDir == "" {
		return "", fmt.Errorf("workflow runtime cache directory is empty")
	}
	target := filepath.Join(cacheDir, runtimeDirectory)
	if runtimeComplete(target) {
		return target, nil
	}
	if _, err := os.Lstat(target); err == nil {
		return "", fmt.Errorf("workflow runtime at %s is incomplete; move it aside and retry installation", target)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect workflow runtime: %w", err)
	}
	if err := os.MkdirAll(cacheDir, 0700); err != nil {
		return "", fmt.Errorf("create workflow runtime cache: %w", err)
	}
	stage, err := os.MkdirTemp(cacheDir, ".runtime-install-")
	if err != nil {
		return "", fmt.Errorf("stage workflow runtime: %w", err)
	}
	defer os.RemoveAll(stage)
	root := filepath.Join(stage, "runtime")
	if err := os.Mkdir(root, 0700); err != nil {
		return "", err
	}
	for index, archive := range archives {
		filename := filepath.Join(stage, fmt.Sprintf("archive-%d.zip", index))
		if err := downloadRuntimeArchive(ctx, client, archive, filename); err != nil {
			return "", err
		}
		if err := extractRuntimeArchive(ctx, filename, filepath.Join(root, archive.destination)); err != nil {
			return "", fmt.Errorf("extract workflow runtime archive %s: %w", archive.url, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(root, runtimeSite, runtimeMarker), []byte(runtimeManifest), 0600); err != nil {
		return "", fmt.Errorf("write runtime completion marker: %w", err)
	}
	if !runtimeComplete(root) {
		return "", fmt.Errorf("downloaded workflow runtime is missing required interpreter or library files")
	}
	if err := os.Rename(root, target); err != nil {
		// Another process may have published the same pinned installation first.
		if runtimeComplete(target) {
			return target, nil
		}
		return "", fmt.Errorf("publish workflow runtime at %s: %w", target, err)
	}
	return target, nil
}

func downloadRuntimeArchive(ctx context.Context, client *http.Client, archive runtimeArchive, filename string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, archive.url, nil)
	if err != nil {
		return fmt.Errorf("prepare workflow runtime download: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("download workflow runtime %s: %w", archive.url, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download workflow runtime %s: HTTP %s; retry when the source is available", archive.url, response.Status)
	}
	output, err := os.OpenFile(filename, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	digest := sha256.New()
	const maxArchive = 256 << 20
	size, copyErr := io.Copy(io.MultiWriter(output, digest), io.LimitReader(response.Body, maxArchive+1))
	closeErr := output.Close()
	if copyErr != nil {
		return fmt.Errorf("download workflow runtime archive: %w", copyErr)
	}
	if closeErr != nil {
		return closeErr
	}
	if size > maxArchive {
		return fmt.Errorf("workflow runtime archive exceeds 256 MiB")
	}
	if hex.EncodeToString(digest.Sum(nil)) != archive.sha256 {
		return fmt.Errorf("workflow runtime checksum mismatch for %s; retry the download, do not bypass verification", archive.url)
	}
	return nil
}

type runtimeContextReader struct {
	context context.Context
	reader  io.Reader
}

func (r runtimeContextReader) Read(p []byte) (int, error) {
	if err := r.context.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func extractRuntimeArchive(ctx context.Context, filename, root string) error {
	archive, err := zip.OpenReader(filename)
	if err != nil {
		return err
	}
	defer archive.Close()
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	var total uint64
	const maxExpanded = 512 << 20
	for _, entry := range archive.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name
		clean := path.Clean(name)
		if name == "" || strings.ContainsAny(name, "\\:\x00") || path.IsAbs(name) || clean == ".." || strings.HasPrefix(clean, "../") {
			return fmt.Errorf("unsafe archive path %q", name)
		}
		if clean == "." && entry.FileInfo().IsDir() {
			continue
		}
		if entry.Mode()&os.ModeSymlink != 0 || (!entry.FileInfo().IsDir() && !entry.Mode().IsRegular()) {
			return fmt.Errorf("unsupported archive entry %q", name)
		}
		if entry.UncompressedSize64 > maxExpanded-total {
			return fmt.Errorf("runtime archive expands beyond 512 MiB")
		}
		total += entry.UncompressedSize64
		target := filepath.Join(root, filepath.FromSlash(clean))
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0700); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		input, err := entry.Open()
		if err != nil {
			output.Close()
			return err
		}
		_, copyErr := io.Copy(output, runtimeContextReader{ctx, input})
		input.Close()
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
