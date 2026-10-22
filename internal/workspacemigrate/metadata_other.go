//go:build !linux

package workspacemigrate

import (
	"io/fs"
	"os"
)

func portableMetadata(*os.Root, string, fs.FileInfo) error { return ErrSnapshot }
