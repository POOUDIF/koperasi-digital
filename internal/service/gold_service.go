// Package service — logika bisnis modul Jual Beli Emas Digital.
//
// Posisi dalam arsitektur:
//
//	Handler → GoldService → GoldRepository → Redis (cache)
//	                                       → Database
//	        → GoldService → Redis (message queue: queue:gold_mint)
//	                ↓
//	           GoldWorker (consume via BLPop)
//
// GoldService tidak tahu tentang HTTP (gin.Context, status code).
// GoldRepository tidak tahu tentang aturan bisnis (validasi gram, otorisasi user).
//
// Arsitektur Event-Driven:
// Setelah BuyGold berhasil commit ke PostgreSQL (status: 'pending'),
// GoldService melakukan RPush ID transaksi ke "queue:gold_mint" di Redis.
// GoldWorker yang sedang BLPop akan langsung terbangun dan memproses transaksi
// tanpa perlu menunggu tick berikutnya (tidak ada polling lagi).
package service

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/redis/go-redis/v9"

	"koperasi-digital/internal/model"
	"koperasi-digital/internal/repository"
	"koperasi-digital/internal/util"
)

// goldMintQueueKey adalah Redis key untuk antrian ID transaksi emas yang
// menunggu diproses (di-mint) oleh GoldWorker.
// Worker melakukan BLPop pada key ini; Service melakukan RPush setelah buy.
const goldMintQueueKey = "queue:gold_mint"

// ErrGoldPriceNotAvailable dikembalikan saat tidak ada data harga emas di sistem.
// Kemungkinan sebab: migration belum dijalankan atau admin belum mengisi harga.
var ErrGoldPriceNotAvailable = errors.New("harga emas belum tersedia, hubungi admin koperasi")

// GoldService mendefinisikan kontrak logika bisnis untuk modul emas.
// Handler layer hanya bergantung pada interface ini.
type GoldService interface {
	// GetCurrentPrice mengambil harga emas terbaru yang ditetapkan koperasi.
	GetCurrentPrice(ctx context.Context) (*model.GoldPrice, error)

	// BuyGold memproses pembelian emas oleh anggota.
	//
	// Langkah-langkah yang dijalankan:
	//  1. Ambil harga emas terbaru (dari Redis cache atau PostgreSQL).
	//  2. Hitung total_rupiah = gramAmount × buy_price_per_gram (dibulatkan 4 desimal).
	//  3. Validasi & debit rekening simpanan Wadiah user sebesar total_rupiah (atomik di DB).
	//  4. Catat transaksi emas dengan status 'pending'.
	//  5. Push ID transaksi ke Redis queue "queue:gold_mint" → worker langsung memproses.
	//
	// Error yang mungkin dikembalikan:
	//   ErrGoldPriceNotAvailable — belum ada data harga.
	//   ErrSavingsAccountNotFound — rekening tidak ada atau bukan milik user.
	//   ErrAccountNotActive       — rekening dibekukan/ditutup.
	//   ErrInsufficientBalance    — saldo tidak cukup untuk membeli sejumlah gram itu.
	BuyGold(ctx context.Context, userID int64, req model.BuyGoldRequest) (*model.GoldTransaction, error)
}

// goldService adalah implementasi konkret GoldService.
type goldService struct {
	goldRepo repository.GoldRepository
	rdb      *redis.Client // untuk push ke message queue setelah buy berhasil
}

// NewGoldService membuat instance service dengan dependency diinject.
//
// Parameter:
//   - goldRepo : repository untuk operasi database emas (wajib).
//   - rdb      : Redis client untuk message queue (boleh nil — RPush dilewati).
//
// Menerima interface (bukan *postgresGoldRepository) agar mudah di-mock saat testing.
func NewGoldService(goldRepo repository.GoldRepository, rdb *redis.Client) GoldService {
	return &goldService{goldRepo: goldRepo, rdb: rdb}
}

// GetCurrentPrice mengambil harga emas terbaru dari repository.
// Repository sudah menangani cache-aside ke Redis secara transparan.
func (s *goldService) GetCurrentPrice(ctx context.Context) (*model.GoldPrice, error) {
	price, err := s.goldRepo.GetCurrentPrice(ctx)
	if err != nil {
		if errors.Is(err, repository.ErrGoldPriceNotAvailable) {
			return nil, ErrGoldPriceNotAvailable
		}
		return nil, fmt.Errorf("mengambil harga emas gagal: %w", err)
	}
	return price, nil
}

