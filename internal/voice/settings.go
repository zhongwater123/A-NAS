package voice

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

// silence is one second of 16 kHz mono Ogg Opus silence: a connection test
// that costs a second of recognition and that the service answers with
// "静音音频" once the credentials and the relay are right.
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
	settingRegion      = "tos.region"
	settingBucket      = "tos.bucket"
	settingAccessKeyID = "tos.access_key_id"
	settingSecretKey   = "tos.secret_access_key"
)

// RecognizerSettings is what administrators see of the recognition
// service's credentials: never the secret, only its last characters.
type RecognizerSettings struct {
	Provider   string           `json:"provider"`
	Configured bool             `json:"configured"`
	Mode       string           `json:"mode,omitempty"`
	AppID      string           `json:"appId,omitempty"`
	SecretHint string           `json:"secretHint,omitempty"`
	Storage    *StorageSettings `json:"storage,omitempty"`
	// Regions are the TOS regions to choose from.
	Regions   []string   `json:"regions"`
	UpdatedBy string     `json:"updatedBy,omitempty"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
}

// StorageSettings are the saved relay bucket, its access key masked.
type StorageSettings struct {
	Region        string `json:"region"`
	Bucket        string `json:"bucket"`
	AccessKeyHint string `json:"accessKeyHint"`
	SecretHint    string `json:"secretHint"`
}

// RecognizerCredentials are what an administrator enters.
type RecognizerCredentials struct {
	Mode        string       `json:"mode"`
	APIKey      string       `json:"apiKey"`
	AppID       string       `json:"appId"`
	AccessToken string       `json:"accessToken"`
	Storage     StorageInput `json:"storage"`
}

// StorageInput is the relay bucket an administrator enters.
type StorageInput struct {
	Region          string `json:"region"`
	Bucket          string `json:"bucket"`
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
}

func (c RecognizerCredentials) clean() (RecognizerCredentials, error) {
	trimmed := RecognizerCredentials{Mode: c.Mode, APIKey: strings.TrimSpace(c.APIKey), AppID: strings.TrimSpace(c.AppID), AccessToken: strings.TrimSpace(c.AccessToken),
		Storage: StorageInput{Region: strings.TrimSpace(c.Storage.Region), Bucket: strings.TrimSpace(c.Storage.Bucket),
			AccessKeyID: strings.TrimSpace(c.Storage.AccessKeyID), SecretAccessKey: strings.TrimSpace(c.Storage.SecretAccessKey)}}
	valid := func(value string) bool {
		return value != "" && utf8.RuneCountInString(value) <= 200 && !strings.ContainsAny(value, " \t\r\n")
	}
	if !slices.Contains(StorageRegions(), trimmed.Storage.Region) {
		return RecognizerCredentials{}, fmt.Errorf("%w: the TOS region", ErrInvalid)
	}
	if !bucketName.MatchString(trimmed.Storage.Bucket) {
		return RecognizerCredentials{}, fmt.Errorf("%w: the TOS bucket name", ErrInvalid)
	}
	if !valid(trimmed.Storage.AccessKeyID) || !valid(trimmed.Storage.SecretAccessKey) {
		return RecognizerCredentials{}, fmt.Errorf("%w: the TOS access key", ErrInvalid)
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
	return DoubaoCredentials{APIKey: c.APIKey, AppID: c.AppID, AccessToken: c.AccessToken, Storage: StorageCredentials{
		Region: c.Storage.Region, Bucket: c.Storage.Bucket, AccessKeyID: c.Storage.AccessKeyID, SecretAccessKey: c.Storage.SecretAccessKey}}
}

// recognizerSettingKeys select the recognizer's and the relay's settings.
const recognizerSettingKeys = "key LIKE 'doubao.%' OR key LIKE 'tos.%'"

// DoubaoCredentials reads the saved credentials for the recognizer.
func (s *Store) DoubaoCredentials(ctx context.Context) (DoubaoCredentials, error) {
	values := map[string]string{}
	rows, err := s.db.QueryContext(ctx, "SELECT key, value FROM settings WHERE "+recognizerSettingKeys)
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
	credentials := DoubaoCredentials{Storage: StorageCredentials{Region: values[settingRegion], Bucket: values[settingBucket],
		AccessKeyID: values[settingAccessKeyID], SecretAccessKey: values[settingSecretKey]}}
	switch values[settingMode] {
	case ModeAPIKey:
		credentials.APIKey = values[settingAPIKey]
	case ModeAppKey:
		credentials.AppID, credentials.AccessToken = values[settingAppID], values[settingAccessToken]
	}
	return credentials, nil
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
	settings := RecognizerSettings{Provider: "doubao", Regions: StorageRegions()}
	credentials, err := s.store.DoubaoCredentials(ctx)
	if err != nil {
		return RecognizerSettings{}, err
	}
	switch {
	case credentials.APIKey != "":
		settings.Mode, settings.SecretHint = ModeAPIKey, hint(credentials.APIKey)
	case credentials.AppID != "" && credentials.AccessToken != "":
		settings.Mode, settings.AppID, settings.SecretHint = ModeAppKey, credentials.AppID, hint(credentials.AccessToken)
	}
	if storage := credentials.Storage; storage.usable() {
		settings.Storage = &StorageSettings{Region: storage.Region, Bucket: storage.Bucket,
			AccessKeyHint: hint(storage.AccessKeyID), SecretHint: hint(storage.SecretAccessKey)}
	}
	settings.Configured = credentials.usable()
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
	if _, err := tx.ExecContext(ctx, "DELETE FROM settings WHERE "+recognizerSettingKeys); err != nil {
		return RecognizerSettings{}, err
	}
	now := formatTime(s.now())
	values := map[string]string{settingMode: cleaned.Mode, settingAPIKey: cleaned.APIKey, settingAppID: cleaned.AppID, settingAccessToken: cleaned.AccessToken,
		settingRegion: cleaned.Storage.Region, settingBucket: cleaned.Storage.Bucket, settingAccessKeyID: cleaned.Storage.AccessKeyID, settingSecretKey: cleaned.Storage.SecretAccessKey}
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
	s.logger.Info("voice recognition credentials changed", "by", actor.Username, "mode", cleaned.Mode, "region", cleaned.Storage.Region, "bucket", cleaned.Storage.Bucket)
	kick(s.audio)
	return s.RecognizerSettings(ctx, actor)
}

// ClearRecognizer forgets the credentials; new recordings wait for new ones.
func (s *Service) ClearRecognizer(ctx context.Context, actor accounts.User) error {
	if actor.Role != accounts.RoleAdmin {
		return ErrForbidden
	}
	if _, err := s.store.db.ExecContext(ctx, "DELETE FROM settings WHERE "+recognizerSettingKeys); err != nil {
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
	// Queued: the service took the test task but had not finished it.
	Queued bool `json:"queued,omitempty"`
}

// testWait is how long a connection test waits for the service to answer
// the second of silence.
var testWait = 20 * time.Second

// TestRecognizer relays a second of silence through TOS and submits it, with
// the given credentials or the saved ones when none are given.
// Administrators only.
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
	ctx, cancel := context.WithTimeout(ctx, testWait+40*time.Second)
	defer cancel()
	task, err := doubao.Submit(ctx, silence, RecognizeRequest{Duration: time.Second})
	if err != nil {
		return testOutcome(err), nil
	}
	defer func() {
		release, done := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer done()
		if err := doubao.Release(release, task); err != nil {
			s.logger.Warn("delete the relayed test audio", "error", err)
		}
	}()
	deadline := time.Now().Add(testWait)
	for {
		_, err := doubao.Result(ctx, task)
		if !errors.Is(err, ErrRecognizing) {
			return testOutcome(err), nil
		}
		if time.Now().After(deadline) {
			// The credentials and the relay work; the queue is long.
			return RecognizerTest{OK: true, Queued: true}, nil
		}
		select {
		case <-ctx.Done():
			return testOutcome(ctx.Err()), nil
		case <-time.After(min(2*time.Second, testWait/4)):
		}
	}
}

func testOutcome(err error) RecognizerTest {
	switch {
	case err == nil:
		return RecognizerTest{OK: true}
	case errors.Is(err, ErrRecognizerUnconfigured):
		return RecognizerTest{Code: "unconfigured", Message: "还没有填写密钥"}
	case errors.Is(err, ErrRelayRejected):
		return RecognizerTest{Code: "storage_rejected", Message: err.Error()}
	case errors.Is(err, ErrRecognizerRejected):
		return RecognizerTest{Code: "rejected", Message: err.Error()}
	case errors.Is(err, ErrRecognizerBusy):
		code := "unreachable"
		if strings.Contains(err.Error(), "object storage") {
			code = "storage_unreachable"
		}
		return RecognizerTest{Code: code, Message: err.Error()}
	}
	return RecognizerTest{Code: "failed", Message: err.Error()}
}
