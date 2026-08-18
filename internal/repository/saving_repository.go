// Package repository — implementasi SavingRepository untuk PostgreSQL.
// Pola yang sama dengan UserRepository:
package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"koperasi-digital/internal/model"
)

// Sentinel errors untuk modul simpanan.
// Service layer membandingkan error ini dengan errors.Is() agar tidak bergantung
var (
	// ErrSavingsAccountNotFound dikembalikan saat rekening tidak ditemukan.
	ErrSavingsAccountNotFound = errors.New("rekening simpanan tidak ditemukan")

	// ErrSavingsProductNotFound dikembalikan saat produk simpanan tidak ditemukan.
	ErrSavingsProductNotFound = errors.New("produk simpanan tidak ditemukan")

	// ErrAccountNotActive dikembalikan saat mencoba transaksi di rekening non-aktif.
	ErrAccountNotActive = errors.New("rekening simpanan tidak aktif")

	// ErrDepositRequestNotFound dikembalikan saat request setoran tidak ditemukan.
	ErrDepositRequestNotFound = errors.New("permohonan setoran tidak ditemukan")
)

// SavingRepository mendefinisikan kontrak operasi database untuk modul simpanan.
type SavingRepository interface {
	// FindProductByID mengambil produk simpanan berdasarkan primary key.
	// Mengembalikan ErrSavingsProductNotFound jika tidak ada.
	FindProductByID(ctx context.Context, productID int64) (*model.SavingsProduct, error)

	// GetMandatoryProducts mengambil semua produk simpanan yang wajib dimiliki anggota.
	GetMandatoryProducts(ctx context.Context) ([]model.SavingsProduct, error)

	// CreateAccount membuka rekening simpanan baru dengan saldo awal 0.
	CreateAccount(ctx context.Context, account *model.SavingsAccount) (*model.SavingsAccount, error)

	// GetAccountByUserID mengambil semua rekening simpanan milik satu user.
	GetAccountByUserID(ctx context.Context, userID int64) ([]model.SavingsAccount, error)

	// GetAccountByID mengambil satu rekening berdasarkan primary key.
	// Mengembalikan ErrSavingsAccountNotFound jika tidak ada.
	GetAccountByID(ctx context.Context, accountID int64) (*model.SavingsAccount, error)

	// Deposit mengeksekusi setoran dana secara ATOMIK menggunakan database transaction:
	// 1. Validasi rekening (status harus 'active') + kunci baris dengan FOR UPDATE.
	Deposit(ctx context.Context, accountID int64, amount float64, referenceID string) error

	// GetAllTransactions mengambil semua mutasi (log) dari semua rekening simpanan.
	GetAllTransactions(ctx context.Context) ([]model.SavingsTransaction, error)

	// InsertDepositRequest menyimpan permohonan setoran baru.
	InsertDepositRequest(ctx context.Context, req *model.DepositRequestModel) (*model.DepositRequestModel, error)

	// UpdateDepositRequestStatus mengupdate status (approved/rejected).
	UpdateDepositRequestStatus(ctx context.Context, tx *sql.Tx, requestID int64, status string, adminID int64) error

	// GetDepositRequestsByUserID mengambil riwayat permohonan setoran milik anggota.
	GetDepositRequestsByUserID(ctx context.Context, userID int64) ([]model.DepositRequestModel, error)

	// GetAllDepositRequests mengambil semua permohonan setoran (untuk admin).
	GetAllDepositRequests(ctx context.Context) ([]model.DepositRequestModel, error)

	// GetDepositRequestByID mengambil satu permohonan berdasarkan ID.
	GetDepositRequestByID(ctx context.Context, requestID int64) (*model.DepositRequestModel, error)

	// BeginTx memulai transaction baru untuk digunakan di layer service.
	BeginTx(ctx context.Context) (*sql.Tx, error)
}

// postgresSavingRepository adalah implementasi SavingRepository dengan PostgreSQL.
type postgresSavingRepository struct {
	db *sql.DB
}

// NewSavingRepository membuat instance repository baru.
// Mengembalikan interface (bukan struct konkret) agar caller tidak bisa
func NewSavingRepository(db *sql.DB) SavingRepository {
	return &postgresSavingRepository{db: db}
}

