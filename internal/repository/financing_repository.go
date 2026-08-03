// Package repository — implementasi FinancingRepository untuk PostgreSQL.
// Pola identik dengan UserRepository dan SavingRepository:
package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/lib/pq"

	"koperasi-digital/internal/model"
)

// Sentinel errors untuk modul pembiayaan.
var (
	// ErrFinancingNotFound dikembalikan saat pengajuan dengan kriteria tertentu tidak ada.
	ErrFinancingNotFound = errors.New("pengajuan pembiayaan tidak ditemukan")

	// ErrDuplicateFinancingNumber dikembalikan jika financing_number sudah dipakai.
	// Dalam praktik sangat jarang karena kita pakai unix nano, tapi ditangani secara defensif.
	ErrDuplicateFinancingNumber = errors.New("nomor pembiayaan sudah digunakan")

	// ErrInstallmentNotFound dikembalikan saat cicilan dengan id tertentu tidak ditemukan.
	ErrInstallmentNotFound = errors.New("cicilan tidak ditemukan")

	// ErrInstallmentAlreadyPaid dikembalikan saat mencoba membayar cicilan yang
	// sudah berstatus 'paid'. Bisa terjadi sebagai race condition (dua request
	ErrInstallmentAlreadyPaid = errors.New("cicilan sudah dibayar sebelumnya")

	// ErrInsufficientBalance dikembalikan saat saldo rekening simpanan tidak cukup
	// untuk membayar cicilan yang diminta.
	ErrInsufficientBalance = errors.New("saldo rekening tidak mencukupi")
)

// FinancingRepository mendefinisikan kontrak operasi database untuk modul pembiayaan.
// Service layer hanya bergantung pada interface ini — bukan struct konkret.
type FinancingRepository interface {
	// CreateFinancing menyimpan pengajuan pembiayaan baru ke database.
	// Mengembalikan pointer ke Financing yang sudah terisi id dan created_at dari DB.
	CreateFinancing(ctx context.Context, f *model.Financing) (*model.Financing, error)

	// FindByID mengambil satu pengajuan berdasarkan primary key.
	// Mengembalikan ErrFinancingNotFound jika tidak ada.
	FindByID(ctx context.Context, id int64) (*model.Financing, error)

	// FindByUserID mengambil semua pengajuan milik satu user,
	// diurutkan dari yang terbaru.
	FindByUserID(ctx context.Context, userID int64) ([]model.Financing, error)

	// UpdateStatus memperbarui status dan mencatat reviewer pada satu pengajuan.
	// Dipakai untuk operasi sederhana (reject) yang tidak perlu generate data lain.
	UpdateStatus(ctx context.Context, id int64, status string, reviewedBy int64) error

	// ApproveWithInstallments mengeksekusi approval secara ATOMIK dalam satu
	// database transaction:
	ApproveWithInstallments(ctx context.Context, financingID int64, reviewedBy int64, installments []model.FinancingInstallment) error

	// GetInstallmentsByFinancingID mengambil semua baris angsuran milik satu pengajuan,
	// diurutkan dari angsuran pertama ke terakhir.
	GetInstallmentsByFinancingID(ctx context.Context, financingID int64) ([]model.FinancingInstallment, error)

	// GetInstallmentByID mengambil satu baris angsuran berdasarkan primary key.
	// Mengembalikan ErrInstallmentNotFound jika tidak ada.
	GetInstallmentByID(ctx context.Context, id int64) (*model.FinancingInstallment, error)

	// PayInstallment mengeksekusi pembayaran satu cicilan secara ATOMIK dalam satu
	// database transaction:
	PayInstallment(ctx context.Context, installmentID int64, financingID int64, amountDue float64, accountID int64, userID int64) error
}

// postgresFinancingRepository adalah implementasi konkret dengan PostgreSQL.
type postgresFinancingRepository struct {
	db *sql.DB
}

// NewFinancingRepository membuat instance repository baru.
// Mengembalikan interface agar caller tidak bisa bergantung pada detail implementasi.
func NewFinancingRepository(db *sql.DB) FinancingRepository {
	return &postgresFinancingRepository{db: db}
}

