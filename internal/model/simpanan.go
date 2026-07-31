// Package model mendefinisikan struct domain untuk fitur simpanan anggota koperasi.
package model

import "time"

// Simpanan merepresentasikan rekening tabungan milik seorang anggota koperasi.
// Setiap anggota memiliki tepat satu rekening simpanan.
type Simpanan struct {
	ID        int64     `db:"id"         json:"id"`
	UserID    int64     `db:"user_id"    json:"user_id"`
	Saldo     float64   `db:"saldo"      json:"saldo"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
	UpdatedAt time.Time `db:"updated_at" json:"updated_at"`
}

// TransaksiSimpanan mencatat setiap mutasi saldo: setor atau tarik.
// Tabel ini adalah "buku besar" — TIDAK BOLEH diubah setelah commit,
// hanya boleh ditambah (insert-only / append-only).
type TransaksiSimpanan struct {
	ID         int64     `db:"id"          json:"id"`
	SimpananID int64     `db:"simpanan_id" json:"simpanan_id"`
	Jenis      string    `db:"jenis"       json:"jenis"`  // "setor" | "tarik"
	Jumlah     float64   `db:"jumlah"      json:"jumlah"` // selalu positif
	Catatan    string    `db:"catatan"     json:"catatan"`
	CreatedAt  time.Time `db:"created_at"  json:"created_at"`
}

// SetorRequest adalah payload JSON untuk endpoint POST /simpanan/setor.
type SetorRequest struct {
	Jumlah  float64 `json:"jumlah"  binding:"required,gt=0"`
	Catatan string  `json:"catatan"`
}

// TarikRequest adalah payload JSON untuk endpoint POST /simpanan/tarik.
type TarikRequest struct {
	Jumlah  float64 `json:"jumlah"  binding:"required,gt=0"`
	Catatan string  `json:"catatan"`
}