// FindProductByID mengambil satu produk simpanan berdasarkan primary key.
func (r *postgresSavingRepository) FindProductByID(ctx context.Context, productID int64) (*model.SavingsProduct, error) {
	query := `
		SELECT id, name, akad_type, min_deposit, profit_sharing_ratio, is_mandatory,
		       created_at, updated_at
		FROM   savings_products
		WHERE  id = $1
		LIMIT  1
	`

	p := &model.SavingsProduct{}
	err := r.db.QueryRowContext(ctx, query, productID).Scan(
		&p.ID, &p.Name, &p.AkadType, &p.MinDeposit, &p.ProfitSharingRatio,
		&p.IsMandatory, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrSavingsProductNotFound
		}
		return nil, fmt.Errorf("query produk simpanan gagal: %w", err)
	}

	return p, nil
}

// GetMandatoryProducts mengambil semua produk simpanan yang diwajibkan (is_mandatory = TRUE).
func (r *postgresSavingRepository) GetMandatoryProducts(ctx context.Context) ([]model.SavingsProduct, error) {
	query := `
		SELECT id, name, akad_type, min_deposit, profit_sharing_ratio, is_mandatory,
		       created_at, updated_at
		FROM   savings_products
		WHERE  is_mandatory = TRUE
		ORDER BY id ASC
	`

	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query produk simpanan wajib gagal: %w", err)
	}
	defer rows.Close()

	var products []model.SavingsProduct
	for rows.Next() {
		var p model.SavingsProduct
		if err := rows.Scan(
			&p.ID, &p.Name, &p.AkadType, &p.MinDeposit, &p.ProfitSharingRatio,
			&p.IsMandatory, &p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan produk simpanan wajib gagal: %w", err)
		}
		products = append(products, p)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterasi produk simpanan wajib gagal: %w", err)
	}

	return products, nil
}

// CreateAccount membuka rekening simpanan baru.
// Saldo awal selalu 0 — disematkan langsung di query agar application code
func (r *postgresSavingRepository) CreateAccount(ctx context.Context, account *model.SavingsAccount) (*model.SavingsAccount, error) {
	query := `
		INSERT INTO savings_accounts (user_id, savings_product_id, balance, status)
		VALUES ($1, $2, 0, 'active')
		RETURNING id, balance, status, created_at, updated_at
	`

	err := r.db.QueryRowContext(ctx, query, account.UserID, account.SavingsProductID).Scan(
		&account.ID, &account.Balance, &account.Status,
		&account.CreatedAt, &account.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("buka rekening simpanan gagal: %w", err)
	}

	return account, nil
}

// GetAccountByUserID mengambil semua rekening simpanan milik satu user,
// diurutkan dari yang paling baru dibuka.
func (r *postgresSavingRepository) GetAccountByUserID(ctx context.Context, userID int64) ([]model.SavingsAccount, error) {
	query := `
		SELECT id, user_id, savings_product_id, balance, status, created_at, updated_at
		FROM   savings_accounts
		WHERE  user_id = $1
		ORDER BY created_at DESC
	`

	rows, err := r.db.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("query rekening simpanan gagal: %w", err)
	}
	defer rows.Close()

	accounts := make([]model.SavingsAccount, 0)
	for rows.Next() {
		var a model.SavingsAccount
		if err := rows.Scan(
			&a.ID, &a.UserID, &a.SavingsProductID, &a.Balance,
			&a.Status, &a.CreatedAt, &a.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan rekening simpanan gagal: %w", err)
		}
		accounts = append(accounts, a)
	}

	// rows.Err() wajib dicek setelah loop — bisa berisi error network/driver
	// yang muncul di tengah iterasi, bukan hanya saat Open/Query.
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterasi rekening simpanan gagal: %w", err)
	}

	return accounts, nil
}

// GetAccountByID mengambil satu rekening berdasarkan primary key.
func (r *postgresSavingRepository) GetAccountByID(ctx context.Context, accountID int64) (*model.SavingsAccount, error) {
	query := `
		SELECT id, user_id, savings_product_id, balance, status, created_at, updated_at
		FROM   savings_accounts
		WHERE  id = $1
		LIMIT  1
	`

	a := &model.SavingsAccount{}
	err := r.db.QueryRowContext(ctx, query, accountID).Scan(
		&a.ID, &a.UserID, &a.SavingsProductID, &a.Balance,
		&a.Status, &a.CreatedAt, &a.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrSavingsAccountNotFound
		}
		return nil, fmt.Errorf("query rekening by ID gagal: %w", err)
	}

	return a, nil
}

