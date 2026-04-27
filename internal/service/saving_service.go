// Package service — logika bisnis modul simpanan syariah.
//
// Layer ini berada di antara handler dan repository:
//
//	Handler → SavingService → SavingRepository → Database
//
// SavingService tidak tahu tentang HTTP (gin.Context, status code).
// SavingRepository tidak tahu tentang aturan bisnis (min_deposit, authorization).
// Pembagian tanggung jawab ini membuat setiap layer mudah ditest secara terpisah.
package service

import (
	"context"
	"errors"
	"fmt"

	"koperasi-digital/internal/model"
	"koperasi-digital/internal/repository"
)

// Sentinel errors modul simpanan — handler memeriksa ini untuk menentukan HTTP status.
var (
	// ErrSavingsAccountNotFound dikembalikan saat rekening tidak ditemukan
	// atau saat user mencoba mengakses rekening milik orang lain (authorization).
	// Sengaja sama dengan "not found" untuk mencegah enumerasi rekening.
	ErrSavingsAccountNotFound = errors.New("rekening simpanan tidak ditemukan")

	// ErrSavingsProductNotFound dikembalikan saat ID produk tidak valid.
	ErrSavingsProductNotFound = errors.New("produk simpanan tidak ditemukan")

	// ErrAccountNotActive dikembalikan saat rekening berstatus frozen/closed.
	ErrAccountNotActive = errors.New("rekening simpanan tidak aktif")

	// ErrDepositBelowMinimum dikembalikan saat nominal setoran di bawah min_deposit produk.
	// Pesan error menyertakan nilai minimum agar client bisa menampilkannya ke user.
	ErrDepositBelowMinimum = errors.New("jumlah setoran di bawah minimum produk")
)

// SavingService mendefinisikan kontrak logika bisnis untuk modul simpanan.
// Handler layer hanya bergantung pada interface ini, bukan implementasi konkret.
type SavingService interface {
	// OpenAccount membuka rekening simpanan baru untuk anggota.
	// Memvalidasi bahwa produk simpanan yang dipilih ada di database.
	OpenAccount(ctx context.Context, userID int64, req model.OpenAccountRequest) (*model.SavingsAccount, error)

	// GetAccounts mengambil semua rekening simpanan milik user.
	GetAccounts(ctx context.Context, userID int64) ([]model.SavingsAccount, error)

	// DepositFund menyetor dana ke rekening simpanan.
	// Memvalidasi kepemilikan rekening, status rekening, dan nominal minimum.
	DepositFund(ctx context.Context, userID int64, req model.DepositRequest) error
}

// savingService adalah implementasi konkret SavingService.
type savingService struct {
	savingRepo repository.SavingRepository
}

// NewSavingService membuat instance service baru dengan dependency diinject.
// Menerima interface (bukan *postgresSavingRepository) agar mudah di-mock saat testing.
func NewSavingService(savingRepo repository.SavingRepository) SavingService {
	return &savingService{savingRepo: savingRepo}
}

// OpenAccount membuka rekening simpanan baru untuk anggota.
//
// Aturan bisnis:
//  1. Produk simpanan yang dipilih harus ada di database.
//  2. Rekening dibuka dengan saldo awal 0 dan status 'active'.
//
// Catatan: aturan "satu anggota satu rekening per produk" bisa ditambahkan
// di sini di masa depan dengan query COUNT sebelum CreateAccount dipanggil.
func (s *savingService) OpenAccount(ctx context.Context, userID int64, req model.OpenAccountRequest) (*model.SavingsAccount, error) {
	// Validasi: pastikan produk simpanan yang dipilih ada.
	// Kita tidak perlu data produknya sekarang, cukup pastikan ia eksis.
	_, err := s.savingRepo.FindProductByID(ctx, req.SavingsProductID)
	if err != nil {
		if errors.Is(err, repository.ErrSavingsProductNotFound) {
			return nil, ErrSavingsProductNotFound
		}
		return nil, fmt.Errorf("validasi produk simpanan gagal: %w", err)
	}

	newAccount := &model.SavingsAccount{
		UserID:           userID,
		SavingsProductID: req.SavingsProductID,
	}

	saved, err := s.savingRepo.CreateAccount(ctx, newAccount)
	if err != nil {
		return nil, fmt.Errorf("buka rekening gagal: %w", err)
	}

	return saved, nil
}

