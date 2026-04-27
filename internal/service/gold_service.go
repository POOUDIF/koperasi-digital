// Package service — logika bisnis modul Jual Beli Emas Digital.
//
// Posisi dalam arsitektur:
//
//	Handler → GoldService → GoldRepository → Database
//
// GoldService tidak tahu tentang HTTP (gin.Context, status code).
// GoldRepository tidak tahu tentang aturan bisnis (validasi gram, otorisasi user).
//
// Integrasi Blockchain (tahap berikutnya):
// Saat ini BuyGold hanya mencatat transaksi off-chain (status: pending).
// Di iterasi berikutnya, sebuah background worker akan mengambil transaksi
// 'pending' dan mengirimnya ke Smart Contract di Polygon, lalu mengisi tx_hash
// dan memperbarui status menjadi 'processing' → 'success'/'failed'.
package service

import (
	"context"
	"errors"
	"fmt"
	"math"

	"koperasi-digital/internal/model"
	"koperasi-digital/internal/repository"
)

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
	// Langkah-langkah yang dijalankan (semuanya atomik via DB transaction):
	//  1. Ambil harga emas terbaru (buy_price_per_gram).
	//  2. Hitung total_rupiah = gramAmount × buy_price_per_gram (dibulatkan 4 desimal).
	//  3. Validasi & debit rekening simpanan Wadiah user sebesar total_rupiah.
	//  4. Catat transaksi emas dengan status 'pending' (tx_hash masih kosong).
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
}

// NewGoldService membuat instance service dengan dependency diinject.
// Menerima interface (bukan *postgresGoldRepository) agar mudah di-mock saat testing.
func NewGoldService(goldRepo repository.GoldRepository) GoldService {
	return &goldService{goldRepo: goldRepo}
}

// GetCurrentPrice mengambil harga emas terbaru dari repository.
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
func (s *goldService) BuyGold(ctx context.Context, userID int64, req model.BuyGoldRequest) (*model.GoldTransaction, error) {
	// --- Langkah 1: Ambil harga emas terbaru ---
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
	rawTotal := req.GramAmount * price.BuyPricePerGram
	totalRupiah := math.Round(rawTotal*10000) / 10000

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

	return goldTx, nil
}