// Deposit menyetor dana ke rekening simpanan secara ATOMIK.
// Kenapa harus database transaction?
func (r *postgresSavingRepository) Deposit(ctx context.Context, accountID int64, amount float64, referenceID string) error {
	// Mulai database transaction dengan isolation level ReadCommitted.
	// ReadCommitted cukup untuk use-case ini: kita sudah mengunci baris target
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{
		Isolation: sql.LevelReadCommitted,
	})
	if err != nil {
		return fmt.Errorf("gagal memulai transaksi DB: %w", err)
	}

	// defer Rollback adalah safety net:
	// - Jika kita return error sebelum Commit → Rollback membatalkan semua perubahan.
	defer tx.Rollback() //nolint:errcheck — error rollback setelah commit adalah no-op

	// --- Langkah 1: Kunci baris dan baca status rekening ---
	// FOR UPDATE mengunci baris di database sampai tx selesai (commit/rollback).
	var status string
	lockQuery := `
		SELECT status
		FROM   savings_accounts
		WHERE  id = $1
		FOR UPDATE
	`
	err = tx.QueryRowContext(ctx, lockQuery, accountID).Scan(&status)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrSavingsAccountNotFound
		}
		return fmt.Errorf("lock rekening gagal: %w", err)
	}

	// Validasi status SETELAH kunci baris — bukan sebelum — agar kita membaca
	// status terbaru, bukan status yang mungkin sudah berubah oleh tx lain.
	if status != "active" {
		return ErrAccountNotActive
	}

	// --- Langkah 2: Update balance ---
	// Kita pakai `balance + $1` (bukan `$1` langsung) agar database yang
	updateQuery := `
		UPDATE savings_accounts
		SET    balance    = balance + $1,
		       updated_at = NOW()
		WHERE  id = $2
	`
	result, err := tx.ExecContext(ctx, updateQuery, amount, accountID)
	if err != nil {
		return fmt.Errorf("update saldo gagal: %w", err)
	}

	// Periksa apakah baris benar-benar terupdate — defensive check.
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return ErrSavingsAccountNotFound
	}

	// --- Langkah 3: Insert log transaksi ---
	// Ini adalah catatan permanen di buku besar. Jika langkah ini gagal,
	insertQuery := `
		INSERT INTO savings_transactions (savings_account_id, type, amount, reference_id)
		VALUES ($1, 'deposit', $2, $3)
	`
	if _, err = tx.ExecContext(ctx, insertQuery, accountID, amount, referenceID); err != nil {
		return fmt.Errorf("catat log transaksi gagal: %w", err)
	}

	// --- Langkah 4: Commit ---
	// Hanya setelah UPDATE dan INSERT keduanya berhasil, kita commit.
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit transaksi DB gagal: %w", err)
	}

	return nil
}

// GetAllTransactions mengambil semua transaksi dari tabel savings_transactions.
func (r *postgresSavingRepository) GetAllTransactions(ctx context.Context) ([]model.SavingsTransaction, error) {
	query := `
		SELECT id, savings_account_id, type, amount, reference_id, created_at
		FROM   savings_transactions
		ORDER  BY created_at DESC
	`

	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query semua transaksi simpanan gagal: %w", err)
	}
	defer rows.Close()

	var txs []model.SavingsTransaction
	for rows.Next() {
		var t model.SavingsTransaction
		if err := rows.Scan(
			&t.ID, &t.SavingsAccountID, &t.Type, &t.Amount, &t.ReferenceID, &t.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan transaksi simpanan gagal: %w", err)
		}
		txs = append(txs, t)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterasi rows transaksi simpanan gagal: %w", err)
	}

	return txs, nil
}

// InsertDepositRequest menyimpan permohonan setoran baru dengan status 'pending'.
func (r *postgresSavingRepository) InsertDepositRequest(ctx context.Context, req *model.DepositRequestModel) (*model.DepositRequestModel, error) {
	query := `
		INSERT INTO deposit_requests (user_id, savings_account_id, amount, payment_method, proof_image_url, status, reference_id)
		VALUES ($1, $2, $3, $4, $5, 'pending', $6)
		RETURNING id, status, created_at, updated_at
	`
	err := r.db.QueryRowContext(ctx, query,
		req.UserID, req.SavingsAccountID, req.Amount, req.PaymentMethod, req.ProofImageURL, req.ReferenceID,
	).Scan(&req.ID, &req.Status, &req.CreatedAt, &req.UpdatedAt)
	
	if err != nil {
		return nil, fmt.Errorf("insert deposit request gagal: %w", err)
	}
	return req, nil
}

