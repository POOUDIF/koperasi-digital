// Package repository — implementasi GoldRepository untuk PostgreSQL + Redis cache.
//
// Posisi dalam arsitektur:
//
//	GoldService → GoldRepository → Redis  (cache layer)
//	                             → PostgreSQL (gold_prices, gold_transactions)
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
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"time"

	"github.com/redis/go-redis/v9"

	"koperasi-digital/internal/model"
)

// goldPriceCacheKey adalah Redis key untuk menyimpan harga emas terbaru.
const goldPriceCacheKey = "gold:current_price"

// goldPriceCacheTTL adalah masa kedaluwarsa cache harga emas.
// 15 menit: cukup pendek agar perubahan harga terlihat tepat waktu,
// cukup panjang untuk mengurangi beban query PostgreSQL secara signifikan.
const goldPriceCacheTTL = 15 * time.Minute

// ErrGoldPriceNotAvailable dikembalikan saat tabel gold_prices kosong atau
// tidak ada data harga yang bisa diambil.
var ErrGoldPriceNotAvailable = errors.New("harga emas belum tersedia")

// GoldRepository mendefinisikan kontrak operasi database untuk modul emas.
// Service layer hanya bergantung pada interface ini — bukan implementasi konkret.
type GoldRepository interface {
	// GetCurrentPrice mengambil harga emas terbaru.
	// Menggunakan Redis sebagai cache layer (TTL 15 menit).
	// Cache miss → query PostgreSQL → simpan ke Redis → return.
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

	// FindByID mengambil satu transaksi emas berdasarkan ID-nya.
	//
	// Dipanggil oleh GoldWorker setelah menerima ID dari message queue Redis.
	// Mengembalikan error jika transaksi tidak ditemukan.
	FindByID(ctx context.Context, id int64) (*model.GoldTransaction, error)

	// FindPending mengambil semua transaksi emas berstatus 'pending'.
	//
	// Dipertahankan untuk kompatibilitas dan keperluan recovery/admin.
	// Pada arsitektur event-driven, worker menggunakan BLPop + FindByID,
	// namun FindPending berguna sebagai fallback jika ada transaksi yang
	// tidak masuk ke queue (misalnya restart saat crash).
	FindPending(ctx context.Context) ([]model.GoldTransaction, error)

	// FindProcessing mengambil semua transaksi emas berstatus 'processing'
	// yang memiliki tx_hash (sudah dikirim ke blockchain).
	//
	// Digunakan oleh Recover() saat startup — jika server crash saat goroutine
	// awaitReceipt sedang menunggu konfirmasi blok, transaksi ini perlu
	// di-resume agar bisa di-update ke 'success' atau di-refund jika revert.
	FindProcessing(ctx context.Context) ([]model.GoldTransaction, error)

	// UpdateStatusAndHash memperbarui status transaksi emas dan menyimpan tx_hash.
	//
	// Dipanggil oleh GoldWorker setelah transaksi berhasil dikirim ke blockchain:
	//   - status  : "processing" (tx dikirim, menunggu konfirmasi) atau "failed".
	//   - txHash  : hash transaksi on-chain dari Polygon (format: "0x...").
	//
	// Mengembalikan error jika transaksi dengan ID tersebut tidak ditemukan.
	UpdateStatusAndHash(ctx context.Context, id int64, status string, txHash string) error

	// UpdateTransactionStatus memperbarui hanya kolom status (tanpa mengubah tx_hash).
	//
	// Dipanggil oleh GoldWorker setelah menerima receipt dari blockchain:
	//   - "success" : transaksi dikonfirmasi on-chain (receipt.Status == 1).
	//
	// Untuk kasus "failed" dengan refund, gunakan RefundFailedTransaction.
	UpdateTransactionStatus(ctx context.Context, id int64, status string) error

	// RefundFailedTransaction menjalankan proses refund saldo secara ATOMIK
	// dalam satu DB transaction ketika transaksi on-chain di-revert oleh EVM.
	//
	// Langkah-langkah (atomik):
	//  1. Ambil total_rupiah dari gold_transactions.
	//  2. Lookup savings_account_id dari savings_transactions via reference_id.
	//  3. UPDATE savings_accounts SET balance += total_rupiah.
	//  4. INSERT savings_transactions (type:'deposit', reference:'gold_refund_{id}').
	//  5. UPDATE gold_transactions SET status='failed'.
	//
	// Self-contained: tidak menerima account_id / total_rupiah sebagai parameter
	// untuk menghindari risiko bug jika caller melempar nilai yang tidak konsisten.
	RefundFailedTransaction(ctx context.Context, goldTxID int64) error
}

