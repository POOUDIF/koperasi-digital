// Package service — logika bisnis modul Jual Beli Emas Digital.
// Posisi dalam arsitektur:
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
const goldMintQueueKey = "queue:gold_mint"

// ErrGoldPriceNotAvailable dikembalikan saat tidak ada data harga emas di sistem.
// Kemungkinan sebab: migration belum dijalankan atau admin belum mengisi harga.
var ErrGoldPriceNotAvailable = errors.New("harga emas belum tersedia, hubungi admin koperasi")

// ErrExceedsTransactionLimit dikembalikan saat percobaan pembelian atau penjualan melebihi batas (GAP-09).
var ErrExceedsTransactionLimit = errors.New("maksimal transaksi emas adalah 100 gram per transaksi")

// GoldService mendefinisikan kontrak logika bisnis untuk modul emas.
// Handler layer hanya bergantung pada interface ini.
type GoldService interface {
	// GetCurrentPrice mengambil harga emas terbaru yang ditetapkan koperasi.
	GetCurrentPrice(ctx context.Context) (*model.GoldPrice, error)

	// BuyGold memproses pembelian emas oleh anggota.
	// Langkah-langkah yang dijalankan:
	BuyGold(ctx context.Context, userID int64, req model.BuyGoldRequest) (*model.GoldTransaction, error)

	// SellGold memproses penjualan emas oleh anggota.
	SellGold(ctx context.Context, userID int64, req model.SellGoldRequest) (*model.GoldTransaction, error)

	// GetAllTransactions mengambil semua transaksi emas dari semua user.
	GetAllTransactions(ctx context.Context) ([]model.GoldTransaction, error)
}

// goldService adalah implementasi konkret GoldService.
type goldService struct {
	goldRepo repository.GoldRepository
	rdb      *redis.Client // untuk push ke message queue setelah buy berhasil
}

// NewGoldService membuat instance service dengan dependency diinject.
// Parameter:
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
// Perhitungan total_rupiah dibulatkan ke 4 desimal menggunakan math.Round agar:
func (s *goldService) BuyGold(ctx context.Context, userID int64, req model.BuyGoldRequest) (*model.GoldTransaction, error) {
	// GAP-09: Maksimal transaksi 100 Gram.
	if req.GramAmount > 100 {
		return nil, ErrExceedsTransactionLimit
	}

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
	// Untuk transaksi beli, kita pakai buy_price_per_gram (harga yang lebih tinggi,
	rawTotal := req.GramAmount * price.BuyPricePerGram
	totalRupiah := util.RoundTo4Decimals(rawTotal)

	// --- Langkah 3 & 4: Validasi saldo + debit + insert gold_tx (satu DB transaction) ---
	// Repository menangani validasi kepemilikan rekening, status aktif, kecukupan saldo,
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
	// RPush menambahkan ID ke ujung kanan antrian "queue:gold_mint".
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

// SellGold mengeksekusi layanan penjualan emas dari saldo pengguna ke koperasi.
func (s *goldService) SellGold(ctx context.Context, userID int64, req model.SellGoldRequest) (*model.GoldTransaction, error) {
	if req.GramAmount > 100 {
		return nil, ErrExceedsTransactionLimit
	}

	price, err := s.goldRepo.GetCurrentPrice(ctx)
	if err != nil {
		if errors.Is(err, repository.ErrGoldPriceNotAvailable) {
			return nil, ErrGoldPriceNotAvailable
		}
		return nil, fmt.Errorf("mengambil harga emas gagal: %w", err)
	}

	rawTotal := req.GramAmount * price.SellPricePerGram
	totalRupiah := util.RoundTo4Decimals(rawTotal)

	goldTx, err := s.goldRepo.SellWithCredit(ctx, userID, req.SavingsAccountID, req.GramAmount, price.SellPricePerGram, totalRupiah)
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrSavingsAccountNotFound):
			return nil, ErrSavingsAccountNotFound
		case errors.Is(err, repository.ErrAccountNotActive):
			return nil, ErrAccountNotActive
		default:
			return nil, fmt.Errorf("penjualan emas gagal: %w", err)
		}
	}

	return goldTx, nil
}

// GetAllTransactions mengambil semua transaksi emas dari semua user.
func (s *goldService) GetAllTransactions(ctx context.Context) ([]model.GoldTransaction, error) {
	txs, err := s.goldRepo.GetAllTransactions(ctx)
	if err != nil {
		return nil, fmt.Errorf("mengambil daftar semua transaksi emas gagal: %w", err)
	}
	return txs, nil
}
