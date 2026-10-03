package archive

import (
	"context"
	"os/exec"
)

func execIn(ctx context.Context, dir, name string, args ...string) *exec.Cmd {
	c := exec.CommandContext(ctx, name, args...)
	c.Dir = dir
	return c
}
