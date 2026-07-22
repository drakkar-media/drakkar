package database

import (
	"context"
	"errors"
	"io"
	"syscall"
	"time"
)

// SpeedTestResult reports the outcome of an internal streaming throughput
// test -- mirrors nzbdav's manual "NzbDav Internal Test" tuning procedure
// (stream an already-downloaded file through the real read path, note the
// Mbps and CPU usage) as a one-click backend operation instead of a
// terminal wget command.
type SpeedTestResult struct {
	FileName        string  `json:"fileName"`
	FileSizeBytes   int64   `json:"fileSizeBytes"`
	BytesRead       int64   `json:"bytesRead"`
	DurationSeconds float64 `json:"durationSeconds"`
	ThroughputMbps  float64 `json:"throughputMbps"`
	CPUPercent      float64 `json:"cpuPercent"`
}

const (
	speedTestDuration  = 8 * time.Second
	speedTestChunkSize = 4 * 1024 * 1024 // 4MB
)

// RunSpeedTest streams the largest already-downloaded media file through the
// same SegmentFetcher/ReadAheadManager path real playback uses, for a fixed
// wall-clock window, and reports throughput plus process CPU usage. Small
// files are read on a loop (wrapping back to offset 0) so the test still
// runs for the full window regardless of file size.
func (db *DB) RunSpeedTest(ctx context.Context) (SpeedTestResult, error) {
	entries, err := db.ListContentMountEntries(ctx)
	if err != nil {
		return SpeedTestResult{}, err
	}
	var best *ContentMountEntry
	for i := range entries {
		e := &entries[i]
		if e.ReaderKind == "inline" || e.SizeBytes <= 0 {
			continue
		}
		if best == nil || e.SizeBytes > best.SizeBytes {
			best = e
		}
	}
	if best == nil {
		return SpeedTestResult{}, errors.New("no downloaded media available to test — download something first")
	}

	reader, err := db.OpenVirtualMediaFile(ctx, best.VirtualFileID)
	if err != nil {
		return SpeedTestResult{}, err
	}

	var ruBefore, ruAfter syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ruBefore)

	buf := make([]byte, speedTestChunkSize)
	start := time.Now()
	deadline := start.Add(speedTestDuration)
	var totalRead int64
	var offset int64
	size := reader.Size()
	for time.Now().Before(deadline) {
		n, readErr := reader.ReadAt(ctx, buf, offset)
		totalRead += int64(n)
		offset += int64(n)
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				break
			}
			offset = 0 // loop back to the start of a small file
		}
		if size > 0 && offset >= size {
			offset = 0
		}
	}
	elapsed := time.Since(start)
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ruAfter)

	cpuSeconds := rusageCPUSeconds(ruAfter) - rusageCPUSeconds(ruBefore)
	var cpuPercent float64
	if elapsed.Seconds() > 0 {
		cpuPercent = (cpuSeconds / elapsed.Seconds()) * 100
	}
	var mbps float64
	if elapsed.Seconds() > 0 {
		mbps = (float64(totalRead) * 8 / 1_000_000) / elapsed.Seconds()
	}

	return SpeedTestResult{
		FileName:        best.FileName,
		FileSizeBytes:   best.SizeBytes,
		BytesRead:       totalRead,
		DurationSeconds: elapsed.Seconds(),
		ThroughputMbps:  mbps,
		CPUPercent:      cpuPercent,
	}, nil
}

func rusageCPUSeconds(ru syscall.Rusage) float64 {
	return float64(ru.Utime.Sec) + float64(ru.Utime.Usec)/1e6 +
		float64(ru.Stime.Sec) + float64(ru.Stime.Usec)/1e6
}
