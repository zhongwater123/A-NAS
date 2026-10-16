// Package assistantchanges executes 提交更改, the only way a member's AI
// assistant changes the member's own files (ADR 0022 decision 4).
//
// The assistant stages new contents in its own directory and submits a list
// of operations. Preparing a submission checks it against what the member's
// paths hold now and copies what it would write, which is what the member's
// confirmation card shows. The member's confirmation, from their own
// signed-in session, records a one-time grant. When the assistant's MCP tool
// then submits the same arguments, the executor applies exactly the copies
// the member saw, and only if no target changed since. Replaced and deleted
// files go to the member's recycle bin where Samba's recycle would put them.
//
// Every file is read and written through a files.FileSystem acting as the
// member, which never follows a link; this package only decides what to do.
package assistantchanges

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
)

// StagingDir is where the assistant writes contents to submit, as the
// assistant sees it.
const StagingDir = "/nas/助手/待确认"

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
	var loose struct {
		Summary    any              `json:"summary"`
		Operations []map[string]any `json:"operations"`
	}
	if err := json.Unmarshal(raw, &loose); err != nil {
		return Submission{}, fmt.Errorf("参数不是有效的 JSON 对象")
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

var accountPattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

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
func clean(raw string) (string, error) {
	if !strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("%s：需要绝对路径", raw)
	}
	for _, part := range strings.Split(raw, "/") {
		if part == ".." {
			return "", fmt.Errorf("%s：路径中不能有 ..", raw)
		}
	}
	return path.Clean(raw), nil
}

// volumePath is where a sandbox path is, relative to the data volume. Targets
// are below the member's space or their Agent空间 folder, never the folder
// itself or a system name in it; sources are staged files.
func volumePath(account, sandboxPath string, target bool) (string, error) {
	for _, a := range areas(account) {
		if a.target != target || !strings.HasPrefix(sandboxPath, a.sandbox+"/") {
			continue
		}
		inside := strings.TrimPrefix(sandboxPath, a.sandbox+"/")
		if target {
			for _, part := range strings.Split(inside, "/") {
				if strings.HasPrefix(part, ".a-nas-") {
					return "", fmt.Errorf("%s：不能更改系统文件夹", sandboxPath)
				}
			}
		}
		return a.volume + "/" + inside, nil
	}
	if target {
		return "", fmt.Errorf("%s：只能更改 /nas/个人空间 或 /nas/共享空间/Agent空间/%s 中的文件", sandboxPath, account)
	}
	return "", fmt.Errorf("%s：source 必须是 %s 中已写好的文件", sandboxPath, StagingDir)
}

// trashFor is the folder Samba's recycle (keeptree) would move a path into:
// the member's recycle bin in its space, then the folders it was in. The Web
// recycle bin imports files found there under their own name and folder.
func trashFor(account, sandboxPath string) string {
	var bin, inside string
	if strings.HasPrefix(sandboxPath, "/nas/共享空间/") {
		bin, inside = "spaces/shared/.a-nas-trash/"+account, strings.TrimPrefix(sandboxPath, "/nas/共享空间/")
	} else {
		bin, inside = "spaces/private/"+account+"/.a-nas-trash/"+account, strings.TrimPrefix(sandboxPath, "/nas/个人空间/")
	}
	if parent := path.Dir(inside); parent != "." {
		return bin + "/" + parent
	}
	return bin
}

// ErrInvalid reports a submission that cannot be applied; its message lists
// why, one problem per line, for the assistant.
var ErrInvalid = errors.New("提交被拒绝")

type invalid []string

func (i invalid) Error() string        { return ErrInvalid.Error() + "：\n" + strings.Join(i, "\n") }
func (i invalid) Is(target error) bool { return target == ErrInvalid }