// postgresGoldRepository adalah implementasi konkret dengan PostgreSQL + Redis.
type postgresGoldRepository struct {
	db  *sql.DB
	rdb *redis.Client // nil-safe: semua operasi Redis di-guard dengan nil check
}

// NewGoldRepository membuat instance repository baru.
//
// Parameter:
//   - db  : koneksi PostgreSQL (wajib).
//   - rdb : Redis client untuk caching (opsional — boleh nil, caching dinonaktifkan).
//
// Mengembalikan interface agar caller tidak bisa bergantung pada struct konkret.
func NewGoldRepository(db *sql.DB, rdb *redis.Client) GoldRepository {
	return &postgresGoldRepository{db: db, rdb: rdb}
}

// GetCurrentPrice mengambil harga emas terbaru menggunakan pola Cache-Aside.
//
// Alur:
//  1. Cek Redis dengan key "gold:current_price".
//     → Cache Hit  : unmarshal JSON → return (tidak menyentuh PostgreSQL).
//     → Cache Miss : lanjut ke langkah 2.
//  2. Query PostgreSQL untuk harga terbaru.
//  3. Marshal hasil ke JSON → simpan ke Redis dengan SetEx (TTL 15 menit).
//  4. Return hasil dari PostgreSQL.
//
// Penting: error Redis (jaringan, timeout) TIDAK menghentikan alur utama.
// Sistem tetap berjalan (fall-through ke PostgreSQL) meski Redis down.
func (r *postgresGoldRepository) GetCurrentPrice(ctx context.Context) (*model.GoldPrice, error) {
	// --- Langkah 1: Cek Redis cache ---
	if r.rdb != nil {
		cached, err := r.rdb.Get(ctx, goldPriceCacheKey).Result()
		if err == nil {
			// Cache Hit: unmarshal JSON ke model.GoldPrice
			var p model.GoldPrice
			jsonErr := json.Unmarshal([]byte(cached), &p)
			if jsonErr == nil {
				log.Println("[gold-repo] cache hit — harga emas dari Redis")
				return &p, nil
			}
			// Data rusak di cache — log dan lanjut ke PostgreSQL
			log.Printf("[gold-repo] cache data korupsi, query PostgreSQL: %v", jsonErr)
		} else if !errors.Is(err, redis.Nil) {
			// Error Redis yang bukan "key tidak ada" — log dan lanjut
			log.Printf("[gold-repo] Redis Get error (non-fatal): %v", err)
		} else {
			log.Println("[gold-repo] cache miss — query PostgreSQL")
		}
	}

	// --- Langkah 2: Query PostgreSQL ---
	//
	// Harga "terbaru" ditentukan oleh kolom updated_at (DESC), bukan id.
	// Ini memungkinkan admin mengoreksi harga dengan INSERT baris baru tanpa
	// perlu UPDATE baris lama — jejak audit harga tetap terjaga.
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

	// --- Langkah 3: Simpan ke Redis cache ---
	if r.rdb != nil {
		data, jsonErr := json.Marshal(p)
		if jsonErr == nil {
			if setErr := r.rdb.SetEx(ctx, goldPriceCacheKey, data, goldPriceCacheTTL).Err(); setErr != nil {
				// Gagal simpan cache = non-fatal, data tetap dikembalikan ke caller
				log.Printf("[gold-repo] Redis SetEx error (non-fatal): %v", setErr)
			}
		}
	}

	return p, nil
}

// BuyWithDebit mengeksekusi pembelian emas secara ATOMIK.
//
// Kenapa satu transaction yang menyentuh dua modul (simpanan & emas)?
// Pembelian emas melibatkan dua operasi yang tidak boleh terpisah:
//  1. Debit saldo simpanan anggota.
//  2. Catat transaksi emas.
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

