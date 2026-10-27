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

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/filesystem"
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
	// keepRefusals is how many of Prepare's latest refusals an account keeps
	// for Apply, each for GrantTTL.
	keepRefusals = 16
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
// Volume through the member's own filesystem.FileSystem. Once a change can no
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
	// refused are the submissions Prepare refused lately, by digest, so that
	// Apply can say why without looking at any file.
	refused map[string]refusal
	// copying is the size of the copies Prepare is making.
	copying int64
	// applying are the changes Apply is carrying out in this process; one
	// left applying by an earlier process can no longer be applied.
	applying map[string]bool
}

type refusal struct {
	err error
	at  time.Time
}

// refuse remembers why Prepare refused a submission, dropping what is too
// old to be asked for and, beyond keepRefusals, the oldest.
func (l *ledger) refuse(digest string, err error, now time.Time) {
	if l.refused == nil {
		l.refused = map[string]refusal{}
	}
	oldest := ""
	for d, r := range l.refused {
		if now.Sub(r.at) > GrantTTL {
			delete(l.refused, d)
		} else if oldest == "" || r.at.Before(l.refused[oldest].at) {
			oldest = d
		}
	}
	if len(l.refused) >= keepRefusals {
		delete(l.refused, oldest)
	}
	l.refused[digest] = refusal{err: err, at: now}
}

