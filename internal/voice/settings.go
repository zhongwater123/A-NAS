package voice

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

// silence is one second of 16 kHz mono Ogg Opus silence: a connection test
// that costs a second of recognition and that the service answers with
// "静音音频" once the credentials are right.
//
//go:embed silence.ogg
var silence []byte

// Credential modes of the Volcengine console.
const (
	// ModeAPIKey: the new console's API Key.
	ModeAPIKey = "apiKey"
	// ModeAppKey: the old console's APP ID and Access Token.
	ModeAppKey = "appKey"
)

const (
	settingMode        = "doubao.mode"
	settingAPIKey      = "doubao.api_key"
	settingAppID       = "doubao.app_id"
	settingAccessToken = "doubao.access_token"
)

// RecognizerSettings is what administrators see of the recognition
// service's credentials: never the secret, only its last characters.
type RecognizerSettings struct {
	Provider   string     `json:"provider"`
	Configured bool       `json:"configured"`
	Mode       string     `json:"mode,omitempty"`
	AppID      string     `json:"appId,omitempty"`
	SecretHint string     `json:"secretHint,omitempty"`
	UpdatedBy  string     `json:"updatedBy,omitempty"`
	UpdatedAt  *time.Time `json:"updatedAt,omitempty"`
}

// RecognizerCredentials are what an administrator enters.
type RecognizerCredentials struct {
	Mode        string `json:"mode"`
	APIKey      string `json:"apiKey"`
	AppID       string `json:"appId"`
	AccessToken string `json:"accessToken"`
}

func (c RecognizerCredentials) clean() (RecognizerCredentials, error) {
	trimmed := RecognizerCredentials{Mode: c.Mode, APIKey: strings.TrimSpace(c.APIKey), AppID: strings.TrimSpace(c.AppID), AccessToken: strings.TrimSpace(c.AccessToken)}
	valid := func(value string) bool {
		return value != "" && utf8.RuneCountInString(value) <= 200 && !strings.ContainsAny(value, " \t\r\n")
	}
	switch trimmed.Mode {
	case ModeAPIKey:
		if !valid(trimmed.APIKey) {
			return RecognizerCredentials{}, fmt.Errorf("%w: the API Key", ErrInvalid)
		}
		trimmed.AppID, trimmed.AccessToken = "", ""
	case ModeAppKey:
		if !valid(trimmed.AppID) || !valid(trimmed.AccessToken) {
			return RecognizerCredentials{}, fmt.Errorf("%w: the APP ID and Access Token", ErrInvalid)
		}
		trimmed.APIKey = ""
	default:
		return RecognizerCredentials{}, fmt.Errorf("%w: credential mode", ErrInvalid)
	}
	return trimmed, nil
}

func (c RecognizerCredentials) doubao() DoubaoCredentials {
	return DoubaoCredentials{APIKey: c.APIKey, AppID: c.AppID, AccessToken: c.AccessToken}
}

// DoubaoCredentials reads the saved credentials for the recognizer.
func (s *Store) DoubaoCredentials(ctx context.Context) (DoubaoCredentials, error) {
	values := map[string]string{}
	rows, err := s.db.QueryContext(ctx, "SELECT key, value FROM settings WHERE key LIKE 'doubao.%'")
	if err != nil {
		return DoubaoCredentials{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return DoubaoCredentials{}, err
		}
		values[key] = value
	}
	if err := rows.Err(); err != nil {
		return DoubaoCredentials{}, err
	}
	switch values[settingMode] {
	case ModeAPIKey:
		return DoubaoCredentials{APIKey: values[settingAPIKey]}, nil
	case ModeAppKey:
		return DoubaoCredentials{AppID: values[settingAppID], AccessToken: values[settingAccessToken]}, nil
	}
	return DoubaoCredentials{}, nil
}

func hint(secret string) string {
	runes := []rune(secret)
	if len(runes) <= 4 {
		return "••••"
	}
	return "••••" + string(runes[len(runes)-4:])
}

