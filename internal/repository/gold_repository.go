// Package repository — implementasi GoldRepository untuk PostgreSQL.
//
// Posisi dalam arsitektur:
//
//	GoldService → GoldRepository → PostgreSQL (gold_prices, gold_transactions)
//	                             → PostgreSQL (savings_accounts, savings_transactions)
//
// Catatan penting — Operasi Lintas Modul:
// BuyWithDebit menyentuh tabel simpanan (savings_accounts, savings_transactions)
// DAN tabel emas (gold_transactions) dalam satu database transaction yang sama.
// Ini dilakukan secara sengaja: atomisitas lintas modul lebih penting daripada
// batas package yang ketat, terutama untuk operasi keuangan. Pola yang sama
// digunakan oleh PayInstallment di financing_repository.go.
package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"

	"koperasi-digital/internal/model"
)

// ErrGoldPriceNotAvailable dikembalikan saat tabel gold_prices kosong atau
// tidak ada data harga yang bisa diambil.
var ErrGoldPriceNotAvailable = errors.New("harga emas belum tersedia")

// GoldRepository mendefinisikan kontrak operasi database untuk modul emas.
// Service layer hanya bergantung pada interface ini — bukan implementasi konkret.
type GoldRepository interface {
	// GetCurrentPrice mengambil harga emas terbaru dari tabel gold_prices.
	// Mengembalikan ErrGoldPriceNotAvailable jika tabel kosong.
	GetCurrentPrice(ctx context.Context) (*model.GoldPrice, error)

	// BuyWithDebit mengeksekusi pembelian emas secara ATOMIK dalam satu
	// database transaction:
	//   1. SELECT ... FOR UPDATE pada savings_accounts — kunci baris, validasi
	//      kepemilikan (user_id), status 'active', dan kecukupan saldo.
	//   2. UPDATE savings_accounts — kurangi balance sebesar totalRupiah.
	//   3. INSERT savings_transactions — catat debit (type: 'withdraw').
	//   4. INSERT gold_transactions — catat pembelian emas (status: 'pending').
	//   5. COMMIT — jika semua berhasil; ROLLBACK otomatis jika ada kegagalan.
	//
	// Mengembalikan pointer ke GoldTransaction yang sudah terisi id dan created_at.
	// Error spesifik: ErrSavingsAccountNotFound, ErrAccountNotActive, ErrInsufficientBalance.
	BuyWithDebit(ctx context.Context, userID int64, accountID int64, gramAmount float64, pricePerGram float64, totalRupiah float64) (*model.GoldTransaction, error)

	// FindPending mengambil semua transaksi emas berstatus 'pending'.
	//
	// Dipanggil oleh GoldWorker setiap tick untuk menemukan transaksi yang
	// perlu dikirim ke blockchain. Hasil diurutkan berdasarkan created_at ASC
	// agar transaksi terlama diproses terlebih dahulu (FIFO).
	FindPending(ctx context.Context) ([]model.GoldTransaction, error)

	// UpdateStatusAndHash memperbarui status transaksi emas dan menyimpan tx_hash.
	//
	// Dipanggil oleh GoldWorker setelah transaksi berhasil dikirim ke blockchain:
	//   - status  : "processing" (tx dikirim, menunggu konfirmasi) atau "failed".
	//   - txHash  : hash transaksi on-chain dari Polygon (format: "0x...").
	//
	// Mengembalikan error jika transaksi dengan ID tersebut tidak ditemukan.
	UpdateStatusAndHash(ctx context.Context, id int64, status string, txHash string) error
}

// postgresGoldRepository adalah implementasi konkret dengan PostgreSQL.
type postgresGoldRepository struct {
	db *sql.DB
}

// NewGoldRepository membuat instance repository baru.
// Mengembalikan interface agar caller tidak bisa bergantung pada struct konkret.
func NewGoldRepository(db *sql.DB) GoldRepository {
	return &postgresGoldRepository{db: db}
}

