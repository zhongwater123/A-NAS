package assistantchanges

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
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
	// MaxStaged bounds the staged files of one submission together.
	MaxStaged = 256 << 20
	// previewLimit bounds the text a card shows of each file.
	previewLimit = 64 << 10
	// GrantTTL is how long a grant waits for the call Hermes makes right after
	// the member's answer; a grant nobody used by then is void.
	GrantTTL = 2 * time.Minute
	// awaitTTL is how long a card waits for the member's answer. Hermes waits
	// a day (approvals.timeout in managed-config.yaml); a card older than that
	// is void.
	awaitTTL = 25 * time.Hour
	// keepChanges is how many changes of an account are kept, newest first;
	// the texts of the newest keepTexts that ended are kept for their cards.
	keepChanges = 200
	keepTexts   = 20
)

// maxPending bounds the copies one account's changes hold while they can
// still be applied, together with those being made. Tests lower it.
var maxPending int64 = 512 << 20

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
// Volume through the member's own files.FileSystem. Once a change can no
// longer be applied, only the start of each copy is kept, for its card.
type Executor struct {
	Root   string
	Volume string
	Now    func() time.Time

	mu       sync.Mutex
	accounts map[string]*ledger
}

// ledger serializes one account's changes; accounts do not wait for each
// other.
type ledger struct {
	sync.Mutex
	// refused is the last submission Prepare refused, so that Apply can say
	// why without looking at any file.
	refused refusal
	// copying is the size of the copies Prepare is making.
	copying int64
}

type refusal struct {
	digest string
	err    error
	at     time.Time
}

func (e *Executor) ledger(account string) *ledger {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.accounts == nil {
		e.accounts = map[string]*ledger{}
	}
	l, ok := e.accounts[account]
	if !ok {
		l = &ledger{}
		e.accounts[account] = l
	}
	return l
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
	Op     string      `json:"op"`
	Path   string      `json:"path"`
	To     string      `json:"to,omitempty"`
	Rel    string      `json:"rel"`
	ToRel  string      `json:"toRel,omitempty"`
	Trash  string      `json:"trash"`
	Before fingerprint `json:"before"`
	// A copy's source, and what it was when the card was made, or a write's
	// staged file; Hash is the SHA-256 of the copy kept of a staged file.
	Source       string       `json:"source,omitempty"`
	SourceRel    string       `json:"sourceRel,omitempty"`
	SourceBefore *fingerprint `json:"sourceBefore,omitempty"`
	SourceSize   int64        `json:"sourceSize,omitempty"`
	Hash         string       `json:"hash,omitempty"`
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
	// Kept is what is left of the copies once the change can no longer be
	// applied: keptStart, what the card shows, or keptNone.
	Kept string `json:"kept,omitempty"`
}

const (
	keptStart = "start"
	keptNone  = "none"
)

// live reports whether a change can still be applied, so its copies are kept
// whole.
func live(c change, now time.Time) bool {
	switch c.Status {
	case StatusApproved:
		return now.Sub(time.Unix(c.DecidedAt, 0)) <= GrantTTL
	case StatusAwaiting, StatusApplying:
		return now.Sub(time.Unix(c.CreatedAt, 0)) <= awaitTTL
	}
	return false
}

