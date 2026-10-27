// Package assistantchanges executes 提交更改, the only way a member's AI
// assistant changes the member's own files (ADR 0024 decision 4).
//
// The assistant stages new contents in its own directory, or names a file it
// reads to copy, and submits a list of operations. Preparing a submission checks it against what the member's
// paths hold now and copies what it would write, which is what the member's
// confirmation card shows. The member's confirmation, from their own
// signed-in session, records a one-time grant. When the assistant's MCP tool
// then submits the same arguments, the executor applies exactly the copies
// the member saw, and only if no target changed since. Replaced and deleted
// files go to the member's recycle bin where Samba's recycle would put them.
//
// A submission is bounded before any file is looked at (MaxOperations,
// MaxSubmission), and its staged files by their sizes before any is opened
// (MaxContent, MaxStaged). Contents are copied and hashed a block at a time;
// none is held in memory.
//
// Every file is read and written through a filesystem.FileSystem acting as the
// member, which never follows a link; this package only decides what to do.
package assistantchanges

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
)

// StagingDir is where the assistant writes contents to submit, as the
// assistant sees it.
const StagingDir = "/nas/助手/待确认"

const (
	// MaxOperations bounds the steps of one submission; its card lists each.
	MaxOperations = 100
	// MaxSubmission bounds a submission's summary and paths together, in
	// bytes of its canonical form, so the record of a change stays small.
	MaxSubmission = 64 << 10
	// maxArguments bounds the arguments as they arrive, the same as the body
	// of the MCP tool's call.
	maxArguments = 1 << 20
)

// Operation is one step of a submission, in the tool's own terms.
type Operation struct {
	Op     string `json:"op"`
	Path   string `json:"path,omitempty"`
	To     string `json:"to,omitempty"`
	Source string `json:"source,omitempty"`
}

// Submission is the arguments of one call of 提交更改 in canonical form; the
// grant is bound to their digest.
type Submission struct {
	Summary    string      `json:"summary"`
	Operations []Operation `json:"operations"`
}

// ParseSubmission reads the arguments of one call. Keys other than summary and
// operations, and keys of an operation other than op, path, to and source, do
// not count: the product service sees the call as Hermes reports it starting,
// the MCP tool as the model wrote it.
func ParseSubmission(raw []byte) (Submission, error) {
	if len(raw) > maxArguments {
		return Submission{}, invalid{fmt.Sprintf("参数超过 %d KiB", maxArguments>>10)}
	}
	var loose struct {
		Summary    any              `json:"summary"`
		Operations []map[string]any `json:"operations"`
	}
	if err := json.Unmarshal(raw, &loose); err != nil {
		return Submission{}, invalid{"参数不是有效的 JSON 对象"}
	}
	submission := Submission{Summary: text(loose.Summary), Operations: []Operation{}}
	for _, op := range loose.Operations {
		submission.Operations = append(submission.Operations, Operation{
			Op: text(op["op"]), Path: text(op["path"]), To: text(op["to"]), Source: text(op["source"]),
		})
	}
	return submission, nil
}

func text(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		data, _ := json.Marshal(v)
		return string(data)
	}
}