// GetCurrentPrice mengambil satu baris harga emas terbaru.
//
// Harga "terbaru" ditentukan oleh kolom updated_at (DESC), bukan id.
// Ini memungkinkan admin mengoreksi harga dengan INSERT baris baru tanpa
// perlu UPDATE baris lama — jejak audit harga tetap terjaga.
func (r *postgresGoldRepository) GetCurrentPrice(ctx context.Context) (*model.GoldPrice, error) {
	query := `
		SELECT id, buy_price_per_gram, sell_price_per_gram, updated_at
		FROM   gold_prices
		ORDER  BY updated_at DESC
		LIMIT  1
	`

	p := &model.GoldPrice{}
	err := r.db.QueryRowContext(ctx, query).Scan(
		&p.ID, &p.BuyPricePerGram, &p.SellPricePerGram, &p.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrGoldPriceNotAvailable
		}
		return nil, fmt.Errorf("query harga emas gagal: %w", err)
	}

	return p, nil
}

// BuyWithDebit mengeksekusi pembelian emas secara ATOMIK.
//
// Kenapa satu transaction yang menyentuh dua modul (simpanan & emas)?
// Pembelian emas melibatkan dua operasi yang tidak boleh terpisah:
//   1. Debit saldo simpanan anggota.
//   2. Catat transaksi emas.
//
// Jika keduanya dilakukan dalam transaksi terpisah, ada jendela waktu di mana
// saldo sudah berkurang tapi catatan emas belum ada (atau sebaliknya jika urutan
// dibalik). Dengan satu DB transaction, keduanya commit bersama atau rollback bersama.
//
// Pembulatan totalRupiah ke 4 desimal dilakukan di sini (bukan di service) karena
// nilai inilah yang benar-benar tersimpan di DB dan harus konsisten dengan
// amount yang didebet dari savings_accounts.
func (r *postgresGoldRepository) BuyWithDebit(
	ctx context.Context,
	userID int64,
	accountID int64,
	gramAmount float64,
	pricePerGram float64,
	totalRupiah float64,
) (*model.GoldTransaction, error) {
	// Bulatkan totalRupiah ke 4 desimal sebelum masuk ke DB.
	// Ini memastikan nilai yang didebet dari simpanan identik dengan nilai
	// yang dicatat di gold_transactions — tidak ada selisih akibat floating-point.
	totalRupiah = math.Round(totalRupiah*10000) / 10000

	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("gagal memulai transaksi DB: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// --- Langkah 1: Kunci rekening simpanan & validasi ---
	//
	// FOR UPDATE mengunci baris agar tidak ada transaksi lain yang membaca
	// saldo lama dan menghasilkan saldo negatif (double-spend).
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
			return nil, ErrSavingsAccountNotFound
		}
		return nil, fmt.Errorf("lock rekening simpanan gagal: %w", err)
	}

	// Validasi kepemilikan: rekening harus milik user yang sedang login.
	// Return ErrSavingsAccountNotFound (bukan "forbidden") — mencegah enumerasi ID rekening.
	if accUserID != userID {
		return nil, ErrSavingsAccountNotFound
	}

	if accStatus != "active" {
		return nil, ErrAccountNotActive
	}

	if balance < totalRupiah {
		return nil, ErrInsufficientBalance
	}

	// --- Langkah 2: Debit saldo rekening simpanan ---
	updateBalanceQuery := `
		UPDATE savings_accounts
		SET    balance    = balance - $1,
		       updated_at = NOW()
		WHERE  id = $2
	`
	if _, err = tx.ExecContext(ctx, updateBalanceQuery, totalRupiah, accountID); err != nil {
		return nil, fmt.Errorf("pengurangan saldo simpanan gagal: %w", err)
	}

	// --- Langkah 3: Insert gold_transactions (RETURNING untuk dapat id) ---
	//
	// Kita insert gold_transactions sebelum savings_transactions agar kita punya
	// gold_transaction.id untuk dipakai sebagai reference_id di log simpanan.
	insertGoldQuery := `
		INSERT INTO gold_transactions
			(user_id, type, gram_amount, price_per_gram, total_rupiah, status)
		VALUES ($1, 'buy', $2, $3, $4, 'pending')
		RETURNING id, created_at
	`
	goldTx := &model.GoldTransaction{
		UserID:       userID,
		Type:         "buy",
		GramAmount:   gramAmount,
		PricePerGram: pricePerGram,
		TotalRupiah:  totalRupiah,
		Status:       "pending",
	}

	err = tx.QueryRowContext(ctx, insertGoldQuery,
		userID, gramAmount, pricePerGram, totalRupiah,
	).Scan(&goldTx.ID, &goldTx.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("insert transaksi emas gagal: %w", err)
	}

	// --- Langkah 4: Catat log debit di buku besar simpanan ---
	//
	// reference_id = "gold_buy_{goldTxID}" menghubungkan log simpanan
	// dengan transaksi emas yang memicunya — penting untuk audit trail.
	referenceID := fmt.Sprintf("gold_buy_%d", goldTx.ID)
	insertLogQuery := `
		INSERT INTO savings_transactions (savings_account_id, type, amount, reference_id)
		VALUES ($1, 'withdraw', $2, $3)
	`
	if _, err = tx.ExecContext(ctx, insertLogQuery, accountID, totalRupiah, referenceID); err != nil {
		return nil, fmt.Errorf("catat log debit simpanan gagal: %w", err)
	}

	// --- Langkah 5: Commit — semua langkah berhasil ---
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit transaksi pembelian emas gagal: %w", err)
	}

	return goldTx, nil
}

