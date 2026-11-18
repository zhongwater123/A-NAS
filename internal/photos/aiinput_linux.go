package photos

import (
	"os"

	"github.com/zhongwater123/A-NAS/internal/unixmsg"
)

func memoryFile(content []byte) (*os.File, error) {
	return unixmsg.SealedFile("anas-ai-input", content)
}
