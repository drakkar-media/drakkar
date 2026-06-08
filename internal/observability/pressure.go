package observability

import (
	"os"
	"strconv"
	"strings"
)

// IOPressureSome reads the "some" avg10 value from /proc/pressure/io.
// Returns 0.0 on any error (file absent on non-Linux or older kernels).
// A value above ~15.0 means the system is under meaningful IO pressure.
func IOPressureSome() float64 {
	data, err := os.ReadFile("/proc/pressure/io")
	if err != nil {
		return 0
	}
	// Format: "some avg10=X.XX avg60=X.XX avg300=X.XX total=N"
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "some ") {
			continue
		}
		for _, field := range strings.Fields(line) {
			if strings.HasPrefix(field, "avg10=") {
				val, _ := strconv.ParseFloat(strings.TrimPrefix(field, "avg10="), 64)
				return val
			}
		}
	}
	return 0
}

// HighIOPressure returns true when avg10 IO pressure exceeds the given threshold.
// Use 15.0 as a sensible default (reference uses ~15%).
func HighIOPressure(threshold float64) bool {
	return IOPressureSome() > threshold
}