// FindByID mengambil satu transaksi emas berdasarkan primary key-nya.
//
// Dipanggil oleh GoldWorker setelah menerima ID dari queue Redis (BLPop).
// Worker menerima ID string dari queue → parse → panggil FindByID → proses.
func (r *postgresGoldRepository) FindByID(ctx context.Context, id int64) (*model.GoldTransaction, error) {
	query := `
		SELECT id, user_id, type, gram_amount, price_per_gram,
		       total_rupiah, tx_hash, status, created_at
		FROM   gold_transactions
		WHERE  id = $1
	`

	var t model.GoldTransaction
	err := r.db.QueryRowContext(ctx, query, id).Scan(
		&t.ID, &t.UserID, &t.Type, &t.GramAmount, &t.PricePerGram,
		&t.TotalRupiah, &t.TxHash, &t.Status, &t.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("transaksi emas ID=%d tidak ditemukan", id)
		}
		return nil, fmt.Errorf("query transaksi emas ID=%d gagal: %w", id, err)
	}

	return &t, nil
}

// FindPending mengambil semua transaksi emas yang masih berstatus 'pending'.
//
// Dipertahankan untuk keperluan recovery dan debugging admin.
// Pada arsitektur event-driven normal, worker menggunakan BLPop + FindByID,
// namun FindPending berguna jika ada transaksi yang lolos dari queue
// (misalnya server crash sebelum RPush berhasil).
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

// FindProcessing mengambil semua transaksi emas berstatus 'processing'
// yang memiliki tx_hash (sudah dikirim ke blockchain tapi belum dikonfirmasi).
//
// Digunakan oleh GoldWorker.Recover() saat startup untuk meresume goroutine
// awaitReceipt yang terbunuh akibat crash atau graceful shutdown di tengah jalan.
//
// Kondisi tx_hash IS NOT NULL memastikan kita hanya mengambil transaksi yang
// benar-benar sudah on-chain — bukan yang gagal sebelum broadcast.
func (r *postgresGoldRepository) FindProcessing(ctx context.Context) ([]model.GoldTransaction, error) {
	query := `
		SELECT id, user_id, type, gram_amount, price_per_gram,
		       total_rupiah, tx_hash, status, created_at
		FROM   gold_transactions
		WHERE  status = 'processing'
		  AND  tx_hash IS NOT NULL
		ORDER  BY created_at ASC
	`

	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query transaksi processing gagal: %w", err)
	}
	defer rows.Close()

	var txs []model.GoldTransaction
	for rows.Next() {
		var t model.GoldTransaction
		if err := rows.Scan(
			&t.ID, &t.UserID, &t.Type, &t.GramAmount, &t.PricePerGram,
			&t.TotalRupiah, &t.TxHash, &t.Status, &t.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan transaksi processing gagal: %w", err)
		}
		txs = append(txs, t)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterasi rows transaksi processing gagal: %w", err)
	}

	return txs, nil
}

// UpdateStatusAndHash memperbarui status dan tx_hash transaksi emas.
//
// Dipanggil saat transaksi pertama kali dikirim ke blockchain:
//   - status = "processing", txHash = hash transaksi on-chain.
//   - status = "failed", txHash = "" (gagal sebelum sampai ke chain).
//
// Untuk update status setelah receipt, gunakan UpdateTransactionStatus atau
// RefundFailedTransaction.
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

