package repl

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const catalogCacheVersion = 1

type catalogCacheFile struct {
	Version int           `json:"version"`
	Choices []ModelChoice `json:"choices"`
}

func catalogCacheDirectory(getenv func(string) string) (string, error) {
	base := getenv("XDG_CACHE_HOME")
	if base == "" {
		home := getenv("HOME")
		if home == "" {
			return "", fmt.Errorf("HOME is unset; set HOME or XDG_CACHE_HOME for model catalog caching")
		}
		base = filepath.Join(home, ".cache")
	}
	if !filepath.IsAbs(base) {
		return "", fmt.Errorf("XDG_CACHE_HOME must be an absolute path: %q", base)
	}
	return filepath.Join(base, "unreal-agent-repl", "models"), nil
}

func catalogCachePath(getenv func(string) string, provider, endpoint, credential string) (string, error) {
	directory, err := catalogCacheDirectory(getenv)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(provider + "\x00" + endpoint + "\x00" + credential))
	return filepath.Join(directory, hex.EncodeToString(digest[:])+".json"), nil
}

func readCatalogCache(path, provider string) ([]ModelChoice, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (8<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 8<<20 {
		return nil, fmt.Errorf("model catalog cache exceeds 8 MiB")
	}
	var cached catalogCacheFile
	if err := json.Unmarshal(data, &cached); err != nil {
		return nil, err
	}
	if cached.Version != catalogCacheVersion {
		return nil, fmt.Errorf("unsupported model catalog cache version %d", cached.Version)
	}
	if len(cached.Choices) == 0 {
		return nil, fmt.Errorf("empty model catalog cache")
	}
	for i := range cached.Choices {
		if cached.Choices[i].Ref.ID == "" || cached.Choices[i].Ref.Provider != provider {
			return nil, fmt.Errorf("model catalog cache has an invalid model identity")
		}
		cached.Choices[i].Source = "cached"
		cached.Choices[i].Availability = "cached"
	}
	return cached.Choices, nil
}

func writeCatalogCache(path string, choices []ModelChoice) error {
	if len(choices) == 0 {
		return nil
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	data, err := json.Marshal(catalogCacheFile{Version: catalogCacheVersion, Choices: choices})
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".models-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	defer temporary.Close()
	if err := temporary.Chmod(0600); err != nil {
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func cacheYoungerThan(path string, age time.Duration) bool {
	info, err := os.Stat(path)
	return err == nil && !info.ModTime().After(time.Now()) && time.Since(info.ModTime()) < age
}
