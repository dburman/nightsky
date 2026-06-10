//go:build !windows

package capture

import (
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// DiskFreeGB returns the available (unprivileged) disk space in gigabytes
// at the filesystem containing path.
func DiskFreeGB(path string) (float64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return float64(st.Bavail) * float64(st.Bsize) / (1 << 30), nil
}

// CleanForSpace removes the oldest date directories under outputDir, one at a
// time, until free disk space exceeds minFreeGB. It never removes today's
// directory or protectDir (the just-finished night session, which is dated by
// the night's start and therefore not "today" when end-of-night cleanup runs).
// If no removable directory remains, it logs a warning and returns without
// error. protectDir is a directory basename ("2006-01-02"); pass "" to only
// protect today.
func CleanForSpace(outputDir string, minFreeGB float64, protectDir string, logger *slog.Logger) error {
	today := time.Now().Format("2006-01-02")

	for {
		free, err := DiskFreeGB(outputDir)
		if err != nil {
			return err
		}
		if free >= minFreeGB {
			return nil
		}

		dirs, err := ListDateDirs(outputDir)
		if err != nil {
			return err
		}

		// ListDateDirs returns newest-first; pick the oldest removable dir.
		victim := ""
		for i := len(dirs) - 1; i >= 0; i-- {
			base := filepath.Base(dirs[i])
			if base == today || base == protectDir {
				continue
			}
			victim = dirs[i]
			break
		}
		if victim == "" {
			logger.Warn("disk space low but only protected captures remain — nothing removed",
				"free_gb", free,
				"min_free_gb", minFreeGB,
			)
			return nil
		}

		logger.Info("disk space below threshold, removing oldest captures",
			"dir", victim,
			"free_gb", free,
			"min_free_gb", minFreeGB,
		)
		if err := os.RemoveAll(victim); err != nil {
			return err
		}
	}
}
