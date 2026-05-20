package hash

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os/exec"
	"strings"
)

// Compute returns the 12-char content address for the given git paths plus
// an arbitrary extra string. Each path is resolved to its HEAD tree SHA via
// `git rev-parse HEAD:<path>`; SHAs are joined with newlines, then `extra:<x>\n`
// is appended, then SHA-256'd and truncated to 12 hex chars.
func Compute(paths []string, extra string) (string, error) {
	var b strings.Builder
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out, err := exec.Command("git", "rev-parse", "HEAD:"+p).Output()
		if err != nil {
			return "", fmt.Errorf("git rev-parse HEAD:%s: %w", p, err)
		}
		b.WriteString(strings.TrimSpace(string(out)))
		b.WriteByte('\n')
	}
	b.WriteString("extra:")
	b.WriteString(extra)
	b.WriteByte('\n')
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])[:12], nil
}