// UpdateDepositRequestStatus mengupdate status menjadi approved atau rejected.
// Menggunakan parameter *sql.Tx agar bisa dijalankan dalam satu transaction saat approve (update balance).
func (r *postgresSavingRepository) UpdateDepositRequestStatus(ctx context.Context, tx *sql.Tx, requestID int64, status string, adminID int64) error {
	query := `
		UPDATE deposit_requests
		SET status = $1, reviewed_by = $2, reviewed_at = NOW(), updated_at = NOW()
		WHERE id = $3
	`
	var err error
	if tx != nil {
		_, err = tx.ExecContext(ctx, query, status, adminID, requestID)
	} else {
		_, err = r.db.ExecContext(ctx, query, status, adminID, requestID)
	}

	if err != nil {
		return fmt.Errorf("update status deposit request gagal: %w", err)
	}
	return nil
}

// GetDepositRequestsByUserID mengambil daftar setoran per user.
func (r *postgresSavingRepository) GetDepositRequestsByUserID(ctx context.Context, userID int64) ([]model.DepositRequestModel, error) {
	query := `
		SELECT id, user_id, savings_account_id, amount, payment_method, proof_image_url, status, reference_id, reviewed_by, reviewed_at, created_at, updated_at
		FROM deposit_requests
		WHERE user_id = $1
		ORDER BY created_at DESC
	`
	rows, err := r.db.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("query deposit requests by user gagal: %w", err)
	}
	defer rows.Close()

	var reqs []model.DepositRequestModel
	for rows.Next() {
		var req model.DepositRequestModel
		if err := rows.Scan(
			&req.ID, &req.UserID, &req.SavingsAccountID, &req.Amount, &req.PaymentMethod,
			&req.ProofImageURL, &req.Status, &req.ReferenceID, &req.ReviewedBy,
			&req.ReviewedAt, &req.CreatedAt, &req.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan deposit requests gagal: %w", err)
		}
		reqs = append(reqs, req)
	}
	return reqs, nil
}

// GetAllDepositRequests mengambil semua permohonan setoran.
func (r *postgresSavingRepository) GetAllDepositRequests(ctx context.Context) ([]model.DepositRequestModel, error) {
	query := `
		SELECT id, user_id, savings_account_id, amount, payment_method, proof_image_url, status, reference_id, reviewed_by, reviewed_at, created_at, updated_at
		FROM deposit_requests
		ORDER BY created_at DESC
	`
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query all deposit requests gagal: %w", err)
	}
	defer rows.Close()

	var reqs []model.DepositRequestModel
	for rows.Next() {
		var req model.DepositRequestModel
		if err := rows.Scan(
			&req.ID, &req.UserID, &req.SavingsAccountID, &req.Amount, &req.PaymentMethod,
			&req.ProofImageURL, &req.Status, &req.ReferenceID, &req.ReviewedBy,
			&req.ReviewedAt, &req.CreatedAt, &req.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan all deposit requests gagal: %w", err)
		}
		reqs = append(reqs, req)
	}
	return reqs, nil
}

// GetDepositRequestByID mengambil satu permohonan setoran berdasarkan ID.
func (r *postgresSavingRepository) GetDepositRequestByID(ctx context.Context, requestID int64) (*model.DepositRequestModel, error) {
	query := `
		SELECT id, user_id, savings_account_id, amount, payment_method, proof_image_url, status, reference_id, reviewed_by, reviewed_at, created_at, updated_at
		FROM deposit_requests
		WHERE id = $1
	`
	var req model.DepositRequestModel
	err := r.db.QueryRowContext(ctx, query, requestID).Scan(
		&req.ID, &req.UserID, &req.SavingsAccountID, &req.Amount, &req.PaymentMethod,
		&req.ProofImageURL, &req.Status, &req.ReferenceID, &req.ReviewedBy,
		&req.ReviewedAt, &req.CreatedAt, &req.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrDepositRequestNotFound
		}
		return nil, fmt.Errorf("query deposit request by ID gagal: %w", err)
	}
	return &req, nil
}

// BeginTx memulai transaksi.
func (r *postgresSavingRepository) BeginTx(ctx context.Context) (*sql.Tx, error) {
	return r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
}

