package archive

import (
	"context"
	"os/exec"
)

// WrapHeavy wraps heavy external command with ionice -c3 and nice -n 19 when available on the host.
func WrapHeavy(name string, args ...string) (string, []string) {
	cmdName := name
	cmdArgs := args
	if ionice, err := exec.LookPath("ionice"); err == nil {
		cmdArgs = append([]string{"-c3", cmdName}, cmdArgs...)
		cmdName = ionice
	}
	if nice, err := exec.LookPath("nice"); err == nil {
		cmdArgs = append([]string{"-n", "19", cmdName}, cmdArgs...)
		cmdName = nice
	}
	return cmdName, cmdArgs
}

func execIn(ctx context.Context, dir, name string, args ...string) *exec.Cmd {
	bin, wArgs := WrapHeavy(name, args...)
	c := exec.CommandContext(ctx, bin, wArgs...)
	c.Dir = dir
	return c
}
