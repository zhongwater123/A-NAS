package engine

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/zhongwater123/A-NAS/internal/appstore"
)

// Generated app secrets live in the agent's state directory, one file per
// app, readable only by the container agent. They are made the first time
// the app's plan is shown and kept across uninstalls, so a reinstall that
// keeps its data keeps its logins.

func (s *Store) secretsPath(id string) string {
	return filepath.Join(s.stateDir, "secrets", id+".json")
}

// readSecrets returns an app's stored secrets; none when it has no file.
func (s *Store) readSecrets(id string) (appstore.Secrets, error) {
	data, err := os.ReadFile(s.secretsPath(id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var secrets appstore.Secrets
	if err := json.Unmarshal(data, &secrets); err != nil {
		return nil, err
	}
	return secrets, nil
}

// appSecrets returns an app's secrets, generating and storing the ones it
// does not have yet.
func (s *Store) appSecrets(id string) (appstore.Secrets, error) {
	s.secretsMu.Lock()
	defer s.secretsMu.Unlock()
	stored, err := s.readSecrets(id)
	if err != nil {
		return nil, err
	}
	secrets, changed := appstore.NewSecrets(id, stored)
	if !changed {
		return secrets, nil
	}
	data, err := json.Marshal(secrets)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(s.secretsPath(id)), 0o700); err != nil {
		return nil, err
	}
	temporary := s.secretsPath(id) + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(temporary, s.secretsPath(id)); err != nil {
		return nil, err
	}
	return secrets, nil
}
