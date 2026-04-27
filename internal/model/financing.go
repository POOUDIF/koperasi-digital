// Package model — struct domain untuk modul Pembiayaan Syariah.
//
// Entitas dalam modul ini:
//   - Financing            : master data satu pengajuan pembiayaan.
//   - FinancingInstallment : satu baris jadwal angsuran bulanan.
//
// Request/response payload HTTP juga didefinisikan di sini agar tetap
// satu package dengan domain-nya (konsisten dengan pola model/saving.go).
package model

import "time"

// Financing merepresentasikan satu pengajuan pembiayaan syariah.
//
// Setelah akad disepakati (status → approved), nilai PrincipalAmount,
// MarginAmount, dan TotalPayable TIDAK BOLEH berubah — prinsip transparansi
// murabahah mengharuskan harga jual ditetapkan dan diketahui anggota di awal.
type Financing struct {
	ID int64 `db:"id" json:"id"`

	// FinancingNumber adalah nomor unik yang tercantum di dokumen akad.
	// Format: "FIN-MRB-<unix_nano>" — dihasilkan oleh service saat pengajuan.
	FinancingNumber string `db:"financing_number" json:"financing_number"`

	// UserID adalah foreign key ke tabel users — pengaju pembiayaan.
	UserID int64 `db:"user_id" json:"user_id"`

	// Akad syariah yang digunakan. Saat ini selalu "murabahah".
	// Kolom ini disiapkan untuk ekspansi ke akad lain di iterasi berikutnya.
	Akad string `db:"akad" json:"akad"`

	// PrincipalAmount adalah harga pokok barang yang dibiayai koperasi.
	// Ini adalah nilai yang anggota ajukan, sebelum ditambah margin.
	PrincipalAmount float64 `db:"principal_amount" json:"principal_amount"`

	// MarginAmount adalah keuntungan koperasi yang ditetapkan dan disepakati di awal.
	// Dihitung oleh service (saat ini: 10% dari PrincipalAmount).
	MarginAmount float64 `db:"margin_amount" json:"margin_amount"`

	// TotalPayable adalah total yang harus dibayar anggota = PrincipalAmount + MarginAmount.
	// Di-persist agar nilai ini tidak berubah meski kebijakan margin berubah di masa depan.
	TotalPayable float64 `db:"total_payable" json:"total_payable"`

	// DurationMonths adalah tenor pembiayaan dalam satuan bulan (1–360).
	DurationMonths int `db:"duration_months" json:"duration_months"`

	// Status alur pengajuan:
	//   pending  → approved → active → paid
	//   pending  → rejected
	Status string `db:"status" json:"status"`

	// ReviewedBy adalah user_id admin/pengurus yang melakukan approve atau reject.
	// NULL selama status masih 'pending'. Pakai pointer agar bisa dibedakan antara
	// "belum direview" (nil) dan "direview oleh user ID 0" (tidak valid).
	ReviewedBy *int64 `db:"reviewed_by" json:"reviewed_by,omitempty"`

	// ReviewedAt adalah timestamp keputusan review. NULL selama pending.
	ReviewedAt *time.Time `db:"reviewed_at" json:"reviewed_at,omitempty"`

	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

// FinancingInstallment merepresentasikan satu baris jadwal angsuran bulanan.
//
// Baris ini dibuat oleh sistem saat pembiayaan disetujui (status → active).
// Setiap baris mewakili satu bulan: nomor angsuran, tanggal jatuh tempo,
// nominal yang harus dibayar, dan berapa yang sudah dibayar.
type FinancingInstallment struct {
	ID int64 `db:"id" json:"id"`

	// FinancingID adalah foreign key ke tabel financing.
	FinancingID int64 `db:"financing_id" json:"financing_id"`

	// InstallmentNumber adalah urutan angsuran: 1 untuk bulan pertama, dst.
	InstallmentNumber int `db:"installment_number" json:"installment_number"`

	// AmountDue adalah nominal angsuran sesuai jadwal.
	// Untuk murabahah flat: TotalPayable / DurationMonths.
	AmountDue float64 `db:"amount_due" json:"amount_due"`

	// AmountPaid adalah total yang sudah dibayarkan anggota untuk angsuran ini.
	// Default 0 saat baris pertama kali dibuat.
	AmountPaid float64 `db:"amount_paid" json:"amount_paid"`

	// DueDate adalah tanggal jatuh tempo angsuran (tipe date, bukan timestamp).
	DueDate time.Time `db:"due_date" json:"due_date"`

	// Status angsuran: "unpaid" atau "paid".
	Status string `db:"status" json:"status"`

	// PaidAt adalah timestamp saat angsuran dilunasi. NULL selama status masih 'unpaid'.
	PaidAt *time.Time `db:"paid_at" json:"paid_at,omitempty"`
}

// =============================================================================
// Request / Response Payload — HTTP layer untuk modul pembiayaan
// =============================================================================

// ApplyFinancingRequest adalah payload JSON untuk POST /api/v1/financing/apply.
//
// Dua field ini cukup untuk versi awal — service akan menghitung margin,
// total, dan menghasilkan nomor pengajuan secara otomatis.
type ApplyFinancingRequest struct {
	// PrincipalAmount adalah harga pokok barang yang ingin dibiayai (dalam Rupiah).
	// Harus lebih dari 0.
	PrincipalAmount float64 `json:"principal_amount" binding:"required,gt=0"`

	// DurationMonths adalah tenor yang diinginkan (dalam bulan).
	// Dibatasi antara 1 bulan (minimum) hingga 360 bulan / 30 tahun (maksimum KPR).
	DurationMonths int `json:"duration_months" binding:"required,min=1,max=360"`
}

// ReviewFinancingRequest adalah payload JSON untuk PUT /api/v1/admin/financing/:id/review.
// Admin mengirimkan keputusan: "approve" atau "reject".
type ReviewFinancingRequest struct {
	// Action adalah keputusan admin: "approve" atau "reject".
	// oneof validation memastikan tidak ada nilai selain dua pilihan ini.
	Action string `json:"action" binding:"required,oneof=approve reject"`
}

// PayInstallmentRequest adalah payload JSON untuk POST /api/v1/financing/installments/:id/pay.
// Anggota menentukan rekening simpanan mana yang akan didebet untuk membayar cicilan.
type PayInstallmentRequest struct {
	// SavingsAccountID adalah ID rekening simpanan yang saldonya akan dikurangi.
	// Rekening ini harus aktif, milik user yang sedang login, dan saldonya mencukupi.
	SavingsAccountID int64 `json:"savings_account_id" binding:"required,gt=0"`
}