// RecognizerSettings returns the saved credentials, masked. Administrators only.
func (s *Service) RecognizerSettings(ctx context.Context, actor accounts.User) (RecognizerSettings, error) {
	if actor.Role != accounts.RoleAdmin {
		return RecognizerSettings{}, ErrForbidden
	}
	settings := RecognizerSettings{Provider: "doubao"}
	credentials, err := s.store.DoubaoCredentials(ctx)
	if err != nil {
		return RecognizerSettings{}, err
	}
	switch {
	case credentials.APIKey != "":
		settings.Configured, settings.Mode, settings.SecretHint = true, ModeAPIKey, hint(credentials.APIKey)
	case credentials.AppID != "" && credentials.AccessToken != "":
		settings.Configured, settings.Mode, settings.AppID, settings.SecretHint = true, ModeAppKey, credentials.AppID, hint(credentials.AccessToken)
	}
	if settings.Configured {
		var by, at string
		if err := s.store.db.QueryRowContext(ctx, "SELECT updated_by, updated_at FROM settings WHERE key = ?", settingMode).Scan(&by, &at); err == nil {
			settings.UpdatedBy, settings.UpdatedAt = by, optionalTime(at)
		}
	}
	return settings, nil
}

// SetRecognizer saves new credentials and lets waiting recordings go on.
func (s *Service) SetRecognizer(ctx context.Context, actor accounts.User, credentials RecognizerCredentials) (RecognizerSettings, error) {
	if actor.Role != accounts.RoleAdmin {
		return RecognizerSettings{}, ErrForbidden
	}
	cleaned, err := credentials.clean()
	if err != nil {
		return RecognizerSettings{}, err
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return RecognizerSettings{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM settings WHERE key LIKE 'doubao.%'"); err != nil {
		return RecognizerSettings{}, err
	}
	now := formatTime(s.now())
	values := map[string]string{settingMode: cleaned.Mode, settingAPIKey: cleaned.APIKey, settingAppID: cleaned.AppID, settingAccessToken: cleaned.AccessToken}
	for key, value := range values {
		if value == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO settings (key, value, updated_by, updated_at) VALUES (?, ?, ?, ?)", key, value, actor.Username, now); err != nil {
			return RecognizerSettings{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return RecognizerSettings{}, err
	}
	s.logger.Info("voice recognition credentials changed", "by", actor.Username, "mode", cleaned.Mode)
	kick(s.audio)
	return s.RecognizerSettings(ctx, actor)
}

// ClearRecognizer forgets the credentials; new recordings wait for new ones.
func (s *Service) ClearRecognizer(ctx context.Context, actor accounts.User) error {
	if actor.Role != accounts.RoleAdmin {
		return ErrForbidden
	}
	if _, err := s.store.db.ExecContext(ctx, "DELETE FROM settings WHERE key LIKE 'doubao.%'"); err != nil {
		return err
	}
	s.logger.Info("voice recognition credentials removed", "by", actor.Username)
	return nil
}

// RecognizerTest is the outcome of a connection test.
type RecognizerTest struct {
	OK      bool   `json:"ok"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// TestRecognizer sends a second of silence with the given credentials, or
// with the saved ones when none are given. Administrators only.
func (s *Service) TestRecognizer(ctx context.Context, actor accounts.User, credentials *RecognizerCredentials) (RecognizerTest, error) {
	if actor.Role != accounts.RoleAdmin {
		return RecognizerTest{}, ErrForbidden
	}
	doubao, ok := s.recognizer.(Doubao)
	if !ok {
		if s.recognizer == nil {
			return RecognizerTest{}, fmt.Errorf("%w: no recognizer", ErrUnavailable)
		}
		// A development fake accepts anything.
		return RecognizerTest{OK: true}, nil
	}
	if credentials != nil {
		cleaned, err := credentials.clean()
		if err != nil {
			return RecognizerTest{}, err
		}
		doubao.Credentials = func(context.Context) (DoubaoCredentials, error) { return cleaned.doubao(), nil }
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err := doubao.Recognize(ctx, silence, RecognizeRequest{Duration: time.Second})
	switch {
	case err == nil:
		return RecognizerTest{OK: true}, nil
	case errors.Is(err, ErrRecognizerUnconfigured):
		return RecognizerTest{Code: "unconfigured", Message: "还没有填写密钥"}, nil
	case errors.Is(err, ErrRecognizerRejected):
		return RecognizerTest{Code: "rejected", Message: err.Error()}, nil
	case errors.Is(err, ErrRecognizerBusy):
		return RecognizerTest{Code: "unreachable", Message: err.Error()}, nil
	}
	return RecognizerTest{Code: "failed", Message: err.Error()}, nil
}