// scanFinancing adalah helper untuk meng-Scan satu baris financing ke struct.
// Dipusatkan di sini agar semua query (FindByID, FindByUserID, dsb.) menggunakan
func scanFinancing(row interface {
	Scan(dest ...any) error
}, f *model.Financing) error {
	return row.Scan(
		&f.ID, &f.FinancingNumber, &f.UserID, &f.Akad,
		&f.PrincipalAmount, &f.MarginAmount, &f.TotalPayable,
		&f.DurationMonths, &f.Status,
		&f.ReviewedBy, &f.ReviewedAt,
		&f.CreatedAt,
	)
}

// financingSelectColumns adalah daftar kolom yang di-SELECT pada semua query financing.
// Konstanta ini memastikan urutan kolom konsisten dengan scanFinancing di atas.
const financingSelectColumns = `
	id, financing_number, user_id, akad,
	principal_amount, margin_amount, total_payable,
	duration_months, status,
	reviewed_by, reviewed_at,
	created_at
`

// CreateFinancing menyimpan pengajuan pembiayaan baru ke tabel `financing`.
// Kolom yang di-insert dari application: semua field bisnis.
func (r *postgresFinancingRepository) CreateFinancing(ctx context.Context, f *model.Financing) (*model.Financing, error) {
	query := `
		INSERT INTO financing (
			financing_number,
			user_id,
			akad,
			principal_amount,
			margin_amount,
			total_payable,
			duration_months,
			status
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, created_at
	`

	err := r.db.QueryRowContext(ctx, query,
		f.FinancingNumber,
		f.UserID,
		f.Akad,
		f.PrincipalAmount,
		f.MarginAmount,
		f.TotalPayable,
		f.DurationMonths,
		f.Status,
	).Scan(&f.ID, &f.CreatedAt)

	if err != nil {
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code == pgUniqueViolation {
			return nil, ErrDuplicateFinancingNumber
		}
		return nil, fmt.Errorf("insert pengajuan pembiayaan gagal: %w", err)
	}

	return f, nil
}

// FindByID mengambil satu pengajuan pembiayaan berdasarkan primary key.
func (r *postgresFinancingRepository) FindByID(ctx context.Context, id int64) (*model.Financing, error) {
	query := `SELECT` + financingSelectColumns + `FROM financing WHERE id = $1 LIMIT 1`

	f := &model.Financing{}
	if err := scanFinancing(r.db.QueryRowContext(ctx, query, id), f); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrFinancingNotFound
		}
		return nil, fmt.Errorf("query pembiayaan by id gagal: %w", err)
	}

	return f, nil
}

// FindByUserID mengambil semua pengajuan pembiayaan milik satu user.
// Mengembalikan slice kosong (bukan nil) jika tidak ada pengajuan.
func (r *postgresFinancingRepository) FindByUserID(ctx context.Context, userID int64) ([]model.Financing, error) {
	query := `SELECT` + financingSelectColumns + `FROM financing WHERE user_id = $1 ORDER BY created_at DESC`

	rows, err := r.db.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("query daftar pembiayaan gagal: %w", err)
	}
	defer rows.Close()

	financings := make([]model.Financing, 0)
	for rows.Next() {
		var f model.Financing
		if err := scanFinancing(rows, &f); err != nil {
			return nil, fmt.Errorf("scan data pembiayaan gagal: %w", err)
		}
		financings = append(financings, f)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterasi data pembiayaan gagal: %w", err)
	}

	return financings, nil
}

// UpdateStatus memperbarui status dan menyimpan data reviewer pada satu pengajuan.
// Digunakan untuk operasi sederhana yang tidak menghasilkan data turunan,
func (r *postgresFinancingRepository) UpdateStatus(ctx context.Context, id int64, status string, reviewedBy int64) error {
	query := `
		UPDATE financing
		SET    status      = $1,
		       reviewed_by = $2,
		       reviewed_at = NOW()
		WHERE  id = $3
	`

	result, err := r.db.ExecContext(ctx, query, status, reviewedBy, id)
	if err != nil {
		return fmt.Errorf("update status pembiayaan gagal: %w", err)
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		return ErrFinancingNotFound
	}

	return nil
}

