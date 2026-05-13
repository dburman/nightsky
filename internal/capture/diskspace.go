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
// directory. If space cannot be freed (only today remains), it logs a warning
// and returns without error.
func CleanForSpace(outputDir string, minFreeGB float64, logger *slog.Logger) error {
	today := time.Now().Format("2006-01-02")

	for {
		free, err := DiskFreeGB(outputDir)
		if err != nil {
			return err
		}
		if free >= minFreeGB {
			return nil
		}

		// ListDateDirs returns newest-first; oldest is last.
		dirs, err := ListDateDirs(outputDir)
		if err != nil || len(dirs) == 0 {
			return nil
		}

		oldest := dirs[len(dirs)-1]
		if filepath.Base(oldest) == today {
			logger.Warn("disk space low but only today's captures remain — nothing removed",
				"free_gb", free,
				"min_free_gb", minFreeGB,
			)
			return nil
		}

		logger.Info("disk space below threshold, removing oldest captures",
			"dir", oldest,
			"free_gb", free,
			"min_free_gb", minFreeGB,
		)
		if err := os.RemoveAll(oldest); err != nil {
			return err
		}
	}
}
