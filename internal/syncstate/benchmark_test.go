package syncstate

import (
	"fmt"
	"testing"
)

// These benchmarks report the writer-lock hold time, per-fsync latency, and
// per-append allocation rate through the store's Metrics and the testing
// harness (-benchmem / -memprofile / -cpuprofile). Percentile fsync
// distributions for the release regression budget are collected from the
// profiles rather than inlined here.
func BenchmarkAppend(b *testing.B) {
	for _, stateSize := range []int{100, 10000} {
		b.Run(fmt.Sprintf("state=%d", stateSize), func(b *testing.B) {
			s, err := Open(b.TempDir(), Options{})
			if err != nil {
				b.Fatal(err)
			}
			defer s.Close()
			for i := 0; i < stateSize; i++ {
				if _, err := s.Put(fmt.Sprintf("k%d", i), i); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := s.Put(fmt.Sprintf("bench%d", i), i); err != nil {
					b.Fatal(err)
				}
			}
			m := s.Metrics()
			if m.Appends == 0 {
				b.Fatal("no appends counted")
			}
			b.ReportMetric(float64(m.FsyncTotalNs)/float64(m.FsyncCount), "ns/fsync")
			b.ReportMetric(float64(m.WriterLockHoldNs)/float64(m.Appends), "ns/lock-hold")
		})
	}
}

func BenchmarkCompaction(b *testing.B) {
	for _, stateSize := range []int{100, 10000} {
		b.Run(fmt.Sprintf("state=%d", stateSize), func(b *testing.B) {
			s, err := Open(b.TempDir(), Options{})
			if err != nil {
				b.Fatal(err)
			}
			defer s.Close()
			for i := 0; i < stateSize; i++ {
				if _, err := s.Put(fmt.Sprintf("k%d", i), i); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := s.Compact(); err != nil {
					b.Fatal(err)
				}
			}
			m := s.Metrics()
			if m.Compactions == 0 {
				b.Fatal("no compactions counted")
			}
			b.ReportMetric(float64(m.CompactionDurationNs)/float64(m.Compactions), "ns/compaction")
		})
	}
}
