//go:build !linux

package projectionrepair

import (
	"errors"
	"os"
)

func openBundleNoFollow(string) (*os.File, error) {
	return nil, errors.New("synthetic repair bundle execution requires Linux O_NOFOLLOW support")
}
