package report

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// DiskStore writes RunResult as JSON files to a lazily-created temp directory.
type DiskStore struct {
	mu  sync.Mutex
	dir string
}

// NewDiskStore creates a new DiskStore. The underlying temp directory
// is created lazily on the first Save.
func NewDiskStore() *DiskStore {
	return &DiskStore{}
}

// Save writes a RunResult as a JSON file to disk.
func (s *DiskStore) Save(result *RunResult) error {
	dir, err := s.ensureDir()
	if err != nil {
		return err
	}
	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshalling result %s: %w", result.ID, err)
	}
	path := filepath.Join(dir, result.ID+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing result %s: %w", result.ID, err)
	}
	return nil
}

// Load reads a RunResult from disk.
func (s *DiskStore) Load(runID string) (*RunResult, error) {
	dir, err := s.ensureDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, runID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading result %s: %w", runID, err)
	}
	var result RunResult
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("unmarshalling result %s: %w", runID, err)
	}
	return &result, nil
}

func (s *DiskStore) ensureDir() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dir != "" {
		return s.dir, nil
	}
	dir, err := os.MkdirTemp("", "governor-runs-*")
	if err != nil {
		return "", fmt.Errorf("creating result directory: %w", err)
	}
	s.dir = dir
	return dir, nil
}
