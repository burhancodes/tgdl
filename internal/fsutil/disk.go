package fsutil

import (
	"syscall"
)

// StatfsFreeFunc allows injecting a mock free-space measurement in tests.
var StatfsFreeFunc = SyscallStatfsFree

// SyscallStatfsFree queries free space available to unprivileged user using syscall.Statfs.
func SyscallStatfsFree(path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}

// FreeDiskSpace returns the free disk space in bytes for the filesystem containing path.
func FreeDiskSpace(path string) (uint64, error) {
	return StatfsFreeFunc(path)
}