// FindPending mengambil semua transaksi emas yang masih berstatus 'pending'.
//
// Worker blockchain memanggil method ini setiap interval (default 5 detik)
// untuk menemukan transaksi yang perlu dikirim ke Smart Contract di Polygon.
//
// Urutan created_at ASC memastikan transaksi terlama diproses duluan (FIFO),
// mencegah starvation jika ada lonjakan transaksi baru.
func (r *postgresGoldRepository) FindPending(ctx context.Context) ([]model.GoldTransaction, error) {
	query := `
		SELECT id, user_id, type, gram_amount, price_per_gram,
		       total_rupiah, tx_hash, status, created_at
		FROM   gold_transactions
		WHERE  status = 'pending'
		ORDER  BY created_at ASC
	`

	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query transaksi pending gagal: %w", err)
	}
	defer rows.Close()

	var txs []model.GoldTransaction
	for rows.Next() {
		var t model.GoldTransaction
		if err := rows.Scan(
			&t.ID, &t.UserID, &t.Type, &t.GramAmount, &t.PricePerGram,
			&t.TotalRupiah, &t.TxHash, &t.Status, &t.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan transaksi pending gagal: %w", err)
		}
		txs = append(txs, t)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterasi rows transaksi pending gagal: %w", err)
	}

	return txs, nil
}

// UpdateStatusAndHash memperbarui status dan tx_hash transaksi emas.
//
// Query ini sengaja tidak memakai WHERE status = 'pending' sebagai guard
// karena worker sudah membaca status pending sebelum memanggil fungsi ini.
// Jika dua worker berjalan paralel (scale-out), perlu ditambahkan
// SELECT ... FOR UPDATE SKIP LOCKED di FindPending untuk menghindari
// race condition.
func (r *postgresGoldRepository) UpdateStatusAndHash(ctx context.Context, id int64, status string, txHash string) error {
	query := `
		UPDATE gold_transactions
		SET    status = $1, tx_hash = $2
		WHERE  id = $3
	`

	result, err := r.db.ExecContext(ctx, query, status, txHash, id)
	if err != nil {
		return fmt.Errorf("update status transaksi emas ID=%d gagal: %w", id, err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("cek rows affected gagal: %w", err)
	}

	if rows == 0 {
		return fmt.Errorf("transaksi emas ID=%d tidak ditemukan", id)
	}

	return nil
}
