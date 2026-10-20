package ui

import "testing"

func BenchmarkFleetFrame(b *testing.B) {
	a := fixture(200, 58, []int{1, 2, 4, 8, 2, 4, 1, 8, 4, 2, 1, 2, 4})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = a.fleetCanvas().ANSI()
	}
}

func BenchmarkDetailFrame(b *testing.B) {
	a := fixture(200, 58, []int{8})
	a.detail = true
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = a.detailCanvas().ANSI()
	}
}
