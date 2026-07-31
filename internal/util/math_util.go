package util

import "math"

// RoundTo4Decimals membulatkan nilai float64 ke 4 angka di belakang koma.
//
// Digunakan secara konsisten untuk perhitungan finansial krusial dalam
// aplikasi Kopersi Digital (seperti akumulasi jumlah emas dan cicilan
// pembiayaan murabahah) agar tidak timbul presisi yang floating yang
// terakumulasi.
//
// Cara kerja:
// nilai 1.23456 * 10000 = 12345.6
// dibulatkan (math.Round) = 12346
// dibagi / 10000 = 1.2346
func RoundTo4Decimals(val float64) float64 {
	return math.Round(val*10000) / 10000
}