// ApproveWithInstallments mengeksekusi persetujuan pembiayaan secara ATOMIK.
// Kenapa harus database transaction?
func (r *postgresFinancingRepository) ApproveWithInstallments(
	ctx context.Context,
	financingID int64,
	reviewedBy int64,
	installments []model.FinancingInstallment,
) error {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("gagal memulai transaksi DB: %w", err)
	}
	// Safety net: rollback otomatis jika return sebelum Commit.
	// Setelah Commit, Rollback menjadi no-op.
	defer tx.Rollback() //nolint:errcheck

	// Langkah 1: Update status financing.
	updateQuery := `
		UPDATE financing
		SET    status      = 'approved',
		       reviewed_by = $1,
		       reviewed_at = NOW()
		WHERE  id          = $2
	`
	result, err := tx.ExecContext(ctx, updateQuery, reviewedBy, financingID)
	if err != nil {
		return fmt.Errorf("update status approval gagal: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrFinancingNotFound
	}

	// Langkah 2: Insert semua baris angsuran.
	// Kita prepare statement sekali dan eksekusi berkali-kali — lebih efisien
	insertQuery := `
		INSERT INTO financing_installments
			(financing_id, installment_number, amount_due, amount_paid, due_date, status)
		VALUES ($1, $2, $3, $4, $5, 'unpaid')
	`
	stmt, err := tx.PrepareContext(ctx, insertQuery)
	if err != nil {
		return fmt.Errorf("prepare insert angsuran gagal: %w", err)
	}
	defer stmt.Close()

	for _, inst := range installments {
		if _, err := stmt.ExecContext(ctx,
			financingID,
			inst.InstallmentNumber,
			inst.AmountDue,
			inst.AmountPaid, // selalu 0 saat baru dibuat
			inst.DueDate,
		); err != nil {
			return fmt.Errorf("insert angsuran ke-%d gagal: %w", inst.InstallmentNumber, err)
		}
	}

	// Langkah 3: Commit — hanya setelah update + semua insert berhasil.
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaksi approval gagal: %w", err)
	}

	return nil
}

// =============================================================================
// Installment helpers

// installmentSelectColumns adalah urutan kolom yang konsisten untuk semua
// query yang meng-SELECT dari financing_installments.
const installmentSelectColumns = `
	id, financing_id, installment_number,
	amount_due, amount_paid,
	due_date, status, paid_at
`

// scanInstallment memetakan satu baris hasil query ke struct FinancingInstallment.
// Dipusatkan di sini agar semua query pakai urutan kolom yang sama.
func scanInstallment(row interface {
	Scan(dest ...any) error
}, inst *model.FinancingInstallment) error {
	return row.Scan(
		&inst.ID, &inst.FinancingID, &inst.InstallmentNumber,
		&inst.AmountDue, &inst.AmountPaid,
		&inst.DueDate, &inst.Status, &inst.PaidAt,
	)
}

// GetInstallmentsByFinancingID mengambil semua cicilan milik satu pengajuan,
// diurutkan dari angsuran pertama ke terakhir (installment_number ASC).
func (r *postgresFinancingRepository) GetInstallmentsByFinancingID(ctx context.Context, financingID int64) ([]model.FinancingInstallment, error) {
	query := `SELECT` + installmentSelectColumns + `FROM financing_installments WHERE financing_id = $1 ORDER BY installment_number ASC`

	rows, err := r.db.QueryContext(ctx, query, financingID)
	if err != nil {
		return nil, fmt.Errorf("query cicilan pembiayaan gagal: %w", err)
	}
	defer rows.Close()

	installments := make([]model.FinancingInstallment, 0)
	for rows.Next() {
		var inst model.FinancingInstallment
		if err := scanInstallment(rows, &inst); err != nil {
			return nil, fmt.Errorf("scan cicilan gagal: %w", err)
		}
		installments = append(installments, inst)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterasi cicilan gagal: %w", err)
	}

	return installments, nil
}

// GetInstallmentByID mengambil satu cicilan berdasarkan primary key.
func (r *postgresFinancingRepository) GetInstallmentByID(ctx context.Context, id int64) (*model.FinancingInstallment, error) {
	query := `SELECT` + installmentSelectColumns + `FROM financing_installments WHERE id = $1 LIMIT 1`

	inst := &model.FinancingInstallment{}
	if err := scanInstallment(r.db.QueryRowContext(ctx, query, id), inst); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrInstallmentNotFound
		}
		return nil, fmt.Errorf("query cicilan by id gagal: %w", err)
	}

	return inst, nil
}

