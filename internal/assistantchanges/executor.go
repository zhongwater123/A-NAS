package assistantchanges

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/zhongwater123/A-NAS/internal/files"
)

const (
	// MaxContent bounds one staged file; the assistant writes documents.
	MaxContent = 64 << 20
	// previewLimit bounds the text a card shows of each file.
	previewLimit = 64 << 10
	// GrantTTL is how long a grant waits for the call Hermes makes right after
	// the member's answer; a grant nobody used by then is void.
	GrantTTL = 2 * time.Minute
	// keepChanges is how many changes of an account are kept, newest first.
	keepChanges = 200
)

// Change statuses.
const (
	StatusAwaiting = "awaiting"
	StatusApproved = "approved"
	StatusApplying = "applying"
	StatusApplied  = "applied"
	StatusFailed   = "failed"
	StatusRejected = "rejected"
	StatusExpired  = "expired"
)

var (
	ErrNotFound = errors.New("no such change")
	ErrDecided  = errors.New("这项更改已经处理过了")
)

// Executor keeps the account's changes and the copies their cards show in
// Root, which only root may read, and applies them on the data volume at
// Volume through the member's own files.FileSystem.
type Executor struct {
	Root   string
	Volume string
	Now    func() time.Time

	mu sync.Mutex
}

// fingerprint is what a target was when the card was made; applying refuses a
// target that is no longer the same.
type fingerprint struct {
	Exists  bool   `json:"exists"`
	Kind    string `json:"kind,omitempty"`
	Size    int64  `json:"size,omitempty"`
	Inode   uint64 `json:"inode,omitempty"`
	ModTime int64  `json:"modTime,omitempty"`
}

type step struct {
	Op         string      `json:"op"`
	Path       string      `json:"path"`
	To         string      `json:"to,omitempty"`
	Rel        string      `json:"rel"`
	ToRel      string      `json:"toRel,omitempty"`
	Trash      string      `json:"trash"`
	Before     fingerprint `json:"before"`
	BeforeText *string     `json:"beforeText,omitempty"`
	// A copy's source, and what it was when the card was made.
	Source       string       `json:"source,omitempty"`
	SourceRel    string       `json:"sourceRel,omitempty"`
	SourceBefore *fingerprint `json:"sourceBefore,omitempty"`
	SourceSize   int64        `json:"sourceSize,omitempty"`
}

type change struct {
	ID        string `json:"id"`
	Digest    string `json:"digest"`
	Summary   string `json:"summary"`
	Steps     []step `json:"steps"`
	Status    string `json:"status"`
	SessionID string `json:"sessionId,omitempty"`
	RequestID string `json:"requestId,omitempty"`
	CreatedAt int64  `json:"createdAt"`
	DecidedAt int64  `json:"decidedAt,omitempty"`
	AppliedAt int64  `json:"appliedAt,omitempty"`
	Done      int    `json:"done,omitempty"`
	Error     string `json:"error,omitempty"`
}

func (e *Executor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// Prepare checks a submission against what the member's paths hold now and
// copies what it would write. It returns the card; ErrInvalid lists why a
// submission cannot be applied.
func (e *Executor) Prepare(ctx context.Context, account string, member files.FileSystem, raw []byte, sessionID, requestID string) (json.RawMessage, error) {
	if !accountPattern.MatchString(account) {
		return nil, errors.New("invalid account")
	}
	submission, err := ParseSubmission(raw)
	if err != nil {
		return nil, invalid{err.Error()}
	}
	steps, contents, err := e.check(ctx, account, member, submission)
	if err != nil {
		return nil, err
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	c := change{ID: id, Digest: submission.Digest(), Summary: submission.Summary, Steps: steps, Status: StatusAwaiting,
		SessionID: sessionID, RequestID: requestID, CreatedAt: e.now().Unix()}
	e.mu.Lock()
	defer e.mu.Unlock()
	dir, err := e.snapshotDir(account, id)
	if err != nil {
		return nil, err
	}
	for n, content := range contents {
		if content != nil {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprint(n)), content, 0o600); err != nil {
				return nil, err
			}
		}
	}
	changes, err := e.load(account)
	if err != nil {
		return nil, err
	}
	changes = append(changes, c)
	if err := e.save(account, changes); err != nil {
		return nil, err
	}
	return e.preview(account, c)
}

