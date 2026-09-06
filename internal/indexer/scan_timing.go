package indexer

import "time"

// Phase timers report elapsed wall time, not CPU time. Child phases are
// contained in their parent total and must not be summed with that total.
func timeScanPhase(timings map[string]int64, name string, run func() error) error {
	started := time.Now()
	err := run()
	timings[name] += time.Since(started).Milliseconds()
	return err
}