// live reports whether a change can still be applied, so its copies are kept
// whole.
func (l *ledger) live(c change, now time.Time) bool {
	switch c.Status {
	case StatusAwaiting:
		return now.Sub(time.Unix(c.CreatedAt, 0)) <= awaitTTL
	case StatusApproved:
		return now.Sub(time.Unix(c.DecidedAt, 0)) <= GrantTTL
	case StatusApplying:
		return l.applying[c.ID]
	}
	return false
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

// staged is the size of the staged files the steps write, which are copied.
func staged(steps []step) int64 {
	var size int64
	for _, s := range steps {
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
func (e *Executor) Prepare(ctx context.Context, account string, member filesystem.FileSystem, raw []byte, sessionID, requestID string) (json.RawMessage, error) {
	if !accountid.ValidAssistantAccount(account) {
		return nil, errors.New("invalid account")
	}
	submission, err := ParseSubmission(raw)
	if err != nil {
		return nil, invalid{err.Error()}
	}
	digest, l := submission.Digest(), e.ledger(account)
	steps, err := validate(account, submission)
	if err == nil {
		err = e.measure(ctx, member, steps)
	}
	c := change{Digest: digest, Summary: submission.Summary, Steps: steps, Status: StatusAwaiting,
		SessionID: sessionID, RequestID: requestID}
	var reserved int64
	if err == nil {
		reserved, err = e.reserve(account, l, &c)
	}
	if err == nil {
		err = e.inspect(ctx, member, c.Steps)
	}
	var dir string
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
		l.refuse(digest, err, e.now())
	}
	if err != nil {
		return nil, err
	}
	delete(l.refused, digest)
	c.CreatedAt = e.now().Unix()
	changes, err := e.load(account)
	if err != nil {
		return nil, err
	}
	changes = append(changes, c)
	if err := e.save(l, account, changes); err != nil {
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
	if !ok || !accountid.ValidAssistantAccount(account) {
		return nil, ErrNotFound
	}
	l := e.ledger(account)
	l.Lock()
	defer l.Unlock()
	changes, err := e.load(account)
	if err != nil {
		return nil, err
	}
	now := e.now()
	for i := range changes {
		if changes[i].ID != id {
			continue
		}
		// A card older than Hermes waits is void, even before a save marks it so.
		if changes[i].Status == StatusAwaiting && !l.live(changes[i], now) {
			return nil, ErrDecided
		}
		allowed := false
		for _, from := range move.from {
			allowed = allowed || changes[i].Status == from
		}
		if !allowed {
			return nil, ErrDecided
		}
		changes[i].Status, changes[i].DecidedAt = move.to, now.Unix()
		if err := e.save(l, account, changes); err != nil {
			return nil, err
		}
		return e.preview(account, changes[i])
	}
	return nil, ErrNotFound
}

// Get returns a change's card as it is now.
func (e *Executor) Get(account, id string) (json.RawMessage, error) {
	if !accountid.ValidAssistantAccount(account) {
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
func (e *Executor) Apply(ctx context.Context, account string, member filesystem.FileSystem, raw []byte) string {
	if !accountid.ValidAssistantAccount(account) {
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
				if l.applying == nil {
					l.applying = map[string]bool{}
				}
				c.Status, l.applying[c.ID] = StatusApplying, true
				found = c
				if err = e.save(l, account, changes); err != nil {
					delete(l.applying, c.ID)
				}
				break
			}
		}
	}
	var granted change
	if found != nil {
		granted = *found
	}
	refused, wasRefused := l.refused[want]
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
		if !wasRefused || now.Sub(refused.at) > GrantTTL {
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
		_ = e.save(l, account, changes)
	}
	delete(l.applying, granted.ID)
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
	if !accountid.ValidAssistantAccount(account) {
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

// reserve makes room for a new change's copies and names it. A card its
// session still waits for is void: Hermes asks for one answer at a time in a
// session, so the new change replaces it. The copies of changes that can no
// longer be applied are cut first, and those of changes that can, with those
// being made, stay within maxPending. The name is one no kept change has.
func (e *Executor) reserve(account string, l *ledger, next *change) (int64, error) {
	l.Lock()
	defer l.Unlock()
	changes, err := e.load(account)
	if err != nil {
		return 0, err
	}
	now, held, taken := e.now(), l.copying, map[string]bool{}
	for i := range changes {
		c := &changes[i]
		taken[c.ID] = true
		if next.SessionID != "" && c.SessionID == next.SessionID && c.Status == StatusAwaiting {
			c.Status, c.DecidedAt = StatusExpired, now.Unix()
		}
		if l.live(*c, now) {
			held += staged(c.Steps)
		}
	}
	if err := e.save(l, account, changes); err != nil {
		return 0, err
	}
	size := staged(next.Steps)
	if size > 0 && held+size > maxPending {
		return 0, invalid{fmt.Sprintf("还在等待确认的更改与这一批的暂存文件合计超过 %d MiB，等用户处理完那些更改再提交", maxPending>>20)}
	}
	for next.ID == "" || taken[next.ID] {
		if next.ID, err = newID(); err != nil {
			return 0, err
		}
	}
	l.copying += size
	return size, nil
}

// measure checks the sizes of the staged files the steps write, from their
// metadata, so that a submission over the limits is refused before any file
// is opened.
func (e *Executor) measure(ctx context.Context, member filesystem.FileSystem, steps []step) error {
	var problems invalid
	for i := range steps {
		s := &steps[i]
		if s.Op != "write" {
			continue
		}
		from, err := e.fingerprint(ctx, member, s.SourceRel)
		switch {
		case err != nil || !from.Exists || from.Kind != filesystem.KindFile:
			problems = append(problems, fmt.Sprintf("第 %d 项：source 必须是 %s 中已写好的文件", i+1, StagingDir))
		case from.Size > MaxContent:
			problems = append(problems, fmt.Sprintf("第 %d 项：%s 超过 %d MiB", i+1, s.Source, MaxContent>>20))
		default:
			s.SourceSize = from.Size
		}
	}
	if staged(steps) > MaxStaged {
		problems = append(problems, fmt.Sprintf("暂存文件合计超过 %d MiB", MaxStaged>>20))
	}
	if len(problems) > 0 {
		return problems
	}
	return nil
}

// inspect looks at what the steps' paths are now, as the member, without
// opening any of them.
func (e *Executor) inspect(ctx context.Context, member filesystem.FileSystem, steps []step) error {
	var problems invalid
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
func (e *Executor) plan(ctx context.Context, member filesystem.FileSystem, s *step) string {
	var err error
	if s.Before, err = e.fingerprint(ctx, member, s.Rel); err != nil {
		return describe(err, s.Path)
	}
	if (s.Op == "delete" || s.Op == "move") && !s.Before.Exists {
		return s.Path + " 不存在"
	}
	if (s.Op == "write" || s.Op == "copy") && s.Before.Exists && s.Before.Kind != filesystem.KindFile {
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
		case from.Kind != filesystem.KindFile && from.Kind != filesystem.KindDirectory:
			return s.Source + " 不能复制"
		case from.Kind == filesystem.KindDirectory && s.Before.Exists:
			return s.Path + " 已经存在"
		}
		s.SourceBefore, s.SourceSize = &from, from.Size
		if from.Kind == filesystem.KindDirectory {
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
func (e *Executor) keep(ctx context.Context, member filesystem.FileSystem, steps []step, dir string) error {
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
	sum, copied, err := copyHashed(file, from, limit)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return sum, copied, err
}

// copyHashed copies up to limit bytes from src to dst a block at a time and
// returns the SHA-256 and length of what it copied.
func copyHashed(dst io.Writer, src io.Reader, limit int64) (string, int64, error) {
	hash := sha256.New()
	copied, err := io.Copy(io.MultiWriter(dst, hash), io.LimitReader(src, limit))
	return hex.EncodeToString(hash.Sum(nil)), copied, err
}

// errDamaged reports a kept copy that is no longer what was copied.
var errDamaged = errors.New("kept copy changed")

func (e *Executor) abs(rel string) string { return filepath.Join(e.Volume, filepath.FromSlash(rel)) }

// fingerprint describes a path now, without opening it.
func (e *Executor) fingerprint(ctx context.Context, member filesystem.FileSystem, rel string) (fingerprint, error) {
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
func (e *Executor) apply(ctx context.Context, account string, member filesystem.FileSystem, c change) (int, string) {
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
func (e *Executor) write(ctx context.Context, member filesystem.FileSystem, s step, kept string) error {
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
	sum, written, err := copyHashed(file, source, s.SourceSize+1)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil && (written != s.SourceSize || sum != s.Hash) {
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
func (e *Executor) copy(ctx context.Context, member filesystem.FileSystem, s step) error {
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
func (e *Executor) toTrash(ctx context.Context, member filesystem.FileSystem, s step) error {
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
	case errors.Is(err, filesystem.ErrUnsupportedType), errors.Is(err, syscall.ELOOP), errors.Is(err, syscall.ENOTDIR):
		return target + " 经过链接或文件，不能更改"
	case errors.Is(err, fs.ErrPermission):
		return "没有权限：" + target
	case errors.Is(err, fs.ErrExist):
		return target + " 已经存在"
	case errors.Is(err, fs.ErrNotExist):
		return target + " 不存在"
	case errors.Is(err, filesystem.ErrInsufficientSpace):
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
			op.From, op.Size, op.Folder = s.Source, &size, s.SourceBefore.Kind == filesystem.KindDirectory
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

// cut keeps only the start of each copy in a change's folder, for its card,
// and reports whether every copy is cut.
func cut(dir string, steps []step) bool {
	done := true
	for n, s := range steps {
		if s.Op != "write" {
			continue
		}
		name := filepath.Join(dir, fmt.Sprint(n))
		info, err := os.Stat(name)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			done = false
		case info.Size() > previewLimit:
			done = os.Truncate(name, previewLimit) == nil && done
		}
	}
	return done
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
// shows, and nothing once it ended GrantTTL ago and is older than the newest
// keepTexts. Tidying up is best effort: what fails is tried again next time.
func (e *Executor) save(l *ledger, account string, changes []change) error {
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
		if l.live(*c, now) {
			continue
		}
		if c.Status == StatusAwaiting {
			c.Status, c.DecidedAt = StatusExpired, now.Unix()
		}
		ended := time.Unix(max(c.CreatedAt, c.DecidedAt, c.AppliedAt), 0)
		switch {
		case i < len(changes)-keepTexts && c.Kept != keptNone && now.Sub(ended) > GrantTTL:
			if os.RemoveAll(filepath.Join(dir, c.ID)) == nil {
				c.Kept = keptNone
			}
		case c.Kept == "":
			if cut(filepath.Join(dir, c.ID), c.Steps) {
				c.Kept = keptStart
			}
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
