// drakkar-restore applies a restore marker before the main service starts.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/drakkar-media/drakkar/internal/config"
	"github.com/drakkar-media/drakkar/internal/systembackup"
)

func main() {
	settingsPath := os.Getenv("DRAKKAR_SETTINGS_PATH")
	if settingsPath == "" {
		settingsPath = config.DefaultSettingsPath
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()
	if err := systembackup.ApplyPendingRestore(ctx, settingsPath, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "drakkar restore: %v\n", err)
		if errors.Is(err, systembackup.ErrRestoreHandled) {
			return
		}
		os.Exit(1)
	}
}
