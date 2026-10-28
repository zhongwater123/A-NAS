// Package shares owns immutable member-share copies and private discussions.
// Its Backend is the only source/target access boundary; it must authorize the
// original session carried by ctx rather than impersonating a persisted user.
package shares

import (
	"context"
	"errors"
	"io"
	"time"
)

const MaxMessageBytes = 16 << 10
const MaxEntries = 10000
const MaxPage = 100

var ErrNotFound = errors.New("share not found")
var ErrInactive = errors.New("share is no longer active")
var ErrInvalid = errors.New("invalid share request")
var ErrConflict = errors.New("request key reused with different content")
var ErrBusy = errors.New("share task limit reached")
var ErrUnauthorized = errors.New("share session or account is unavailable")
var ErrRecipientInactive = errors.New("share recipient is unavailable")

// ErrPublicationUnknown means a remote commit may have finished despite a
// lost response. Retrying requires a new task, never assuming nothing saved.
var ErrPublicationUnknown = errors.New("target publication outcome is unknown")

type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}
type Entry struct {
	ID    string `json:"id"`
	Path  string `json:"path"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size"`
}
type Target struct {
	RootID   string `json:"rootId"`
	ParentID string `json:"parentId"`
	ShareID  string `json:"-"`
	JobID    string `json:"-"`
}
type CreateRequest struct {
	RequestID    string   `json:"requestId"`
	SourceIDs    []string `json:"sourceIds"`
	RecipientIDs []string `json:"recipientIds"`
	Message      string   `json:"message"`
}
type SaveRequest struct {
	RequestID string   `json:"requestId"`
	EntryIDs  []string `json:"entryIds"`
	Target    Target   `json:"target"`
}
type Recipient struct {
	UserID            string     `json:"userId"`
	ViewedAt          *time.Time `json:"viewedAt,omitempty"`
	ReadThrough       int64      `json:"readThrough"`
	SenderReadThrough int64      `json:"senderReadThrough"`
	Saved             bool       `json:"saved"`
	SavedFiles        int        `json:"savedFiles"`
	SaveUncertain     bool       `json:"saveUncertain"`
	LastMessage       int64      `json:"lastMessage"`
}
type Share struct {
	ID            string      `json:"id"`
	Sender        User        `json:"sender"`
	Status        string      `json:"status"`
	CreatedAt     time.Time   `json:"createdAt"`
	PublishedAt   *time.Time  `json:"publishedAt,omitempty"`
	ExpiresAt     *time.Time  `json:"expiresAt,omitempty"`
	Entries       []Entry     `json:"entries"`
	Recipients    []Recipient `json:"recipients"`
	Error         string      `json:"error,omitempty"`
	PreparedBytes int64       `json:"preparedBytes"`
}
type Message struct {
	ID        int64     `json:"id"`
	Sender    User      `json:"sender"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"createdAt"`
}
type Job struct {
	ID           string     `json:"id"`
	ShareID      string     `json:"shareId"`
	UserID       string     `json:"userId"`
	Status       string     `json:"status"`
	CreatedAt    time.Time  `json:"createdAt"`
	FinishedAt   *time.Time `json:"finishedAt,omitempty"`
	BytesDone    int64      `json:"bytesDone"`
	BytesTotal   int64      `json:"bytesTotal"`
	CompletedIDs []string   `json:"completedIds"`
	EntryIDs     []string   `json:"entryIds"`
	Error        string     `json:"error,omitempty"`
}
type Notification struct {
	ID          int64     `json:"id"`
	ShareID     string    `json:"shareId"`
	RecipientID string    `json:"recipientId"`
	Kind        string    `json:"kind"`
	MessageID   int64     `json:"messageId,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}

// Capture must detect source tree changes and provide stable single-file content.
// Save stages privately, calls commit for each publication, and cleans its own
// temporary files on every error. Never publish outside the commit callback.
type Backend interface {
	Capture(context.Context, User, []string, func(Entry, io.Reader) error) error
	Save(context.Context, User, Target, []Entry, func(string) (io.ReadCloser, error), func(string, func() error) error, func(int64)) error
}
type Options struct {
	Now     func() time.Time
	Backend Backend
	Active  func(context.Context, string) (bool, error)
	// ActiveAccounts supplies one authoritative snapshot for a request or
	// maintenance cycle. Active remains the single-user check for running jobs.
	ActiveAccounts func(context.Context) (map[string]bool, error)
	Authorize      func(context.Context, User) error
	CheckVolume    func(context.Context) error
	MaxTasks       int
	RequireClone   bool
}