// PayInstallment mengeksekusi pembayaran satu cicilan secara ATOMIK.
// Kenapa satu transaction yang menyentuh dua modul (simpanan & pembiayaan)?
func (r *postgresFinancingRepository) PayInstallment(
	ctx context.Context,
	installmentID int64,
	financingID int64,
	amountDue float64,
	accountID int64,
	userID int64,
) error {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("gagal memulai transaksi DB: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// --- a. Kunci rekening simpanan & validasi ---
	// FOR UPDATE mengunci baris agar tidak ada transaksi lain yang membaca
	var accUserID int64
	var balance float64
	var accStatus string

	lockQuery := `
		SELECT user_id, balance, status
		FROM   savings_accounts
		WHERE  id = $1
		FOR UPDATE
	`
	err = tx.QueryRowContext(ctx, lockQuery, accountID).Scan(&accUserID, &balance, &accStatus)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrSavingsAccountNotFound
		}
		return fmt.Errorf("lock rekening simpanan gagal: %w", err)
	}

	// Validasi kepemilikan — rekening harus milik user yang sedang login.
	// Return "not found" (bukan "forbidden") untuk mencegah enumerasi ID rekening.
	if accUserID != userID {
		return ErrSavingsAccountNotFound
	}

	if accStatus != "active" {
		return ErrAccountNotActive
	}

	if balance < amountDue {
		return ErrInsufficientBalance
	}

	// --- b. Kurangi saldo rekening ---
	updateBalanceQuery := `
		UPDATE savings_accounts
		SET    balance    = balance - $1,
		       updated_at = NOW()
		WHERE  id = $2
	`
	if _, err = tx.ExecContext(ctx, updateBalanceQuery, amountDue, accountID); err != nil {
		return fmt.Errorf("pengurangan saldo rekening gagal: %w", err)
	}

	// --- c. Catat mutasi debit di buku besar simpanan ---
	referenceID := fmt.Sprintf("cicilan_%d", installmentID)
	insertLogQuery := `
		INSERT INTO savings_transactions (savings_account_id, type, amount, reference_id)
		VALUES ($1, 'withdraw', $2, $3)
	`
	if _, err = tx.ExecContext(ctx, insertLogQuery, accountID, amountDue, referenceID); err != nil {
		return fmt.Errorf("catat log pembayaran cicilan gagal: %w", err)
	}

	// --- d. Update status cicilan → 'paid' ---
	// AND status='unpaid' pada WHERE memberikan proteksi ekstra terhadap race condition:
	updateInstallmentQuery := `
		UPDATE financing_installments
		SET    status      = 'paid',
		       amount_paid = $1,
		       paid_at     = NOW()
		WHERE  id          = $2
		  AND  financing_id = $3
		  AND  status      = 'unpaid'
	`
	result, err := tx.ExecContext(ctx, updateInstallmentQuery, amountDue, installmentID, financingID)
	if err != nil {
		return fmt.Errorf("update status cicilan gagal: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		// Race condition: cicilan sudah dibayar oleh request lain di sela waktu.
		return ErrInstallmentAlreadyPaid
	}

	// --- e. Cek apakah semua cicilan sudah lunas → tutup pembiayaan ---
	var unpaidCount int
	countQuery := `SELECT COUNT(*) FROM financing_installments WHERE financing_id = $1 AND status = 'unpaid'`
	if err = tx.QueryRowContext(ctx, countQuery, financingID).Scan(&unpaidCount); err != nil {
		return fmt.Errorf("hitung sisa cicilan gagal: %w", err)
	}

	if unpaidCount == 0 {
		updateFinancingQuery := `UPDATE financing SET status = 'paid' WHERE id = $1`
		if _, err = tx.ExecContext(ctx, updateFinancingQuery, financingID); err != nil {
			return fmt.Errorf("update status pembiayaan menjadi paid gagal: %w", err)
		}
	}

	// --- Commit: semua langkah berhasil ---
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit transaksi pembayaran cicilan gagal: %w", err)
	}

	return nil
}

// Pastikan *sql.Rows memenuhi interface yang dibutuhkan scanFinancing.
// Ini adalah compile-time check — jika gagal, akan error saat build.
var _ interface{ Scan(...any) error } = (*sql.Rows)(nil)

// Pastikan *sql.Row juga memenuhi interface yang sama.
var _ interface{ Scan(...any) error } = (*sql.Row)(nil)

// reviewedAt dipakai sebagai tipe nullable timestamp dalam scan.
// lib/pq menangani NULL TIMESTAMPTZ → *time.Time dengan benar.
var _ *time.Time = nil