// BuyGold memproses pembelian emas oleh anggota.
//
// Perhitungan total_rupiah dibulatkan ke 4 desimal menggunakan math.Round agar:
//   - Konsisten dengan kolom DECIMAL(19,4) di database.
//   - Tidak ada selisih antara nilai yang didebet dan yang dicatat di gold_transactions.
//
// Seluruh operasi database (validasi saldo, debit, log, insert gold_tx) diserahkan
// ke repository layer yang menjalankannya dalam satu DB transaction atomik.
//
// Setelah DB transaction berhasil commit, ID transaksi di-push ke Redis queue
// sehingga GoldWorker langsung terbangun dan memproses tanpa menunggu polling.
// Jika RPush gagal (Redis down), hanya di-log — DB transaction TIDAK di-rollback.
// Transaksi tetap 'pending' di DB dan bisa diproses oleh mekanisme recovery.
func (s *goldService) BuyGold(ctx context.Context, userID int64, req model.BuyGoldRequest) (*model.GoldTransaction, error) {
	// --- Langkah 1: Ambil harga emas terbaru ---
	// Cache-aside dilakukan di repository layer secara transparan.
	price, err := s.goldRepo.GetCurrentPrice(ctx)
	if err != nil {
		if errors.Is(err, repository.ErrGoldPriceNotAvailable) {
			return nil, ErrGoldPriceNotAvailable
		}
		return nil, fmt.Errorf("mengambil harga emas gagal: %w", err)
	}

	// --- Langkah 2: Hitung total yang harus dibayar ---
	//
	// Untuk transaksi beli, kita pakai buy_price_per_gram (harga yang lebih tinggi,
	// sudah termasuk spread koperasi).
	//
	// Pembulatan ke 4 desimal di sini agar angka yang kita kirim ke repository
	// identik dengan yang akan tersimpan di DB (DECIMAL 19,4).
	// Menggunakan helper terpadu agar tidak akumulasi floating point error.
	rawTotal := req.GramAmount * price.BuyPricePerGram
	totalRupiah := util.RoundTo4Decimals(rawTotal)

	// --- Langkah 3 & 4: Validasi saldo + debit + insert gold_tx (satu DB transaction) ---
	//
	// Repository menangani validasi kepemilikan rekening, status aktif, kecukupan saldo,
	// debit saldo, log transaksi simpanan, dan insert gold_transactions — semuanya atomik.
	goldTx, err := s.goldRepo.BuyWithDebit(ctx, userID, req.SavingsAccountID, req.GramAmount, price.BuyPricePerGram, totalRupiah)
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrSavingsAccountNotFound):
			return nil, ErrSavingsAccountNotFound
		case errors.Is(err, repository.ErrAccountNotActive):
			return nil, ErrAccountNotActive
		case errors.Is(err, repository.ErrInsufficientBalance):
			return nil, ErrInsufficientBalance
		default:
			return nil, fmt.Errorf("pembelian emas gagal: %w", err)
		}
	}

	// --- Langkah 5: Push ID transaksi ke Redis queue (event-driven trigger) ---
	//
	// RPush menambahkan ID ke ujung kanan antrian "queue:gold_mint".
	// GoldWorker yang sedang BLPop akan langsung terbangun dan memproses transaksi ini.
	//
	// Kenapa RPush dilakukan SETELAH commit, bukan di dalam DB transaction?
	// - RPush di dalam DB tx tidak aman: jika tx di-rollback, pesan sudah terkirim ke Redis.
	// - Di luar tx: jika RPush gagal setelah commit, transaksi tetap 'pending' di DB
	//   dan bisa di-recover oleh mekanisme lain (admin trigger, restart worker, dll).
	if s.rdb != nil {
		if pushErr := s.rdb.RPush(ctx, goldMintQueueKey, goldTx.ID).Err(); pushErr != nil {
			// Non-fatal: log peringatan tapi tetap return goldTx ke handler.
			// Transaksi sudah tersimpan di DB — tidak ada data yang hilang.
			log.Printf("[gold-service] PERINGATAN: RPush ke Redis gagal untuk ID=%d: %v", goldTx.ID, pushErr)
		} else {
			log.Printf("[gold-service] transaksi ID=%d berhasil dipush ke queue '%s'", goldTx.ID, goldMintQueueKey)
		}
	}

	return goldTx, nil
}
