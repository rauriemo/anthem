package harness

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed all:skills
var embeddedSkills embed.FS

// WriteEmbeddedSkill extracts a built-in skill from the embedded FS to the
// target directory. Returns true if the skill was found in the embedded FS.
func WriteEmbeddedSkill(name, dstDir string) (bool, error) {
	srcDir := "skills/" + name

	if _, err := fs.ReadDir(embeddedSkills, srcDir); err != nil {
		return false, nil
	}

	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return false, fmt.Errorf("create skill dir %s: %w", dstDir, err)
	}

	if err := copyEmbeddedDir(embeddedSkills, srcDir, dstDir); err != nil {
		return false, fmt.Errorf("copy embedded skill %s: %w", name, err)
	}

	return true, nil
}

func copyEmbeddedDir(fsys embed.FS, src, dst string) error {
	entries, err := fs.ReadDir(fsys, src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := src + "/" + entry.Name()
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			if err := os.MkdirAll(dstPath, 0755); err != nil {
				return err
			}
			if err := copyEmbeddedDir(fsys, srcPath, dstPath); err != nil {
				return err
			}
		} else {
			data, err := fs.ReadFile(fsys, srcPath)
			if err != nil {
				return err
			}
			if err := os.WriteFile(dstPath, data, 0644); err != nil {
				return err
			}
		}
	}
	return nil
}
