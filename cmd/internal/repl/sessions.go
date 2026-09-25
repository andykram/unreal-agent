package repl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"uuid"

	"github.com/rivo/uniseg"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
)

const metadataVersion = 1

type SessionMetadata struct {
	Version         int               `json:"version"`
	SessionID       string            `json:"session_id"`
	Name            string            `json:"name"`
	Workspace       string            `json:"workspace"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
	ResponseOrigins map[string]string `json:"response_origins"`
	ParentSessionID string            `json:"parent_session_id,omitempty"`
	ForkTurnID      string            `json:"fork_turn_id,omitempty"`
}

type SessionChoice struct {
	Metadata   SessionMetadata
	Depth      int
	LastActive time.Time
	Unfinished int
	Providers  []string
	Issue      error
}

func (state *SessionState) Family(ctx context.Context) ([]SessionChoice, error) {
	choices, err := state.List(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]SessionChoice)
	for _, choice := range choices {
		if choice.Issue == nil && choice.Metadata.Workspace == state.Workspace {
			byID[choice.Metadata.SessionID] = choice
		}
	}
	root := state.Current.SessionID
	seen := map[string]bool{}
	for root != "" && !seen[root] {
		seen[root] = true
		parent := byID[root].Metadata.ParentSessionID
		if parent == "" || byID[parent].Metadata.SessionID == "" {
			break
		}
		root = parent
	}
	var family []SessionChoice
	visited := map[string]bool{}
	var appendChildren func(string, int)
	appendChildren = func(id string, depth int) {
		if visited[id] {
			return
		}
		choice, exists := byID[id]
		if !exists {
			return
		}
		visited[id] = true
		choice.Depth = depth
		family = append(family, choice)
		var children []SessionChoice
		for _, candidate := range byID {
			if candidate.Metadata.ParentSessionID == id {
				children = append(children, candidate)
			}
		}
		sort.Slice(children, func(i, j int) bool { return children[i].Metadata.CreatedAt.Before(children[j].Metadata.CreatedAt) })
		for _, child := range children {
			appendChildren(child.Metadata.SessionID, depth+1)
		}
	}
	appendChildren(root, 0)
	return family, nil
}

type SessionState struct {
	Directory  string
	Workspace  string
	Store      *localfile.Store
	Current    SessionMetadata
	lock       *os.File
	metadataMu sync.Mutex
	origins    map[string]string
}

func StateDirectory(getenv func(string) string) (string, error) {
	base := getenv("XDG_STATE_HOME")
	if base == "" {
		home := getenv("HOME")
		if home == "" {
			return "", errors.New("HOME is unset; set HOME or XDG_STATE_HOME")
		}
		base = filepath.Join(home, ".local", "state")
	}
	if !filepath.IsAbs(base) {
		return "", fmt.Errorf("XDG_STATE_HOME must be an absolute path: %q", base)
	}
	return filepath.Join(base, "unreal-agent-repl"), nil
}

func CanonicalWorkspace(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve workspace %s: %w", absolute, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace %s is not a directory", resolved)
	}
	return resolved, nil
}

func WorkspaceKey(workspace string) string {
	digest := sha256.Sum256([]byte(workspace))
	return hex.EncodeToString(digest[:])
}

func OpenSessionState(directory, workspace string) (*SessionState, error) {
	canonical, err := CanonicalWorkspace(workspace)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(directory) {
		return nil, fmt.Errorf("state directory must be absolute: %q", directory)
	}
	for _, child := range []string{"sessions", "metadata", "history", "attachments", "tmp", "locks"} {
		if err := os.MkdirAll(filepath.Join(directory, child), 0700); err != nil {
			return nil, fmt.Errorf("create state directory %s: %w", child, err)
		}
	}
	store, err := localfile.New(filepath.Join(directory, "sessions"))
	if err != nil {
		return nil, err
	}
	return &SessionState{Directory: directory, Workspace: canonical, Store: store}, nil
}

func (state *SessionState) metadataPath(id session.ID) string {
	return filepath.Join(state.Directory, "metadata", string(id)+".json")
}

func (state *SessionState) acquire(id session.ID) error {
	if state.lock != nil {
		return errors.New("a session is already active")
	}
	lock, err := os.OpenFile(filepath.Join(state.Directory, "locks", string(id)+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return fmt.Errorf("session %s is active in another process", id)
		}
		return err
	}
	state.lock = lock
	return nil
}

func (state *SessionState) Close() error {
	if state.lock == nil {
		return nil
	}
	err := syscall.Flock(int(state.lock.Fd()), syscall.LOCK_UN)
	err = errors.Join(err, state.lock.Close())
	state.lock = nil
	return err
}

func (state *SessionState) New(ctx context.Context) (SessionMetadata, error) {
	if state.lock != nil {
		return SessionMetadata{}, errors.New("close the active session before creating another")
	}
	id := session.ID(newSessionID())
	if err := state.acquire(id); err != nil {
		return SessionMetadata{}, err
	}
	if _, err := state.Store.Create(ctx, id); err != nil {
		state.Close()
		return SessionMetadata{}, err
	}
	now := time.Now().UTC()
	metadata := SessionMetadata{
		Version: metadataVersion, SessionID: string(id),
		Name:      "Session " + now.Local().Format("2006-01-02 15:04") + " " + string(id)[:8],
		Workspace: state.Workspace, CreatedAt: now, UpdatedAt: now,
		ResponseOrigins: map[string]string{},
	}
	if err := state.saveMetadata(metadata); err != nil {
		state.Close()
		return SessionMetadata{}, err
	}
	state.Current = metadata
	state.origins = metadata.ResponseOrigins
	return metadata, nil
}

func (state *SessionState) ForkCurrent(ctx context.Context, name string) (SessionMetadata, error) {
	if state.lock == nil || state.Current.SessionID == "" {
		return SessionMetadata{}, errors.New("no active session to fork")
	}
	parent := state.Current
	turn := session.TurnID("")
	after := sessionstore.BeforeFirst
	for {
		page, err := state.Store.Items(ctx, session.ID(parent.SessionID), after, 256)
		if err != nil {
			return SessionMetadata{}, err
		}
		for _, item := range page.Items {
			if item.Kind == sessionstore.ItemTurn {
				turn = item.Data.(session.Turn).ID
			}
		}
		if !page.More {
			break
		}
		after = page.NextAfter
	}
	if turn == "" {
		return SessionMetadata{}, errors.New("send a prompt before forking this session")
	}
	id := session.ID(newSessionID())
	if name == "" {
		name = "Fork " + string(id)[:8]
	}
	if err := validateSessionName(name); err != nil {
		return SessionMetadata{}, err
	}
	choices, err := state.List(ctx)
	if err != nil {
		return SessionMetadata{}, err
	}
	for _, choice := range choices {
		if choice.Issue == nil && choice.Metadata.Workspace == state.Workspace && choice.Metadata.Name == name {
			return SessionMetadata{}, fmt.Errorf("session name %q is already used in this workspace", name)
		}
	}
	if _, err := state.Store.Fork(ctx, id, session.ID(parent.SessionID), turn); err != nil {
		return SessionMetadata{}, err
	}
	now := time.Now().UTC()
	metadata := SessionMetadata{Version: metadataVersion, SessionID: string(id), Name: name, Workspace: state.Workspace, CreatedAt: now, UpdatedAt: now, ParentSessionID: parent.SessionID, ForkTurnID: string(turn), ResponseOrigins: cloneOrigins(parent.ResponseOrigins)}
	if err := state.saveMetadata(metadata); err != nil {
		return SessionMetadata{}, err
	}
	return metadata, nil
}

func newSessionID() string { return uuid.New().String() }

func (state *SessionState) Resume(ctx context.Context, nameOrID string) (SessionMetadata, error) {
	if state.lock != nil {
		return SessionMetadata{}, errors.New("close the active session before resuming another")
	}
	choices, err := state.List(ctx)
	if err != nil {
		return SessionMetadata{}, err
	}
	var matches []SessionMetadata
	for _, choice := range choices {
		if choice.Issue != nil || choice.Metadata.Workspace != state.Workspace {
			continue
		}
		if choice.Metadata.Name == nameOrID || choice.Metadata.SessionID == nameOrID {
			matches = append(matches, choice.Metadata)
		}
	}
	if len(matches) == 0 {
		return SessionMetadata{}, fmt.Errorf("session %q was not found in workspace %s", nameOrID, state.Workspace)
	}
	if len(matches) > 1 {
		return SessionMetadata{}, fmt.Errorf("session name %q is ambiguous in workspace %s", nameOrID, state.Workspace)
	}
	metadata := matches[0]
	id := session.ID(metadata.SessionID)
	if err := state.acquire(id); err != nil {
		return SessionMetadata{}, err
	}
	if _, err := state.Store.Resume(ctx, id); err != nil {
		state.Close()
		return SessionMetadata{}, fmt.Errorf("resume %s: %w", id, err)
	}
	state.Current = metadata
	state.origins = metadata.ResponseOrigins
	return metadata, nil
}

func (state *SessionState) List(ctx context.Context) ([]SessionChoice, error) {
	listed, err := state.Store.ListSessions(ctx)
	if err != nil {
		return nil, err
	}
	lastActive := make(map[session.ID]time.Time, len(listed))
	for _, info := range listed {
		lastActive[info.ID] = info.LastUpdatedAt
	}
	entries, err := os.ReadDir(filepath.Join(state.Directory, "metadata"))
	if err != nil {
		return nil, err
	}
	choices := make([]SessionChoice, 0, len(entries))
	withMetadata := make(map[session.ID]struct{}, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(state.Directory, "metadata", entry.Name())
		withMetadata[session.ID(strings.TrimSuffix(entry.Name(), ".json"))] = struct{}{}
		encoded, err := os.ReadFile(path)
		choice := SessionChoice{}
		if err == nil {
			err = json.Unmarshal(encoded, &choice.Metadata)
		}
		if err == nil && (choice.Metadata.Version != metadataVersion || choice.Metadata.SessionID+".json" != entry.Name()) {
			err = fmt.Errorf("invalid metadata identity or version")
		}
		if err != nil {
			choice.Issue = fmt.Errorf("read metadata %s: %w", path, err)
			choice.Metadata.SessionID = strings.TrimSuffix(entry.Name(), ".json")
		}
		if choice.Issue == nil {
			updated, exists := lastActive[session.ID(choice.Metadata.SessionID)]
			if !exists {
				choice.Issue = fmt.Errorf("session store data is missing for %s", choice.Metadata.SessionID)
			} else {
				choice.LastActive = updated
				resume, resumeErr := state.Store.Resume(ctx, session.ID(choice.Metadata.SessionID))
				if resumeErr != nil {
					choice.Issue = fmt.Errorf("read session %s: %w", choice.Metadata.SessionID, resumeErr)
				} else {
					choice.Unfinished = len(resume.Operations)
				}
			}
		}
		providers := map[string]bool{}
		for _, origin := range choice.Metadata.ResponseOrigins {
			provider, _, _ := strings.Cut(origin, "|")
			if provider != "" {
				providers[provider] = true
			}
		}
		for provider := range providers {
			choice.Providers = append(choice.Providers, provider)
		}
		sort.Strings(choice.Providers)
		choices = append(choices, choice)
	}
	for id, updated := range lastActive {
		if _, exists := withMetadata[id]; !exists {
			choices = append(choices, SessionChoice{
				Metadata: SessionMetadata{SessionID: string(id)}, LastActive: updated,
				Issue: fmt.Errorf("metadata is missing for session %s", id),
			})
		}
	}
	sort.Slice(choices, func(i, j int) bool { return choices[i].LastActive.After(choices[j].LastActive) })
	return choices, nil
}

func (state *SessionState) Rename(ctx context.Context, name string) error {
	if state.lock == nil || state.Current.SessionID == "" {
		return errors.New("no active session")
	}
	name = strings.TrimSpace(name)
	if err := validateSessionName(name); err != nil {
		return err
	}
	choices, err := state.List(ctx)
	if err != nil {
		return err
	}
	for _, choice := range choices {
		if choice.Issue == nil && choice.Metadata.Workspace == state.Workspace && choice.Metadata.Name == name && choice.Metadata.SessionID != state.Current.SessionID {
			return fmt.Errorf("session name %q is already used in this workspace", name)
		}
	}
	state.metadataMu.Lock()
	defer state.metadataMu.Unlock()
	metadata := state.Current
	metadata.ResponseOrigins = cloneOrigins(state.origins)
	metadata.Name = name
	metadata.UpdatedAt = time.Now().UTC()
	if err := state.saveMetadata(metadata); err != nil {
		return err
	}
	state.Current = metadata
	return nil
}

func cloneOrigins(source map[string]string) map[string]string {
	copy := make(map[string]string, len(source))
	for id, origin := range source {
		copy[id] = origin
	}
	return copy
}

// RecordResponseOrigin writes provenance before the response is returned to the
// coordinator, so a subsequently persisted response has durable provenance.
func (state *SessionState) RecordResponseOrigin(responseID, origin string) error {
	if responseID == "" {
		return nil
	}
	state.metadataMu.Lock()
	defer state.metadataMu.Unlock()
	if state.Current.SessionID == "" || state.lock == nil {
		return errors.New("no active session")
	}
	updated := cloneOrigins(state.origins)
	updated[responseID] = origin
	metadata := state.Current
	metadata.ResponseOrigins = updated
	metadata.UpdatedAt = time.Now().UTC()
	if err := state.saveMetadata(metadata); err != nil {
		return err
	}
	state.origins = updated
	return nil
}

func (state *SessionState) ResponseOrigin(responseID string) string {
	state.metadataMu.Lock()
	defer state.metadataMu.Unlock()
	return state.origins[responseID]
}

func validateSessionName(name string) error {
	if name == "" {
		return errors.New("session name is empty")
	}
	if uniseg.GraphemeClusterCount(name) > 80 {
		return errors.New("session name is longer than 80 characters")
	}
	for _, letter := range name {
		if unicode.IsControl(letter) {
			return errors.New("session name contains a control character")
		}
	}
	return nil
}

func (state *SessionState) saveMetadata(value SessionMetadata) error {
	path := state.metadataPath(session.ID(value.SessionID))
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	file, err := os.CreateTemp(filepath.Dir(path), ".metadata-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chmod(0600); err != nil {
		return err
	}
	if _, err := file.Write(encoded); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (state *SessionState) InspectCurrent(ctx context.Context) error {
	if state.Current.SessionID == "" {
		return fs.ErrNotExist
	}
	_, err := state.Store.Inspect(ctx, session.ID(state.Current.SessionID))
	return err
}