// Decide approves, rejects or expires a change. Approving records the
// member's grant; only an awaiting change can be approved or rejected.
func (e *Executor) Decide(account, id, decision string) (json.RawMessage, error) {
	moves := map[string]struct {
		from []string
		to   string
	}{
		"approve": {[]string{StatusAwaiting}, StatusApproved},
		"reject":  {[]string{StatusAwaiting}, StatusRejected},
		"expire":  {[]string{StatusAwaiting, StatusApproved}, StatusExpired},
	}
	move, ok := moves[decision]
	if !ok || !accountPattern.MatchString(account) {
		return nil, ErrNotFound
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	changes, err := e.load(account)
	if err != nil {
		return nil, err
	}
	for i := range changes {
		if changes[i].ID != id {
			continue
		}
		allowed := false
		for _, from := range move.from {
			allowed = allowed || changes[i].Status == from
		}
		if !allowed {
			return nil, ErrDecided
		}
		changes[i].Status, changes[i].DecidedAt = move.to, e.now().Unix()
		if err := e.save(account, changes); err != nil {
			return nil, err
		}
		return e.preview(account, changes[i])
	}
	return nil, ErrNotFound
}

// Get returns a change's card as it is now.
func (e *Executor) Get(account, id string) (json.RawMessage, error) {
	if !accountPattern.MatchString(account) {
		return nil, ErrNotFound
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	changes, err := e.load(account)
	if err != nil {
		return nil, err
	}
	for _, c := range changes {
		if c.ID == id {
			return e.preview(account, c)
		}
	}
	return nil, ErrNotFound
}

// Apply is the MCP tool's call: it applies the change the member approved
// with exactly these arguments, once, from the copies the member saw, and says
// what happened in words for the assistant.
func (e *Executor) Apply(ctx context.Context, account string, member files.FileSystem, raw []byte) string {
	if !accountPattern.MatchString(account) {
		return "这批更改没有得到用户本人的确认，什么都没有改变。"
	}
	submission, err := ParseSubmission(raw)
	if err != nil {
		return "提交被拒绝，什么都没有改变：\n" + err.Error()
	}
	want, now := submission.Digest(), e.now()
	e.mu.Lock()
	changes, err := e.load(account)
	var found *change
	if err == nil {
		for i := len(changes) - 1; i >= 0; i-- {
			c := &changes[i]
			if c.Status == StatusApproved && c.Digest == want && now.Sub(time.Unix(c.DecidedAt, 0)) <= GrantTTL {
				c.Status = StatusApplying
				found = c
				err = e.save(account, changes)
				break
			}
		}
	}
	var granted change
	if found != nil {
		granted = *found
	}
	e.mu.Unlock()
	if err != nil {
		return "暂时无法执行更改，什么都没有改变。"
	}
	if found == nil {
		// The product service lets an invalid submission through without a card, so
		// the assistant learns what is wrong with it instead of hearing that the
		// member said no.
		if _, _, err := e.check(ctx, account, member, submission); err != nil {
			return "提交被拒绝，什么都没有改变：\n" + strings.TrimPrefix(err.Error(), ErrInvalid.Error()+"：\n")
		}
		return "这批更改没有得到用户本人的确认，什么都没有改变。"
	}
	done, failure := e.apply(ctx, account, member, granted)
	e.mu.Lock()
	if changes, err := e.load(account); err == nil {
		for i := range changes {
			if changes[i].ID == granted.ID {
				changes[i].Status, changes[i].AppliedAt, changes[i].Done = StatusApplied, e.now().Unix(), done
				if failure != "" {
					changes[i].Status, changes[i].Error = StatusFailed, failure
				}
			}
		}
		_ = e.save(account, changes)
	}
	e.mu.Unlock()
	total := len(granted.Steps)
	switch {
	case failure == "":
		return fmt.Sprintf("用户已确认，%d 项更改已执行（编号 %s）。被覆盖和删除的旧文件在用户的回收站中。", total, granted.ID)
	case done == 0:
		return fmt.Sprintf("用户已确认，但更改没有执行（编号 %s）：%s。什么都没有改变。", granted.ID, failure)
	default:
		return fmt.Sprintf("用户已确认，执行到第 %d 项时失败（编号 %s）：%s。前 %d 项已经生效，后面的没有执行。", done+1, granted.ID, failure, done)
	}
}

// Recent lists the account's latest changes for the assistant.
func (e *Executor) Recent(account string) string {
	if !accountPattern.MatchString(account) {
		return "最近没有更改。"
	}
	e.mu.Lock()
	changes, err := e.load(account)
	e.mu.Unlock()
	if err != nil || len(changes) == 0 {
		return "最近没有更改。"
	}
	names := map[string]string{StatusAwaiting: "等待确认", StatusApproved: "已确认", StatusApplying: "执行中", StatusApplied: "已执行",
		StatusFailed: "执行失败", StatusRejected: "被拒绝", StatusExpired: "未确认"}
	sort.SliceStable(changes, func(i, j int) bool { return changes[i].CreatedAt > changes[j].CreatedAt })
	var lines []string
	for i, c := range changes {
		if i == 10 {
			break
		}
		lines = append(lines, fmt.Sprintf("%s %s：%s（编号 %s）", time.Unix(c.CreatedAt, 0).Format("01-02 15:04"), names[c.Status], c.Summary, c.ID))
	}
	return strings.Join(lines, "\n")
}

// check validates a submission against what the member's paths hold now and
// returns the planned steps with the staged contents to write.
func (e *Executor) check(ctx context.Context, account string, member files.FileSystem, submission Submission) ([]step, [][]byte, error) {
	var problems invalid
	if len(submission.Operations) == 0 {
		problems = append(problems, "没有任何操作")
	}
	var steps []step
	var contents [][]byte
	for i, op := range submission.Operations {
		prefix := fmt.Sprintf("第 %d 项：", i+1)
		s, content, problem := e.plan(ctx, account, member, op)
		if problem != "" {
			problems = append(problems, prefix+problem)
			continue
		}
		steps, contents = append(steps, s), append(contents, content)
	}
	if len(problems) > 0 {
		return nil, nil, problems
	}
	return steps, contents, nil
}

func (e *Executor) plan(ctx context.Context, account string, member files.FileSystem, op Operation) (step, []byte, string) {
	switch op.Op {
	case "write", "copy", "delete", "move", "mkdir":
	default:
		return step{}, nil, fmt.Sprintf("未知操作 %q", op.Op)
	}
	target, err := clean(op.Path)
	if err != nil {
		return step{}, nil, err.Error()
	}
	rel, err := volumePath(account, target, true)
	if err != nil {
		return step{}, nil, err.Error()
	}
	s := step{Op: op.Op, Path: target, Rel: rel, Trash: trashFor(account, target)}
	var head []byte
	s.Before, head, err = e.fingerprint(ctx, member, rel)
	if err != nil {
		return step{}, nil, describe(err, target)
	}
	if (op.Op == "delete" || op.Op == "move") && !s.Before.Exists {
		return step{}, nil, target + " 不存在"
	}
	if (op.Op == "write" || op.Op == "copy") && s.Before.Exists && s.Before.Kind != files.KindFile {
		return step{}, nil, target + " 不是普通文件"
	}
	if head != nil {
		s.BeforeText = preview(head)
	}
	var content []byte
	switch op.Op {
	case "move":
		if s.To, err = clean(op.To); err != nil {
			return step{}, nil, err.Error()
		}
		if s.ToRel, err = volumePath(account, s.To, true); err != nil {
			return step{}, nil, err.Error()
		}
		to, _, err := e.fingerprint(ctx, member, s.ToRel)
		if err != nil {
			return step{}, nil, describe(err, s.To)
		}
		if to.Exists {
			return step{}, nil, s.To + " 已经存在"
		}
	case "copy":
		// Copied as the member when applied, from wherever the assistant reads;
		// nothing is staged, so a file of any size can be copied.
		if s.Source, err = clean(op.Source); err != nil {
			return step{}, nil, err.Error()
		}
		if s.SourceRel, err = sourcePath(account, s.Source); err != nil {
			return step{}, nil, err.Error()
		}
		from, _, err := e.fingerprint(ctx, member, s.SourceRel)
		switch {
		case err != nil:
			return step{}, nil, describe(err, s.Source)
		case !from.Exists:
			return step{}, nil, s.Source + " 不存在"
		case from.Kind != files.KindFile && from.Kind != files.KindDirectory:
			return step{}, nil, s.Source + " 不能复制"
		case from.Kind == files.KindDirectory && s.Before.Exists:
			return step{}, nil, target + " 已经存在"
		}
		s.SourceBefore, s.SourceSize = &from, from.Size
		if from.Kind == files.KindDirectory {
			if s.SourceSize, err = member.Usage(ctx, e.abs(s.SourceRel)); err != nil {
				return step{}, nil, describe(err, s.Source)
			}
		}
	case "write":
		source, err := clean(op.Source)
		if err != nil {
			return step{}, nil, err.Error()
		}
		sourceRel, err := volumePath(account, source, false)
		if err != nil {
			return step{}, nil, err.Error()
		}
		if content, err = e.read(ctx, member, sourceRel, MaxContent); err != nil {
			if errors.Is(err, errTooLarge) {
				return step{}, nil, fmt.Sprintf("%s 超过 %d MiB", source, MaxContent>>20)
			}
			return step{}, nil, fmt.Sprintf("source 必须是 %s 中已写好的文件", StagingDir)
		}
	}
	return s, content, ""
}

var errTooLarge = errors.New("file too large")

func (e *Executor) abs(rel string) string { return filepath.Join(e.Volume, filepath.FromSlash(rel)) }

// fingerprint describes a target now, with the start of a file's text for the card.
func (e *Executor) fingerprint(ctx context.Context, member files.FileSystem, rel string) (fingerprint, []byte, error) {
	info, err := member.Lstat(ctx, e.abs(rel))
	if errors.Is(err, fs.ErrNotExist) {
		return fingerprint{Exists: false}, nil, nil
	}
	if err != nil {
		return fingerprint{}, nil, err
	}
	seen := fingerprint{Exists: true, Kind: info.Kind, Size: info.Size, Inode: info.Inode, ModTime: info.ModTime.UnixNano()}
	if info.Kind != files.KindFile {
		return seen, nil, nil
	}
	head, err := e.read(ctx, member, rel, previewLimit)
	if errors.Is(err, errTooLarge) || err == nil {
		return seen, head, nil
	}
	return fingerprint{}, nil, err
}

// read reads up to limit bytes of a regular file as the member; errTooLarge
// comes with what was read when the file is longer.
func (e *Executor) read(ctx context.Context, member files.FileSystem, rel string, limit int64) ([]byte, error) {
	file, err := member.Open(ctx, e.abs(rel))
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return data[:limit], errTooLarge
	}
	return data, nil
}

// apply runs the steps as the member; it returns how many were done and why
// it stopped, if it did.
func (e *Executor) apply(ctx context.Context, account string, member files.FileSystem, c change) (int, string) {
	for _, s := range c.Steps {
		now, _, err := e.fingerprint(ctx, member, s.Rel)
		if err != nil {
			return 0, describe(err, s.Path)
		}
		if now != s.Before {
			return 0, s.Path + " 在用户确认前后被改动过，请重新提交"
		}
		if s.SourceBefore != nil {
			if now, _, err = e.fingerprint(ctx, member, s.SourceRel); err != nil || now != *s.SourceBefore {
				return 0, s.Source + " 在用户确认前后被改动过，请重新提交"
			}
		}
	}
	dir := filepath.Join(e.Root, account, c.ID)
	for n, s := range c.Steps {
		var err error
		switch s.Op {
		case "mkdir":
			err = member.MkdirAll(ctx, e.abs(s.Rel))
		case "delete":
			err = e.toTrash(ctx, member, s)
		case "move":
			if err = member.MkdirAll(ctx, e.abs(path.Dir(s.ToRel))); err == nil {
				err = member.Rename(ctx, e.abs(s.Rel), e.abs(s.ToRel))
			}
		case "write":
			var content []byte
			if content, err = os.ReadFile(filepath.Join(dir, fmt.Sprint(n))); err == nil {
				err = e.write(ctx, member, s, content)
			}
		case "copy":
			err = e.copy(ctx, member, s)
		}
		if err != nil {
			return n, describe(err, s.Path)
		}
	}
	return len(c.Steps), ""
}

func (e *Executor) write(ctx context.Context, member files.FileSystem, s step, content []byte) error {
	parent := e.abs(path.Dir(s.Rel))
	if err := member.MkdirAll(ctx, parent); err != nil {
		return err
	}
	file, temp, err := member.CreateTemp(ctx, parent, ".a-nas-assistant-")
	if err != nil {
		return err
	}
	_, err = file.Write(content)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil && s.Before.Exists {
		err = e.toTrash(ctx, member, s)
	}
	if err == nil {
		err = member.Rename(ctx, temp, e.abs(s.Rel))
	}
	if err != nil {
		_ = member.Remove(ctx, temp)
	}
	return err
}

// copy copies the source next to the target under a name of its own, moves a
// file the target replaces into the recycle bin, and puts the copy in place.
func (e *Executor) copy(ctx context.Context, member files.FileSystem, s step) error {
	parent := path.Dir(s.Rel)
	if err := member.MkdirAll(ctx, e.abs(parent)); err != nil {
		return err
	}
	id, err := newID()
	if err != nil {
		return err
	}
	temp := e.abs(parent + "/.a-nas-assistant-copy-" + id)
	err = member.Copy(ctx, e.abs(s.SourceRel), temp)
	if err == nil && s.Before.Exists {
		err = e.toTrash(ctx, member, s)
	}
	if err == nil {
		err = member.Rename(ctx, temp, e.abs(s.Rel))
	}
	if err != nil {
		_ = member.RemoveAll(ctx, temp)
	}
	return err
}

// toTrash moves a target into the member's recycle bin. Samba's
// recycle:versions keeps an earlier copy there and names the next "Copy #1 of
// <name>"; the rename never replaces anything.
func (e *Executor) toTrash(ctx context.Context, member files.FileSystem, s step) error {
	if err := member.MkdirAll(ctx, e.abs(s.Trash)); err != nil {
		return err
	}
	name := path.Base(s.Rel)
	candidate := name
	for n := 1; n <= 100; n++ {
		err := member.Rename(ctx, e.abs(s.Rel), e.abs(s.Trash+"/"+candidate))
		if !errors.Is(err, fs.ErrExist) {
			return err
		}
		candidate = fmt.Sprintf("Copy #%d of %s", n, name)
	}
	return fs.ErrExist
}

// describe puts a file error in words for the assistant and the card.
func describe(err error, target string) string {
	switch {
	case errors.Is(err, files.ErrUnsupportedType), errors.Is(err, syscall.ELOOP), errors.Is(err, syscall.ENOTDIR):
		return target + " 经过链接或文件，不能更改"
	case errors.Is(err, fs.ErrPermission):
		return "没有权限：" + target
	case errors.Is(err, fs.ErrExist):
		return target + " 已经存在"
	case errors.Is(err, fs.ErrNotExist):
		return target + " 不存在"
	case errors.Is(err, files.ErrInsufficientSpace):
		return "空间不足：" + target
	default:
		return fmt.Sprintf("%v：%s", err, target)
	}
}

func preview(data []byte) *string {
	if !utf8.Valid(data) {
		// A cut through the last character still counts as text.
		trimmed := data
		for i := 0; i < 3 && len(trimmed) > 0 && !utf8.Valid(trimmed); i++ {
			trimmed = trimmed[:len(trimmed)-1]
		}
		if !utf8.Valid(trimmed) {
			return nil
		}
		data = trimmed
	}
	text := string(data)
	return &text
}

// preview is the card: each operation, with the old and new text of every write.
func (e *Executor) preview(account string, c change) (json.RawMessage, error) {
	type operation struct {
		Op     string  `json:"op"`
		Path   string  `json:"path"`
		To     string  `json:"to,omitempty"`
		From   string  `json:"from,omitempty"`
		Size   *int64  `json:"size,omitempty"`
		Folder bool    `json:"folder,omitempty"`
		Exists bool    `json:"exists"`
		Before *string `json:"before,omitempty"`
		After  *string `json:"after,omitempty"`
	}
	ops := make([]operation, 0, len(c.Steps))
	for n, s := range c.Steps {
		op := operation{Op: s.Op, Path: s.Path, To: s.To, Exists: s.Before.Exists}
		if s.Op == "copy" && s.SourceBefore != nil {
			size := s.SourceSize
			op.From, op.Size, op.Folder = s.Source, &size, s.SourceBefore.Kind == files.KindDirectory
		}
		if s.Op == "write" {
			op.Before = s.BeforeText
			if data, err := readHead(filepath.Join(e.Root, account, c.ID, fmt.Sprint(n)), previewLimit); err == nil {
				op.After = preview(data)
			}
		}
		ops = append(ops, op)
	}
	return json.Marshal(struct {
		ID         string      `json:"id"`
		SessionID  string      `json:"sessionId,omitempty"`
		RequestID  string      `json:"requestId,omitempty"`
		Summary    string      `json:"summary"`
		Status     string      `json:"status"`
		CreatedAt  int64       `json:"createdAt"`
		DecidedAt  int64       `json:"decidedAt,omitempty"`
		Error      string      `json:"error,omitempty"`
		Operations []operation `json:"operations"`
	}{c.ID, c.SessionID, c.RequestID, c.Summary, c.Status, c.CreatedAt, c.DecidedAt, c.Error, ops})
}

func readHead(name string, limit int64) ([]byte, error) {
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return io.ReadAll(io.LimitReader(file, limit))
}

func newID() (string, error) {
	raw := make([]byte, 4)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// ---- state: Root/<account>/changes.json and Root/<account>/<id>/<step> ----

func (e *Executor) accountDir(account string) (string, error) {
	dir := filepath.Join(e.Root, account)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, os.Chmod(e.Root, 0o700)
}

func (e *Executor) snapshotDir(account, id string) (string, error) {
	base, err := e.accountDir(account)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, id)
	return dir, os.Mkdir(dir, 0o700)
}

func (e *Executor) load(account string) ([]change, error) {
	data, err := os.ReadFile(filepath.Join(e.Root, account, "changes.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var changes []change
	return changes, json.Unmarshal(data, &changes)
}

// save writes the account's changes, dropping the oldest beyond keepChanges
// with their copies.
func (e *Executor) save(account string, changes []change) error {
	dir, err := e.accountDir(account)
	if err != nil {
		return err
	}
	for len(changes) > keepChanges {
		_ = os.RemoveAll(filepath.Join(dir, changes[0].ID))
		changes = changes[1:]
	}
	data, err := json.Marshal(changes)
	if err != nil {
		return err
	}
	temp := filepath.Join(dir, ".changes.json.tmp")
	if err := os.WriteFile(temp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temp, filepath.Join(dir, "changes.json"))
}