// GetAccounts mengambil semua rekening simpanan milik user yang sedang login.
// Mengembalikan slice kosong (bukan error) jika user belum punya rekening.
func (s *savingService) GetAccounts(ctx context.Context, userID int64) ([]model.SavingsAccount, error) {
	accounts, err := s.savingRepo.GetAccountByUserID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("mengambil daftar rekening gagal: %w", err)
	}

	return accounts, nil
}

// DepositFund memproses setoran dana ke rekening simpanan.
//
// Aturan bisnis yang divalidasi (secara berurutan):
//  1. Rekening harus ada dan milik user yang sedang login.
//     (Jika rekening ada tapi milik orang lain → kembalikan "not found",
//     bukan "forbidden" — mencegah enumerasi ID rekening orang lain.)
//  2. Nominal setoran harus >= min_deposit produk simpanan.
//  3. Rekening harus berstatus 'active'.
//     (Langkah 3 divalidasi di repository layer saat FOR UPDATE, bukan di sini,
//     agar validasi terjadi pada data yang sudah dikunci dan paling terkini.)
//
// Eksekusi UPDATE balance + INSERT log transaksi dilimpahkan ke repository.Deposit()
// yang menjalankan keduanya dalam satu database transaction atomik.
func (s *savingService) DepositFund(ctx context.Context, userID int64, req model.DepositRequest) error {
	// --- Langkah 1: Ambil rekening & validasi kepemilikan ---
	account, err := s.savingRepo.GetAccountByID(ctx, req.AccountID)
	if err != nil {
		if errors.Is(err, repository.ErrSavingsAccountNotFound) {
			return ErrSavingsAccountNotFound
		}
		return fmt.Errorf("mengambil data rekening gagal: %w", err)
	}

	// Authorization check: rekening harus milik user yang sedang melakukan request.
	// Kembalikan ErrSavingsAccountNotFound (bukan error "akses ditolak") agar
	// penyerang tidak bisa memastikan apakah sebuah ID rekening valid atau tidak.
	if account.UserID != userID {
		return ErrSavingsAccountNotFound
	}

	// --- Langkah 2: Validasi nominal vs min_deposit produk ---
	product, err := s.savingRepo.FindProductByID(ctx, account.SavingsProductID)
	if err != nil {
		// Ini seharusnya tidak terjadi jika FK constraint database berjalan normal,
		// tapi kita tangani agar service tidak panic jika data tidak konsisten.
		return fmt.Errorf("mengambil data produk simpanan gagal: %w", err)
	}

	if req.Amount < product.MinDeposit {
		// Sematkan nilai minimum di pesan error agar handler bisa meneruskannya
		// ke client tanpa perlu query database lagi.
		return fmt.Errorf("%w: minimum Rp %.0f", ErrDepositBelowMinimum, product.MinDeposit)
	}

	// --- Langkah 3: Eksekusi setoran secara atomik ---
	// Semua operasi database (lock, update balance, insert log) terjadi di dalam
	// satu database transaction di repository layer. Service tidak tahu detail tx.
	if err := s.savingRepo.Deposit(ctx, req.AccountID, req.Amount, req.ReferenceID); err != nil {
		if errors.Is(err, repository.ErrAccountNotActive) {
			return ErrAccountNotActive
		}
		if errors.Is(err, repository.ErrSavingsAccountNotFound) {
			return ErrSavingsAccountNotFound
		}
		return fmt.Errorf("setoran gagal: %w", err)
	}

	return nil
}