// staged is the size of the change's copies.
func (c change) staged() int64 {
	var size int64
	for _, s := range c.Steps {
		if s.Op == "write" {
			size += s.SourceSize
		}
	}
	return size
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
	digest, l := submission.Digest(), e.ledger(account)
	steps, err := validate(account, submission)
	if err == nil {
		err = e.inspect(ctx, member, steps)
	}
	c := change{Digest: digest, Summary: submission.Summary, Steps: steps, Status: StatusAwaiting,
		SessionID: sessionID, RequestID: requestID}
	var reserved int64
	if err == nil {
		reserved, err = e.reserve(account, l, c)
	}
	var dir string
	if err == nil {
		c.ID, err = newID()
	}
	if err == nil {
		dir, err = e.snapshotDir(account, c.ID)
	}
	if err == nil {
		if err = e.keep(ctx, member, c.Steps, dir); err != nil {
			_ = os.RemoveAll(dir)
		}
	}
	l.Lock()
	defer l.Unlock()
	l.copying -= reserved
	if errors.Is(err, ErrInvalid) {
		l.refused = refusal{digest: digest, err: err, at: e.now()}
	}
	if err != nil {
		return nil, err
	}
	l.refused = refusal{}
	c.CreatedAt = e.now().Unix()
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
	l := e.ledger(account)
	l.Lock()
	defer l.Unlock()
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
	l := e.ledger(account)
	l.Lock()
	defer l.Unlock()
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
	want, now, l := submission.Digest(), e.now(), e.ledger(account)
	l.Lock()
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
	refused := l.refused
	l.Unlock()
	if err != nil {
		return "暂时无法执行更改，什么都没有改变。"
	}
	if found == nil {
		// The product service lets a submission Prepare refused through without
		// a card, so the assistant learns what is wrong with it instead of
		// hearing that the member said no. Nothing is looked at for a
		// submission without a grant: Prepare's reasons are repeated, and any
		// other submission is checked by its paths alone.
		err := refused.err
		if refused.digest != want || now.Sub(refused.at) > GrantTTL {
			_, err = validate(account, submission)
		}
		if err != nil {
			return "提交被拒绝，什么都没有改变：\n" + strings.TrimPrefix(err.Error(), ErrInvalid.Error()+"：\n")
		}
		return "这批更改没有得到用户本人的确认，什么都没有改变。"
	}
	done, failure := e.apply(ctx, account, member, granted)
	l.Lock()
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
	l.Unlock()
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
	l := e.ledger(account)
	l.Lock()
	changes, err := e.load(account)
	l.Unlock()
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

// reserve makes room for a new change's copies. A card its session still
// waits for is void: Hermes asks for one answer at a time in a session, so
// the new change replaces it. The copies of changes that can still be
// applied, with those being made, stay within maxPending.
func (e *Executor) reserve(account string, l *ledger, next change) (int64, error) {
	l.Lock()
	defer l.Unlock()
	changes, err := e.load(account)
	if err != nil {
		return 0, err
	}
	now, held, replaced := e.now(), l.copying, false
	for i := range changes {
		c := &changes[i]
		if next.SessionID != "" && c.SessionID == next.SessionID && c.Status == StatusAwaiting {
			c.Status, c.DecidedAt, replaced = StatusExpired, now.Unix(), true
		}
		if live(*c, now) {
			held += c.staged()
		}
	}
	if replaced {
		if err := e.save(account, changes); err != nil {
			return 0, err
		}
	}
	staged := next.staged()
	if staged > 0 && held+staged > maxPending {
		return 0, invalid{fmt.Sprintf("还在等待确认的更改与这一批的暂存文件合计超过 %d MiB，等用户处理完那些更改再提交", maxPending>>20)}
	}
	l.copying += staged
	return staged, nil
}

// inspect looks at what the steps' paths are now, as the member, without
// opening any of them. The staged files' sizes come first, so that a
// submission over the limits is refused before anything else.
func (e *Executor) inspect(ctx context.Context, member files.FileSystem, steps []step) error {
	var problems invalid
	var staged int64
	for i := range steps {
		s := &steps[i]
		if s.Op != "write" {
			continue
		}
		from, err := e.fingerprint(ctx, member, s.SourceRel)
		switch {
		case err != nil || !from.Exists || from.Kind != files.KindFile:
			problems = append(problems, fmt.Sprintf("第 %d 项：source 必须是 %s 中已写好的文件", i+1, StagingDir))
		case from.Size > MaxContent:
			problems = append(problems, fmt.Sprintf("第 %d 项：%s 超过 %d MiB", i+1, s.Source, MaxContent>>20))
		default:
			s.SourceSize = from.Size
			staged += from.Size
		}
	}
	if staged > MaxStaged {
		problems = append(problems, fmt.Sprintf("暂存文件合计超过 %d MiB", MaxStaged>>20))
	}
	if len(problems) > 0 {
		return problems
	}
	for i := range steps {
		if problem := e.plan(ctx, member, &steps[i]); problem != "" {
			problems = append(problems, fmt.Sprintf("第 %d 项：%s", i+1, problem))
		}
	}
	if len(problems) > 0 {
		return problems
	}
	return nil
}

// plan checks one step against what its paths are now and records them.
func (e *Executor) plan(ctx context.Context, member files.FileSystem, s *step) string {
	var err error
	if s.Before, err = e.fingerprint(ctx, member, s.Rel); err != nil {
		return describe(err, s.Path)
	}
	if (s.Op == "delete" || s.Op == "move") && !s.Before.Exists {
		return s.Path + " 不存在"
	}
	if (s.Op == "write" || s.Op == "copy") && s.Before.Exists && s.Before.Kind != files.KindFile {
		return s.Path + " 不是普通文件"
	}
	switch s.Op {
	case "move":
		to, err := e.fingerprint(ctx, member, s.ToRel)
		if err != nil {
			return describe(err, s.To)
		}
		if to.Exists {
			return s.To + " 已经存在"
		}
	case "copy":
		from, err := e.fingerprint(ctx, member, s.SourceRel)
		switch {
		case err != nil:
			return describe(err, s.Source)
		case !from.Exists:
			return s.Source + " 不存在"
		case from.Kind != files.KindFile && from.Kind != files.KindDirectory:
			return s.Source + " 不能复制"
		case from.Kind == files.KindDirectory && s.Before.Exists:
			return s.Path + " 已经存在"
		}
		s.SourceBefore, s.SourceSize = &from, from.Size
		if from.Kind == files.KindDirectory {
			if s.SourceSize, err = member.Usage(ctx, e.abs(s.SourceRel)); err != nil {
				return describe(err, s.Source)
			}
		}
	}
	return ""
}

// keep copies, for each write, the start of the file it replaces and the
// whole staged file into dir, as the member reads them, a block at a time.
// The staged file must still be the size inspect saw; the SHA-256 of its copy
// is recorded.
func (e *Executor) keep(ctx context.Context, member files.FileSystem, steps []step, dir string) error {
	var problems invalid
	for i := range steps {
		s := &steps[i]
		if s.Op != "write" {
			continue
		}
		name := filepath.Join(dir, fmt.Sprint(i))
		if s.Before.Exists {
			target, err := member.Open(ctx, e.abs(s.Rel))
			if err != nil {
				problems = append(problems, fmt.Sprintf("第 %d 项：%s", i+1, describe(err, s.Path)))
				continue
			}
			if _, _, err := copyInto(name+".before", target, previewLimit); err != nil {
				return err
			}
		}
		source, err := member.Open(ctx, e.abs(s.SourceRel))
		if err != nil {
			problems = append(problems, fmt.Sprintf("第 %d 项：source 必须是 %s 中已写好的文件", i+1, StagingDir))
			continue
		}
		var copied int64
		if s.Hash, copied, err = copyInto(name, source, s.SourceSize+1); err != nil {
			return err
		}
		if copied != s.SourceSize {
			problems = append(problems, fmt.Sprintf("第 %d 项：%s 正在被改写，写完后再提交", i+1, s.Source))
		}
	}
	if len(problems) > 0 {
		return problems
	}
	return nil
}

// copyInto copies up to limit bytes of from into a new file in Root, closes
// from, and returns the SHA-256 and length of what it copied.
func copyInto(name string, from *os.File, limit int64) (string, int64, error) {
	defer func() { _ = from.Close() }()
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", 0, err
	}
	hash := sha256.New()
	copied, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(from, limit))
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return hex.EncodeToString(hash.Sum(nil)), copied, err
}

