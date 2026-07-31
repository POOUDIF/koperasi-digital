// Package model — struct domain untuk modul Jual Beli Emas Digital.
//
// Dua entitas utama:
//   - GoldPrice       : snapshot harga emas per gram yang ditetapkan koperasi.
//   - GoldTransaction : satu transaksi jual atau beli emas oleh anggota.
//
// Hubungan dengan blockchain:
//   - GoldTransaction.TxHash diisi oleh worker/job setelah transaksi dikonfirmasi
//     on-chain di Polygon. NULL selama status masih 'pending'/'processing'.
package model

import "time"

// GoldPrice merepresentasikan satu snapshot harga emas per gram.
//
// Harga diperbarui secara periodik oleh admin atau price-feed oracle.
// Endpoint GET /gold/price selalu mengembalikan baris dengan UpdatedAt terbaru.
//
// Spread (buy - sell) adalah keuntungan koperasi dari selisih harga jual dan beli.
type GoldPrice struct {
	ID int64 `db:"id" json:"id"`

	// BuyPricePerGram adalah harga yang dibayar anggota saat MEMBELI emas
	// dari koperasi (sudah termasuk spread/margin koperasi).
	BuyPricePerGram float64 `db:"buy_price_per_gram" json:"buy_price_per_gram"`

	// SellPricePerGram adalah harga yang diterima anggota saat MENJUAL emas
	// ke koperasi (harga beli koperasi dari anggota).
	SellPricePerGram float64 `db:"sell_price_per_gram" json:"sell_price_per_gram"`

	// UpdatedAt adalah waktu harga ini di-set. Dipakai untuk menentukan harga terkini.
	UpdatedAt time.Time `db:"updated_at" json:"updated_at"`
}

// GoldTransaction merepresentasikan satu transaksi jual/beli emas oleh anggota.
//
// Alur status:
//
//	pending → processing → success   (jalur normal)
//	pending → processing → failed    (gagal on-chain, saldo di-refund)
//	pending →             failed     (gagal sebelum dikirim ke chain)
//
// PricePerGram dan TotalRupiah disimpan per-transaksi agar perubahan harga
// di gold_prices tidak mengubah nilai transaksi yang sudah terjadi.
type GoldTransaction struct {
	ID int64 `db:"id" json:"id"`

	// UserID adalah foreign key ke tabel users.
	UserID int64 `db:"user_id" json:"user_id"`

	// Type adalah arah transaksi: "buy" (beli emas) atau "sell" (jual emas).
	Type string `db:"type" json:"type"`

	// GramAmount adalah berat emas dalam satuan gram (presisi 4 desimal).
	// Contoh: 0.5000 = setengah gram.
	GramAmount float64 `db:"gram_amount" json:"gram_amount"`

	// PricePerGram adalah harga per gram yang berlaku saat transaksi dikunci.
	// Nilai ini tidak berubah meski harga pasar bergerak setelah transaksi dibuat.
	PricePerGram float64 `db:"price_per_gram" json:"price_per_gram"`

	// TotalRupiah = GramAmount × PricePerGram, dalam satuan Rupiah.
	// Jumlah inilah yang didebet dari rekening simpanan anggota.
	TotalRupiah float64 `db:"total_rupiah" json:"total_rupiah"`

	// TxHash adalah hash transaksi on-chain dari Polygon Smart Contract.
	// NULL selama status 'pending'/'processing'; diisi setelah konfirmasi blockchain.
	// Pakai *string agar bisa dibedakan antara "belum ada" (nil) dan hash kosong "".
	TxHash *string `db:"tx_hash" json:"tx_hash,omitempty"`

	// Status alur:
	//   pending    : saldo dipotong, menunggu antrian worker blockchain.
	//   processing : sudah dikirim ke Polygon, menunggu konfirmasi blok.
	//   success    : dikonfirmasi on-chain, token emas diterbitkan.
	//   failed     : gagal; jika saldo sudah dipotong, proses refund dipicu.
	Status string `db:"status" json:"status"`

	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

// =============================================================================
// Request / Response Payload — HTTP layer untuk modul emas
// =============================================================================

// BuyGoldRequest adalah payload JSON untuk POST /api/v1/gold/buy.
//
// Anggota memilih berapa gram yang ingin dibeli dan dari rekening simpanan
// Wadiah mana dana akan didebet.
type BuyGoldRequest struct {
	// GramAmount adalah berat emas yang ingin dibeli dalam satuan gram.
	// Harus lebih dari 0. Tidak ada batas maksimum di level validasi Gin —
	// batas bisnis (misalnya maks 100 gram/hari) bisa ditambahkan di service layer.
	// Batas minimum 0.0001 gram untuk mencegah presisi error dari float64.
	GramAmount float64 `json:"gram_amount" binding:"required,min=0.0001"`

	// SavingsAccountID adalah ID rekening simpanan Wadiah yang saldonya akan didebet.
	// Rekening harus aktif dan milik user yang sedang login.
	SavingsAccountID int64 `json:"savings_account_id" binding:"required,gt=0"`
}

// SellGoldRequest adalah payload JSON untuk POST /api/v1/gold/sell.
//
// Anggota menjual emas mereka dan meminta dana hasil penjualannya dikreditkan
// ke rekening Wadiah.
type SellGoldRequest struct {
	// GramAmount adalah berat emas yang ingin dijual.
	// Batas minimum 0.0001 gram.
	GramAmount float64 `json:"gram_amount" binding:"required,min=0.0001"`

	// SavingsAccountID adalah ID rekening simpanan Wadiah penerima transfer.
	SavingsAccountID int64 `json:"savings_account_id" binding:"required,gt=0"`
}
