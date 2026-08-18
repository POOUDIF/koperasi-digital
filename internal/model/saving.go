// Package model mendefinisikan struct domain untuk fitur simpanan syariah.
// Tiga entitas utama:
package model

import "time"

// SavingsProduct merepresentasikan produk simpanan yang ditawarkan koperasi.
// Akad yang didukung:
type SavingsProduct struct {
	ID int64 `db:"id" json:"id"`

	// Name adalah nama produk, contoh: "Simpanan Pokok", "Simpanan Sukarela".
	Name string `db:"name" json:"name"`

	// AkadType adalah jenis akad syariah: "Wadiah" atau "Mudharabah".
	AkadType string `db:"akad_type" json:"akad_type"`

	// MinDeposit adalah setoran minimum per transaksi dalam satuan Rupiah.
	MinDeposit float64 `db:"min_deposit" json:"min_deposit"`

	// ProfitSharingRatio adalah nisbah bagi hasil untuk anggota (0.00 – 1.00).
	// Contoh: 0.60 berarti 60% keuntungan pengelolaan menjadi hak anggota.
	ProfitSharingRatio float64 `db:"profit_sharing_ratio" json:"profit_sharing_ratio"`

	// IsMandatory menandakan apakah simpanan ini wajib bagi semua anggota.
	IsMandatory bool `db:"is_mandatory" json:"is_mandatory"`

	CreatedAt time.Time `db:"created_at" json:"created_at"`
	UpdatedAt time.Time `db:"updated_at" json:"updated_at"`
}

// SavingsAccount merepresentasikan rekening simpanan milik seorang anggota.
// Saldo (Balance) hanya boleh diubah melalui operasi atomik di repository layer
type SavingsAccount struct {
	ID int64 `db:"id" json:"id"`

	// UserID adalah foreign key ke tabel users — pemilik rekening.
	UserID int64 `db:"user_id" json:"user_id"`

	// SavingsProductID adalah foreign key ke tabel savings_products.
	SavingsProductID int64 `db:"savings_product_id" json:"savings_product_id"`

	// Balance adalah saldo berjalan dalam satuan Rupiah.
	// Nilai ini selalu >= 0 dan di-enforce oleh CHECK constraint di database.
	Balance float64 `db:"balance" json:"balance"`

	// Status rekening: "active" | "frozen" | "closed".
	Status string `db:"status" json:"status"`

	CreatedAt time.Time `db:"created_at" json:"created_at"`
	UpdatedAt time.Time `db:"updated_at" json:"updated_at"`
}

// SavingsTransaction adalah catatan satu mutasi saldo (deposit atau withdraw).
// PENTING — APPEND-ONLY: Baris di tabel ini adalah audit trail keuangan syariah.
type SavingsTransaction struct {
	ID int64 `db:"id" json:"id"`

	// SavingsAccountID adalah foreign key ke rekening yang mengalami mutasi.
	SavingsAccountID int64 `db:"savings_account_id" json:"savings_account_id"`

	// Type adalah arah mutasi: "deposit" (uang masuk) atau "withdraw" (uang keluar).
	Type string `db:"type" json:"type"`

	// Amount adalah nominal transaksi — selalu positif.
	// Arah uang ditentukan oleh field Type, bukan tanda positif/negatif di Amount.
	Amount float64 `db:"amount" json:"amount"`

	// ReferenceID adalah nomor referensi eksternal (bukti setor, nomor transfer, dsb.).
	// Boleh kosong untuk transaksi tunai tanpa nomor referensi.
	ReferenceID string `db:"reference_id" json:"reference_id"`

	// Tidak ada UpdatedAt — baris ini tidak pernah diubah setelah insert.
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

// =============================================================================
// Request/Response Structs — Payload HTTP untuk modul simpanan

// OpenAccountRequest adalah payload JSON untuk POST /api/v1/savings/accounts.
// Anggota memilih produk simpanan mana yang ingin dibuka.
type OpenAccountRequest struct {
	SavingsProductID int64 `json:"savings_product_id" binding:"required,gt=0"`
}

// DepositRequest adalah payload JSON untuk POST /api/v1/savings/deposit.
type DepositRequest struct {
	// AccountID adalah ID rekening simpanan yang dituju.
	AccountID int64 `json:"account_id" binding:"required,gt=0"`

	// Amount adalah nominal setoran — harus positif.
	Amount float64 `json:"amount" binding:"required,gt=0"`

	// PaymentMethod adalah cara pembayaran (misal: "manual_transfer")
	PaymentMethod string `json:"payment_method" binding:"required"`

	// ProofImageURL adalah link gambar bukti transfer (opsional jika VA, wajib jika manual, tapi kita buat opsional dulu)
	ProofImageURL string `json:"proof_image_url"`

	// ReferenceID adalah nomor referensi eksternal (opsional).
	// Contoh: nomor bukti transfer, kode teller, dsb.
	ReferenceID string `json:"reference_id"`
}

// DepositRequestModel merepresentasikan tabel deposit_requests di database.
type DepositRequestModel struct {
	ID               int64      `db:"id" json:"id"`
	UserID           int64      `db:"user_id" json:"user_id"`
	SavingsAccountID int64      `db:"savings_account_id" json:"savings_account_id"`
	Amount           float64    `db:"amount" json:"amount"`
	PaymentMethod    string     `db:"payment_method" json:"payment_method"`
	ProofImageURL    string     `db:"proof_image_url" json:"proof_image_url"`
	Status           string     `db:"status" json:"status"`
	ReferenceID      string     `db:"reference_id" json:"reference_id"`
	ReviewedBy       *int64     `db:"reviewed_by" json:"reviewed_by"`
	ReviewedAt       *time.Time `db:"reviewed_at" json:"reviewed_at"`
	CreatedAt        time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt        time.Time  `db:"updated_at" json:"updated_at"`
}

// ReviewDepositRequest adalah payload JSON untuk admin menyetujui/menolak setoran.
type ReviewDepositRequest struct {
	Action string `json:"action" binding:"required,oneof=approve reject"`
}

