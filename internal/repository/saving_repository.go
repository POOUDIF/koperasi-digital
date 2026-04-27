// Package repository — implementasi SavingRepository untuk PostgreSQL.
//
// Pola yang sama dengan UserRepository:
//
//	SavingRepository (interface)  ← service layer bergantung ke sini
//	    ↑
//	postgresSavingRepository (struct)  ← implementasi konkret dengan *sql.DB
//
// Satu-satunya tempat yang boleh membuka database transaction (sql.Tx) adalah
// di package ini, bukan di service atau handler. Layer di atas cukup memanggil
// method Deposit() dan percaya bahwa balance + log transaksi diupdate secara atomik.
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
// pada pesan string dari driver PostgreSQL.
var (
	// ErrSavingsAccountNotFound dikembalikan saat rekening tidak ditemukan.
	ErrSavingsAccountNotFound = errors.New("rekening simpanan tidak ditemukan")

	// ErrSavingsProductNotFound dikembalikan saat produk simpanan tidak ditemukan.
	ErrSavingsProductNotFound = errors.New("produk simpanan tidak ditemukan")

	// ErrAccountNotActive dikembalikan saat mencoba transaksi di rekening non-aktif.
	ErrAccountNotActive = errors.New("rekening simpanan tidak aktif")
)

// SavingRepository mendefinisikan kontrak operasi database untuk modul simpanan.
type SavingRepository interface {
	// FindProductByID mengambil produk simpanan berdasarkan primary key.
	// Mengembalikan ErrSavingsProductNotFound jika tidak ada.
	FindProductByID(ctx context.Context, productID int64) (*model.SavingsProduct, error)

	// CreateAccount membuka rekening simpanan baru dengan saldo awal 0.
	CreateAccount(ctx context.Context, account *model.SavingsAccount) (*model.SavingsAccount, error)

	// GetAccountByUserID mengambil semua rekening simpanan milik satu user.
	GetAccountByUserID(ctx context.Context, userID int64) ([]model.SavingsAccount, error)

	// GetAccountByID mengambil satu rekening berdasarkan primary key.
	// Mengembalikan ErrSavingsAccountNotFound jika tidak ada.
	GetAccountByID(ctx context.Context, accountID int64) (*model.SavingsAccount, error)

	// Deposit mengeksekusi setoran dana secara ATOMIK menggunakan database transaction:
	//   1. Validasi rekening (status harus 'active') + kunci baris dengan FOR UPDATE.
	//   2. UPDATE balance di savings_accounts.
	//   3. INSERT log ke savings_transactions.
	// Jika salah satu langkah gagal, seluruh operasi di-rollback otomatis.
	Deposit(ctx context.Context, accountID int64, amount float64, referenceID string) error
}

// postgresSavingRepository adalah implementasi SavingRepository dengan PostgreSQL.
type postgresSavingRepository struct {
	db *sql.DB
}

// NewSavingRepository membuat instance repository baru.
// Mengembalikan interface (bukan struct konkret) agar caller tidak bisa
// bergantung pada detail implementasi.
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

// CreateAccount membuka rekening simpanan baru.
//
// Saldo awal selalu 0 — disematkan langsung di query agar application code
// tidak bisa "menipu" dengan mengirim saldo awal sembarangan.
// RETURNING dipakai untuk mendapatkan id, balance, status, dan timestamps
// yang di-generate database dalam satu round-trip.
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
//
// Mengembalikan slice kosong (bukan nil) jika user belum punya rekening,
// sehingga handler bisa langsung serialize ke JSON array tanpa pengecekan nil.
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
//
// Kenapa harus database transaction?
// Bayangkan server crash tepat setelah UPDATE balance berhasil, tapi sebelum
// INSERT log transaksi selesai. Tanpa tx, saldo bertambah tapi tidak ada jejak
// audit — uang "muncul dari langit" tanpa pencatatan. Dengan tx, kedua operasi
// di-rollback seolah tidak pernah terjadi, dan anggota bisa mencoba lagi.
//
// Urutan eksekusi di dalam satu tx:
//  1. SELECT ... FOR UPDATE   → kunci baris, baca status & saldo terkini.
//  2. Validasi status rekening (harus 'active').
//  3. UPDATE balance          → tambah saldo.
//  4. INSERT transaksi        → catat log mutasi.
//  5. COMMIT                  → keduanya selesai atau keduanya batal.
func (r *postgresSavingRepository) Deposit(ctx context.Context, accountID int64, amount float64, referenceID string) error {
	// Mulai database transaction dengan isolation level ReadCommitted.
	// ReadCommitted cukup untuk use-case ini: kita sudah mengunci baris target
	// dengan FOR UPDATE, sehingga concurrent deposit ke rekening yang sama
	// akan antri (bukan race condition).
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{
		Isolation: sql.LevelReadCommitted,
	})
	if err != nil {
		return fmt.Errorf("gagal memulai transaksi DB: %w", err)
	}

	// defer Rollback adalah safety net:
	// - Jika kita return error sebelum Commit → Rollback membatalkan semua perubahan.
	// - Jika Commit berhasil → Rollback menjadi no-op (tidak melakukan apa-apa).
	// Pola ini memastikan kita tidak lupa rollback di setiap return path.
	defer tx.Rollback() //nolint:errcheck — error rollback setelah commit adalah no-op

	// --- Langkah 1: Kunci baris dan baca status rekening ---
	//
	// FOR UPDATE mengunci baris di database sampai tx selesai (commit/rollback).
	// Jika ada goroutine lain yang juga ingin Deposit ke rekening yang sama,
	// ia akan MENUNGGU di sini sampai tx kita selesai — bukan membaca data lama
	// dan menghasilkan saldo yang salah.
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
	//
	// Kita pakai `balance + $1` (bukan `$1` langsung) agar database yang
	// menghitung nilai akhir berdasarkan nilai terkini yang sudah dikunci,
	// bukan nilai yang kita baca sebelumnya di application layer.
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
	//
	// Ini adalah catatan permanen di buku besar. Jika langkah ini gagal,
	// defer Rollback() di atas akan membatalkan UPDATE balance juga —
	// tidak ada uang yang "hilang" tanpa jejak.
	insertQuery := `
		INSERT INTO savings_transactions (savings_account_id, type, amount, reference_id)
		VALUES ($1, 'deposit', $2, $3)
	`
	if _, err = tx.ExecContext(ctx, insertQuery, accountID, amount, referenceID); err != nil {
		return fmt.Errorf("catat log transaksi gagal: %w", err)
	}

	// --- Langkah 4: Commit ---
	//
	// Hanya setelah UPDATE dan INSERT keduanya berhasil, kita commit.
	// Jika Commit gagal (misal: koneksi putus di detik terakhir), defer Rollback()
	// akan otomatis membatalkan kedua operasi.
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit transaksi DB gagal: %w", err)
	}

	return nil
}
