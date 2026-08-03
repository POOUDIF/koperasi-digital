package util

import "math"

// RoundTo4Decimals membulatkan nilai float64 ke 4 angka di belakang koma.
// Digunakan secara konsisten untuk perhitungan finansial krusial dalam
func RoundTo4Decimals(val float64) float64 {
	return math.Round(val*10000) / 10000
}
