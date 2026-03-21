package workspace

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ValidatePath ensures the workspace path resolves under the root.
func ValidatePath(root, path string) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolving workspace root: %w", err)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolving workspace path: %w", err)
	}
	if !strings.HasPrefix(absPath, absRoot+string(filepath.Separator)) && absPath != absRoot {
		return fmt.Errorf("workspace path %q is outside root %q", absPath, absRoot)
	}
	return nil
}