// errDamaged reports a kept copy that is no longer what was copied.
var errDamaged = errors.New("kept copy changed")

func (e *Executor) abs(rel string) string { return filepath.Join(e.Volume, filepath.FromSlash(rel)) }

// fingerprint describes a path now, without opening it.
func (e *Executor) fingerprint(ctx context.Context, member files.FileSystem, rel string) (fingerprint, error) {
	info, err := member.Lstat(ctx, e.abs(rel))
	if errors.Is(err, fs.ErrNotExist) {
		return fingerprint{Exists: false}, nil
	}
	if err != nil {
		return fingerprint{}, err
	}
	return fingerprint{Exists: true, Kind: info.Kind, Size: info.Size, Inode: info.Inode, ModTime: info.ModTime.UnixNano()}, nil
}

// apply runs the steps as the member; it returns how many were done and why
// it stopped, if it did.
func (e *Executor) apply(ctx context.Context, account string, member files.FileSystem, c change) (int, string) {
	dir := filepath.Join(e.Root, account, c.ID)
	for _, s := range c.Steps {
		now, err := e.fingerprint(ctx, member, s.Rel)
		if err != nil {
			return 0, describe(err, s.Path)
		}
		if now != s.Before {
			return 0, s.Path + " 在用户确认前后被改动过，请重新提交"
		}
		if s.SourceBefore != nil {
			if now, err = e.fingerprint(ctx, member, s.SourceRel); err != nil || now != *s.SourceBefore {
				return 0, s.Source + " 在用户确认前后被改动过，请重新提交"
			}
		}
	}
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
			if err = e.write(ctx, member, s, filepath.Join(dir, fmt.Sprint(n))); errors.Is(err, errDamaged) {
				return n, s.Path + " 的新内容已不完整，请重新提交"
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

// write copies the kept copy next to the target under a name of its own, a
// block at a time and checking on the way that it is what was kept, moves a
// file the target replaces into the recycle bin, and puts the new one in
// place.
func (e *Executor) write(ctx context.Context, member files.FileSystem, s step, kept string) error {
	source, err := os.Open(kept)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	parent := e.abs(path.Dir(s.Rel))
	if err := member.MkdirAll(ctx, parent); err != nil {
		return err
	}
	file, temp, err := member.CreateTemp(ctx, parent, ".a-nas-assistant-")
	if err != nil {
		return err
	}
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(source, s.SourceSize+1))
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil && (written != s.SourceSize || hex.EncodeToString(hash.Sum(nil)) != s.Hash) {
		err = errDamaged
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
	dir := filepath.Join(e.Root, account, c.ID)
	ops := make([]operation, 0, len(c.Steps))
	for n, s := range c.Steps {
		op := operation{Op: s.Op, Path: s.Path, To: s.To, Exists: s.Before.Exists}
		if s.Op == "copy" && s.SourceBefore != nil {
			size := s.SourceSize
			op.From, op.Size, op.Folder = s.Source, &size, s.SourceBefore.Kind == files.KindDirectory
		}
		if s.Op == "write" {
			if data, err := readHead(filepath.Join(dir, fmt.Sprint(n)+".before"), previewLimit); err == nil {
				op.Before = preview(data)
			}
			if data, err := readHead(filepath.Join(dir, fmt.Sprint(n)), previewLimit); err == nil {
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

// ---- state: Root/<account>/changes.json; for a write, the copy in
// Root/<account>/<id>/<step> and the start of the file it replaces in
// <step>.before ----

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
// with their copies. A card nobody answered in time is void. A change that
// can no longer be applied keeps only the start of each copy, which its card
// shows, and nothing once it is older than the newest keepTexts.
func (e *Executor) save(account string, changes []change) error {
	dir, err := e.accountDir(account)
	if err != nil {
		return err
	}
	for len(changes) > keepChanges {
		_ = os.RemoveAll(filepath.Join(dir, changes[0].ID))
		changes = changes[1:]
	}
	now := e.now()
	for i := range changes {
		c := &changes[i]
		if live(*c, now) {
			continue
		}
		if c.Status == StatusAwaiting {
			c.Status, c.DecidedAt = StatusExpired, now.Unix()
		}
		switch {
		case i < len(changes)-keepTexts && c.Kept != keptNone:
			if err := os.RemoveAll(filepath.Join(dir, c.ID)); err != nil {
				return err
			}
			c.Kept = keptNone
		case c.Kept == "":
			for n, s := range c.Steps {
				if s.Op != "write" {
					continue
				}
				name := filepath.Join(dir, c.ID, fmt.Sprint(n))
				if info, err := os.Stat(name); err == nil && info.Size() > previewLimit {
					if err := os.Truncate(name, previewLimit); err != nil {
						return err
					}
				}
			}
			c.Kept = keptStart
		}
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
