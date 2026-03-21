package voice

import (
	"fmt"
	"os"
	"time"
)

// AppendChangelog logs a voice change to the changelog file.
func AppendChangelog(path string, taskID string, diff string) error {
	entry := fmt.Sprintf("\n## %s\n\nTask: %s\n\n```diff\n%s\n```\n",
		time.Now().UTC().Format(time.RFC3339), taskID, diff)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("opening voice changelog: %w", err)
	}
	defer f.Close()

	if _, err := f.WriteString(entry); err != nil {
		return fmt.Errorf("writing voice changelog: %w", err)
	}
	return nil
}
