//go:build !windows

package session

import "os"

func replaceSessionFile(oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}
