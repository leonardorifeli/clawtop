package push

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path"
)

// SSH pushes payload atomically to a remote path using ssh+cat+mv.
// Host is an ssh_config alias (e.g. "cypher"); Path is the final
// destination on the remote host. The caller's ssh-agent keys are used.
type SSH struct {
	Host string
	Path string
}

func (s SSH) Push(ctx context.Context, payload []byte) error {
	dir := path.Dir(s.Path)
	tmp := s.Path + ".tmp"
	script := fmt.Sprintf(
		"mkdir -p %q && cat > %q && mv %q %q",
		dir, tmp, tmp, s.Path,
	)
	cmd := exec.CommandContext(ctx, "ssh",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		s.Host, script,
	)
	cmd.Stdin = bytes.NewReader(payload)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ssh push: %w: %s", err, errBuf.String())
	}
	return nil
}

func (s SSH) Describe() string { return "ssh:" + s.Host + ":" + s.Path }

// Reachable runs `ssh <host> true` to verify the alias resolves and key
// auth succeeds. Used by `clawtopd doctor` for preflight.
func (s SSH) Reachable(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "ssh",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		s.Host, "true",
	)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ssh %s: %w: %s", s.Host, err, errBuf.String())
	}
	return nil
}