// UpdateTransactionStatus memperbarui hanya kolom status transaksi emas.
//
// Digunakan setelah receipt diterima dari blockchain:
//   - status = "success" : transaksi dikonfirmasi on-chain (receipt.Status == 1).
//
// Untuk kasus "failed" yang memerlukan refund saldo, gunakan RefundFailedTransaction.
func (r *postgresGoldRepository) UpdateTransactionStatus(ctx context.Context, id int64, status string) error {
	query := `
		UPDATE gold_transactions
		SET    status = $1
		WHERE  id = $2
	`

	result, err := r.db.ExecContext(ctx, query, status, id)
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

// RefundFailedTransaction menjalankan refund saldo secara ATOMIK ketika
// transaksi on-chain di-revert oleh EVM (receipt.Status == 0).
//
// Alur (semua dalam satu DB transaction):
//  1. Ambil total_rupiah dan user_id dari gold_transactions.
//  2. Lookup savings_account_id dari savings_transactions menggunakan
//     reference_id = "gold_buy_{goldTxID}" — menghindari kebutuhan
//     menyimpan savings_account_id di gold_transactions.
//  3. UPDATE savings_accounts SET balance += total_rupiah (kredit kembali).
//  4. INSERT savings_transactions type='deposit' sebagai audit trail refund.
//  5. UPDATE gold_transactions SET status='failed'.
//
// Fungsi ini idempotent-safe: jika dipanggil dua kali, INSERT savings_transactions
// kedua akan gagal karena reference_id unik — mencegah double-refund.
func (r *postgresGoldRepository) RefundFailedTransaction(ctx context.Context, goldTxID int64) error {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("gagal memulai DB transaction untuk refund ID=%d: %w", goldTxID, err)
	}
	defer tx.Rollback() //nolint:errcheck

	// --- Langkah 1: Ambil detail transaksi emas ---
	var totalRupiah float64
	var userID int64
	var currentStatus string

	goldQuery := `
		SELECT user_id, total_rupiah, status
		FROM   gold_transactions
		WHERE  id = $1
		FOR UPDATE
	`
	err = tx.QueryRowContext(ctx, goldQuery, goldTxID).Scan(&userID, &totalRupiah, &currentStatus)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("transaksi emas ID=%d tidak ditemukan untuk refund", goldTxID)
		}
		return fmt.Errorf("query transaksi emas ID=%d gagal: %w", goldTxID, err)
	}

	// Guard: hanya proses refund jika status 'pending' atau 'processing'.
	//   - 'pending'    : transaksi belum sempat dikirim ke chain (misal: wallet belum diset).
	//   - 'processing' : transaksi sudah dikirim ke chain tapi EVM me-revert (receipt.Status=0).
	// Status lain ('success', 'failed') berarti sudah final — skip untuk mencegah double-refund.
	if currentStatus != "processing" && currentStatus != "pending" {
		log.Printf("[gold-repo] RefundFailedTransaction: transaksi ID=%d status='%s' (sudah final), skip refund",
			goldTxID, currentStatus)
		return nil
	}

	// --- Langkah 2: Lookup savings_account_id via reference_id ---
	// reference_id format: "gold_buy_{goldTxID}" — ditetapkan saat BuyWithDebit.
	referenceID := fmt.Sprintf("gold_buy_%d", goldTxID)
	var savingsAccountID int64

	lookupQuery := `
		SELECT savings_account_id
		FROM   savings_transactions
		WHERE  reference_id = $1
		LIMIT  1
	`
	err = tx.QueryRowContext(ctx, lookupQuery, referenceID).Scan(&savingsAccountID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("log simpanan untuk gold_tx ID=%d tidak ditemukan (reference: %s)",
				goldTxID, referenceID)
		}
		return fmt.Errorf("lookup savings_account_id untuk refund ID=%d gagal: %w", goldTxID, err)
	}

	// --- Langkah 3: Kredit kembali saldo simpanan ---
	// SELECT ... FOR UPDATE pada savings_accounts untuk mencegah race condition
	// jika ada transaksi lain yang mengubah saldo secara bersamaan.
	refundBalanceQuery := `
		UPDATE savings_accounts
		SET    balance    = balance + $1,
		       updated_at = NOW()
		WHERE  id = $2
	`
	if _, err = tx.ExecContext(ctx, refundBalanceQuery, totalRupiah, savingsAccountID); err != nil {
		return fmt.Errorf("refund saldo simpanan ID=%d gagal: %w", savingsAccountID, err)
	}

	// --- Langkah 4: Catat log refund di buku besar simpanan ---
	// reference_id format: "gold_refund_{goldTxID}" — berbeda dari "gold_buy_{id}"
	// agar bisa dibedakan di riwayat transaksi simpanan.
	//
	// UNIQUE constraint pada savings_transactions.reference_id memberikan
	// proteksi built-in terhadap double-refund.
	refundReferenceID := fmt.Sprintf("gold_refund_%d", goldTxID)
	insertLogQuery := `
		INSERT INTO savings_transactions (savings_account_id, type, amount, reference_id)
		VALUES ($1, 'deposit', $2, $3)
	`
	if _, err = tx.ExecContext(ctx, insertLogQuery, savingsAccountID, totalRupiah, refundReferenceID); err != nil {
		return fmt.Errorf("catat log refund simpanan ID=%d gagal: %w", goldTxID, err)
	}

	// --- Langkah 5: Update status gold_transactions → 'failed' ---
	failQuery := `
		UPDATE gold_transactions
		SET    status = 'failed'
		WHERE  id = $1
	`
	if _, err = tx.ExecContext(ctx, failQuery, goldTxID); err != nil {
		return fmt.Errorf("update status failed transaksi ID=%d gagal: %w", goldTxID, err)
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit refund transaksi ID=%d gagal: %w", goldTxID, err)
	}

	log.Printf("[gold-repo] ✓ refund berhasil — gold_tx ID=%d | akun=%d | +Rp%.2f",
		goldTxID, savingsAccountID, totalRupiah)

	return nil
}
