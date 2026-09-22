// Package procstats collects lightweight process resource metrics via
// syscall.Getrusage. Field units (Maxrss, Utime/Stime) are platform-specific;
// this was written and tested on Darwin, not verified on Linux.
package procstats

import (
	"syscall"
	"time"
)

type Sampler struct {
	lastWall time.Time
	lastCPU  time.Duration
}

func NewSampler() *Sampler {
	s := &Sampler{lastWall: time.Now()}
	s.lastCPU = cpuTime()
	return s
}

func (s *Sampler) Sample() (cpuPercent float64, rssBytes int64) {
	now := time.Now()
	cur := cpuTime()

	wallDelta := now.Sub(s.lastWall)
	cpuDelta := cur - s.lastCPU
	if wallDelta > 0 {
		cpuPercent = 100 * float64(cpuDelta) / float64(wallDelta)
	}

	s.lastWall = now
	s.lastCPU = cur

	rssBytes = rss()
	return
}

func cpuTime() time.Duration {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	utime := time.Duration(ru.Utime.Sec)*time.Second + time.Duration(ru.Utime.Usec)*time.Microsecond
	stime := time.Duration(ru.Stime.Sec)*time.Second + time.Duration(ru.Stime.Usec)*time.Microsecond
	return utime + stime
}

func rss() int64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	return int64(ru.Maxrss)
}
