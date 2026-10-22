package project

import (
	"errors"
	"io/fs"
	"os"
	"strings"
)

// AssetsFolder is the project-relative folder for files agents write for
// the person. Sessions receive it as NOISEDECK_ASSETS_DIR.
const AssetsFolder = assetsDir + "/assets"

// AgentInstructionsFile is the project instruction file that carries the
// assets convention.
const AgentInstructionsFile = "AGENTS.md"

const (
	assetsInstructionsStart = "<!-- NOISEDECK_ASSETS_START -->"
	assetsInstructionsEnd   = "<!-- NOISEDECK_ASSETS_END -->"
)

const assetsInstructions = assetsInstructionsStart + "\n" +
	"## Files for the person\n\n" +
	"Files meant for the person you work with, such as screenshots, exports and reports, go in `" + AssetsFolder + "/`. " +
	"Deck shows them in the Assets panel.\n" +
	assetsInstructionsEnd + "\n"

// AddAssetsInstructions appends the assets block to the project's
// AGENTS.md, creating the file when there is none. A file that already
// carries the block, or an AGENTS.md that is not a regular file, is left as
// it is; existing content is never rewritten.
func AddAssetsInstructions(projectRoot string) (added bool, err error) {
	root, err := openProjectRoot(projectRoot, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = root.Close() }()

	info, err := root.Lstat(AgentInstructionsFile)
	if errors.Is(err, fs.ErrNotExist) {
		return writeInstructions(root, os.O_WRONLY|os.O_CREATE|os.O_EXCL, assetsInstructions)
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, nil
	}
	prior, err := root.ReadFile(AgentInstructionsFile)
	if err != nil {
		return false, err
	}
	if strings.Contains(string(prior), assetsInstructionsStart) {
		return false, nil
	}
	separator := "\n\n"
	switch {
	case len(prior) == 0 || strings.HasSuffix(string(prior), "\n\n"):
		separator = ""
	case strings.HasSuffix(string(prior), "\n"):
		separator = "\n"
	}
	return writeInstructions(root, os.O_WRONLY|os.O_APPEND, separator+assetsInstructions)
}

func writeInstructions(root *os.Root, flag int, text string) (bool, error) {
	f, err := root.OpenFile(AgentInstructionsFile, flag, 0o644)
	if err != nil {
		return false, err
	}
	if _, err := f.WriteString(text); err != nil {
		_ = f.Close()
		return false, err
	}
	return true, f.Close()
}
