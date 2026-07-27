//go:build linux

package projectionrepair

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func openBundleNoFollow(path string) (*os.File, error) {
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(descriptor), path)
	if file == nil {
		_ = unix.Close(descriptor)
		return nil, errors.New("create bundle file handle")
	}
	return file, nil
}
