package appserver

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type threadMetadata struct {
	ID               string `json:"id"`
	WorkspaceRoot    string `json:"workspace_root"`
	WorkingDirectory string `json:"working_directory"`
	CreatedAtMS      int64  `json:"created_at_ms"`
	Title            string `json:"title,omitempty"`
}

type threadStore struct {
	directory string
}

func newThreadStore(dataRoot string) threadStore {
	return threadStore{directory: filepath.Join(dataRoot, "tmp", "threads")}
}

func (store threadStore) rolloutPath(threadID string) string {
	return filepath.Join(store.directory, threadID+".jsonl")
}

func (store threadStore) remove(threadID string) {
	_ = os.Remove(filepath.Join(store.directory, threadID+".json"))
	_ = os.Remove(store.rolloutPath(threadID))
}

func (store threadStore) hiddenPath(threadID string) string {
	return filepath.Join(store.directory, threadID+".hidden")
}

func (store threadStore) hide(threadID string) {
	if err := os.MkdirAll(store.directory, 0o755); err != nil {
		return
	}
	_ = os.WriteFile(store.hiddenPath(threadID), nil, 0o600)
}

func (store threadStore) unhide(threadID string) {
	_ = os.Remove(store.hiddenPath(threadID))
}

func (store threadStore) hidden(threadID string) bool {
	_, err := os.Stat(store.hiddenPath(threadID))
	return err == nil
}

func (store threadStore) create(workspaceRoot string) (threadMetadata, error) {
	workspaceRoot, err := validateWorkspace(workspaceRoot)
	if err != nil {
		return threadMetadata{}, err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return threadMetadata{}, fmt.Errorf("generate thread id: %w", err)
	}
	metadata := threadMetadata{
		ID: hex.EncodeToString(random[:]), WorkspaceRoot: workspaceRoot,
		WorkingDirectory: workspaceRoot, CreatedAtMS: time.Now().UnixMilli(),
	}
	if err := store.write(metadata); err != nil {
		return threadMetadata{}, err
	}
	return metadata, nil
}

func (store threadStore) list() ([]threadMetadata, error) {
	entries, err := os.ReadDir(store.directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read threads: %w", err)
	}
	threads := make([]threadMetadata, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(store.directory, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read thread %s: %w", entry.Name(), err)
		}
		var metadata threadMetadata
		if err := json.Unmarshal(contents, &metadata); err != nil {
			return nil, fmt.Errorf("decode thread %s: %w", entry.Name(), err)
		}
		threads = append(threads, metadata)
	}
	sort.Slice(threads, func(left, right int) bool {
		return threads[left].CreatedAtMS > threads[right].CreatedAtMS
	})
	return threads, nil
}

func (store threadStore) write(metadata threadMetadata) error {
	if err := os.MkdirAll(store.directory, 0o755); err != nil {
		return fmt.Errorf("create threads directory: %w", err)
	}
	contents, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("encode thread: %w", err)
	}
	path := filepath.Join(store.directory, metadata.ID+".json")
	temporaryPath := path + ".tmp"
	if err := os.WriteFile(temporaryPath, append(contents, '\n'), 0o600); err != nil {
		return fmt.Errorf("write thread: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace thread: %w", err)
	}
	return nil
}

func validateWorkspace(workspaceRoot string) (string, error) {
	if workspaceRoot == "" {
		return "", errors.New("workspace_root is required")
	}
	workspaceRoot, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return "", fmt.Errorf("resolve workspace_root: %w", err)
	}
	workspaceRoot, err = filepath.EvalSymlinks(workspaceRoot)
	if err != nil {
		return "", fmt.Errorf("canonicalize workspace_root: %w", err)
	}
	info, err := os.Stat(workspaceRoot)
	if err != nil {
		return "", fmt.Errorf("open workspace_root: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("workspace_root must be a directory")
	}
	return workspaceRoot, nil
}