// Digest identifies a submission.
func (s Submission) Digest() string {
	data, _ := json.Marshal(s)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// area is a place a change may touch: a sandbox prefix and where it is on the
// data volume.
type area struct {
	sandbox, volume string
	target          bool
}

func areas(account string) []area {
	return []area{
		{"/nas/个人空间", "spaces/private/" + account, true},
		{"/nas/共享空间/Agent空间/" + account, "spaces/shared/Agent空间/" + account, true},
		{StagingDir, "assistant/" + account + "/files/待确认", false},
	}
}

// clean makes a path canonical without following anything; ".." is refused.
// The problem, if any, is in the assistant's words, as locate's are.
func clean(raw string) (string, string) {
	if !strings.HasPrefix(raw, "/") {
		return "", raw + "：需要绝对路径"
	}
	if slices.Contains(strings.Split(raw, "/"), "..") {
		return "", raw + "：路径中不能有 .."
	}
	return path.Clean(raw), ""
}

// validate checks a submission without looking at any file: its size, its
// operations and where each path is. It returns the steps with their places
// on the data volume.
func validate(account string, submission Submission) ([]step, error) {
	switch n := len(submission.Operations); {
	case n == 0:
		return nil, invalid{"没有任何操作"}
	case n > MaxOperations:
		return nil, invalid{fmt.Sprintf("一批最多 %d 项操作，这批有 %d 项", MaxOperations, n)}
	}
	if data, _ := json.Marshal(submission); len(data) > MaxSubmission {
		return nil, invalid{fmt.Sprintf("说明与路径合计超过 %d KiB", MaxSubmission>>10)}
	}
	var problems invalid
	steps := make([]step, 0, len(submission.Operations))
	for i, op := range submission.Operations {
		s, problem := locate(account, op)
		if problem != "" {
			problems = append(problems, fmt.Sprintf("第 %d 项：%s", i+1, problem))
			continue
		}
		steps = append(steps, s)
	}
	if len(problems) > 0 {
		return nil, problems
	}
	return steps, nil
}

// locate checks one operation and finds its paths on the data volume.
func locate(account string, op Operation) (step, string) {
	switch op.Op {
	case "write", "copy", "delete", "move", "mkdir":
	default:
		return step{}, fmt.Sprintf("未知操作 %q", op.Op)
	}
	target, problem := clean(op.Path)
	if problem != "" {
		return step{}, problem
	}
	rel, problem := volumePath(account, target, true)
	if problem != "" {
		return step{}, problem
	}
	s := step{Op: op.Op, Path: target, Rel: rel, Trash: trashFor(account, target)}
	switch op.Op {
	case "move":
		if s.To, problem = clean(op.To); problem != "" {
			return step{}, problem
		}
		if s.ToRel, problem = volumePath(account, s.To, true); problem != "" {
			return step{}, problem
		}
		if !sameArea(account, s.Path, s.To) {
			return step{}, fmt.Sprintf("%s 与 %s 不在同一个空间，不能移动：先 copy 到新位置，再 delete 原来的", s.Path, s.To)
		}
	case "copy":
		// Copied as the member when applied, from wherever the assistant reads;
		// nothing is staged, so a file of any size can be copied.
		if s.Source, problem = clean(op.Source); problem != "" {
			return step{}, problem
		}
		if s.SourceRel, problem = sourcePath(account, s.Source); problem != "" {
			return step{}, problem
		}
	case "write":
		if s.Source, problem = clean(op.Source); problem != "" {
			return step{}, problem
		}
		if s.SourceRel, problem = volumePath(account, s.Source, false); problem != "" {
			return step{}, problem
		}
	}
	return s, ""
}

// volumePath is where a sandbox path is, relative to the data volume. Targets
// are below the member's space or their Agent空间 folder, never the folder
// itself or a system name in it; sources are staged files. The problem, if
// any, is in the assistant's words.
func volumePath(account, sandboxPath string, target bool) (string, string) {
	for _, a := range areas(account) {
		if a.target != target || !strings.HasPrefix(sandboxPath, a.sandbox+"/") {
			continue
		}
		inside := strings.TrimPrefix(sandboxPath, a.sandbox+"/")
		if target {
			for part := range strings.SplitSeq(inside, "/") {
				if strings.HasPrefix(part, ".a-nas-") {
					return "", sandboxPath + "：不能更改系统文件夹"
				}
			}
		}
		return a.volume + "/" + inside, ""
	}
	if target {
		return "", fmt.Sprintf("%s：只能更改 /nas/个人空间 或 /nas/共享空间/Agent空间/%s 中的文件", sandboxPath, account)
	}
	return "", fmt.Sprintf("%s：source 必须是 %s 中已写好的文件", sandboxPath, StagingDir)
}

// sameArea reports whether two targets are in the same area. The member's
// space and the shared space are separate Btrfs subvolumes, and a rename
// between them fails.
func sameArea(account, a, b string) bool {
	for _, ar := range areas(account) {
		if ar.target && strings.HasPrefix(a, ar.sandbox+"/") {
			return strings.HasPrefix(b, ar.sandbox+"/")
		}
	}
	return false
}

// sourcePath is where a file to copy is, relative to the data volume: anywhere
// the assistant reads as the member, besides its own folder. The problem, if
// any, is in the assistant's words.
func sourcePath(account, sandboxPath string) (string, string) {
	for _, a := range []area{
		{"/nas/个人空间", "spaces/private/" + account, false},
		{"/nas/共享空间", "spaces/shared", false},
		{"/nas/助手", "assistant/" + account + "/files", false},
	} {
		if inside, ok := strings.CutPrefix(sandboxPath, a.sandbox+"/"); ok {
			return a.volume + "/" + inside, ""
		}
	}
	return "", sandboxPath + "：只能复制个人空间、共享空间或 /nas/助手 中的文件"
}

// trashFor is the folder Samba's recycle (keeptree) would move a path into:
// the member's recycle bin in its space, then the folders it was in. The Web
// recycle bin imports files found there under their own name and folder.
func trashFor(account, sandboxPath string) string {
	var bin, inside string
	if shared, ok := strings.CutPrefix(sandboxPath, "/nas/共享空间/"); ok {
		bin, inside = "spaces/shared/.a-nas-trash/"+account, shared
	} else {
		bin, inside = "spaces/private/"+account+"/.a-nas-trash/"+account, strings.TrimPrefix(sandboxPath, "/nas/个人空间/")
	}
	if parent := path.Dir(inside); parent != "." {
		return bin + "/" + parent
	}
	return bin
}

// ErrInvalid reports a submission that cannot be applied; Reasons says why.
var ErrInvalid = errors.New("the submission cannot be applied")

// invalid lists why a submission cannot be applied, one problem per line, in
// the words the assistant reads. The words are not the error's message: they
// reach the assistant through Reasons, in the tool's reply (#222).
type invalid []string

func (i invalid) Error() string        { return fmt.Sprintf("%v (%d problems)", ErrInvalid, len(i)) }
func (i invalid) Is(target error) bool { return target == ErrInvalid }

// Reasons says why err refused a submission, one problem per line, as the
// assistant reads it; it is empty for any other error.
func Reasons(err error) string {
	if problems, ok := errors.AsType[invalid](err); ok {
		return strings.Join(problems, "\n")
	}
	return ""
}
