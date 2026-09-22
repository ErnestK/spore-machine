package procstats

import (
	"testing"
	"time"
)

func TestSample_FirstCallNoNonsenseValues(t *testing.T) {
	s := NewSampler()
	cpuPercent, rssBytes := s.Sample()

	if cpuPercent < 0 {
		t.Errorf("cpuPercent = %v, want >= 0", cpuPercent)
	}
	if rssBytes < 0 {
		t.Errorf("rssBytes = %v, want >= 0", rssBytes)
	}
}

func TestSample_RepeatedCallsDoNotPanic(t *testing.T) {
	s := NewSampler()
	for i := 0; i < 3; i++ {
		time.Sleep(5 * time.Millisecond)
		cpuPercent, rssBytes := s.Sample()
		if cpuPercent < 0 {
			t.Errorf("call %d: cpuPercent = %v, want >= 0", i, cpuPercent)
		}
		if rssBytes < 0 {
			t.Errorf("call %d: rssBytes = %v, want >= 0", i, rssBytes)
		}
	}
}
