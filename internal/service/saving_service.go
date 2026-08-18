// Package service — logika bisnis modul simpanan syariah.
// Layer ini berada di antara handler dan repository:
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

	// OpenMandatoryAccounts dipanggil saat registrasi untuk membuat rekening Simpanan Pokok & Wajib secara otomatis.
	OpenMandatoryAccounts(ctx context.Context, userID int64) error

	// GetAccounts mengambil semua rekening simpanan milik user.
	GetAccounts(ctx context.Context, userID int64) ([]model.SavingsAccount, error)

	// DepositFund membuat permohonan setoran dana (status pending).
	DepositFund(ctx context.Context, userID int64, req model.DepositRequest) (*model.DepositRequestModel, error)

	// GetAllTransactions mengambil semua mutasi (log) dari semua rekening simpanan.
	GetAllTransactions(ctx context.Context) ([]model.SavingsTransaction, error)

	// GetDepositRequests mengambil riwayat setoran per user.
	GetDepositRequests(ctx context.Context, userID int64) ([]model.DepositRequestModel, error)

	// GetAllDepositRequestsAdmin mengambil semua riwayat setoran (khusus admin).
	GetAllDepositRequestsAdmin(ctx context.Context) ([]model.DepositRequestModel, error)

	// ReviewDeposit memproses review admin (approve/reject).
	ReviewDeposit(ctx context.Context, adminID int64, requestID int64, req model.ReviewDepositRequest) error
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
// Aturan bisnis:
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

// OpenMandatoryAccounts membuat rekening untuk semua produk simpanan yang wajib.
// Dipanggil oleh UserHandler secara asinkron atau sinkron setelah registrasi.
func (s *savingService) OpenMandatoryAccounts(ctx context.Context, userID int64) error {
	products, err := s.savingRepo.GetMandatoryProducts(ctx)
	if err != nil {
		return fmt.Errorf("gagal mengambil produk mandatory: %w", err)
	}

	for _, p := range products {
		account := &model.SavingsAccount{
			UserID:           userID,
			SavingsProductID: p.ID,
		}
		// CreateAccount akan otomatis membuat rekening dengan balance 0.
		_, err := s.savingRepo.CreateAccount(ctx, account)
		if err != nil {
			// Kita bisa return error, tapi jika salah satu gagal, mungkin kita
			// tidak ingin me-rollback semuanya. Namun untuk sekarang kita return error.
			return fmt.Errorf("gagal membuat rekening mandatory %s: %w", p.Name, err)
		}
	}
	return nil
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

// DepositFund membuat permohonan setoran dana (status pending).
func (s *savingService) DepositFund(ctx context.Context, userID int64, req model.DepositRequest) (*model.DepositRequestModel, error) {
	// --- Langkah 1: Ambil rekening & validasi kepemilikan ---
	account, err := s.savingRepo.GetAccountByID(ctx, req.AccountID)
	if err != nil {
		if errors.Is(err, repository.ErrSavingsAccountNotFound) {
			return nil, ErrSavingsAccountNotFound
		}
		return nil, fmt.Errorf("mengambil data rekening gagal: %w", err)
	}

	if account.UserID != userID {
		return nil, ErrSavingsAccountNotFound
	}

	// --- Langkah 2: Validasi nominal vs min_deposit produk ---
	product, err := s.savingRepo.FindProductByID(ctx, account.SavingsProductID)
	if err != nil {
		return nil, fmt.Errorf("mengambil data produk simpanan gagal: %w", err)
	}

	if req.Amount < product.MinDeposit {
		return nil, fmt.Errorf("%w: minimum Rp %.0f", ErrDepositBelowMinimum, product.MinDeposit)
	}

	// --- Langkah 3: Insert Deposit Request ---
	depositModel := &model.DepositRequestModel{
		UserID:           userID,
		SavingsAccountID: req.AccountID,
		Amount:           req.Amount,
		PaymentMethod:    req.PaymentMethod,
		ProofImageURL:    req.ProofImageURL,
		ReferenceID:      req.ReferenceID,
	}

	createdReq, err := s.savingRepo.InsertDepositRequest(ctx, depositModel)
	if err != nil {
		return nil, fmt.Errorf("menyimpan permohonan setoran gagal: %w", err)
	}

	return createdReq, nil
}

// GetAllTransactions mengambil semua mutasi (log) dari semua rekening simpanan.
func (s *savingService) GetAllTransactions(ctx context.Context) ([]model.SavingsTransaction, error) {
	txs, err := s.savingRepo.GetAllTransactions(ctx)
	if err != nil {
		return nil, fmt.Errorf("mengambil daftar semua transaksi simpanan gagal: %w", err)
	}
	return txs, nil
}

func (s *savingService) GetDepositRequests(ctx context.Context, userID int64) ([]model.DepositRequestModel, error) {
	return s.savingRepo.GetDepositRequestsByUserID(ctx, userID)
}

func (s *savingService) GetAllDepositRequestsAdmin(ctx context.Context) ([]model.DepositRequestModel, error) {
	return s.savingRepo.GetAllDepositRequests(ctx)
}

func (s *savingService) ReviewDeposit(ctx context.Context, adminID int64, requestID int64, req model.ReviewDepositRequest) error {
	depositReq, err := s.savingRepo.GetDepositRequestByID(ctx, requestID)
	if err != nil {
		if errors.Is(err, repository.ErrDepositRequestNotFound) {
			return repository.ErrDepositRequestNotFound
		}
		return fmt.Errorf("gagal mengambil deposit request: %w", err)
	}

	if depositReq.Status != "pending" {
		return errors.New("permohonan setoran sudah direview sebelumnya")
	}

	if req.Action == "reject" {
		return s.savingRepo.UpdateDepositRequestStatus(ctx, nil, requestID, "rejected", adminID)
	}

	if req.Action == "approve" {
		// 1. Eksekusi penambahan balance
		err := s.savingRepo.Deposit(ctx, depositReq.SavingsAccountID, depositReq.Amount, depositReq.ReferenceID)
		if err != nil {
			return fmt.Errorf("gagal menyetujui setoran: %w", err)
		}

		// 2. Update status request
		err = s.savingRepo.UpdateDepositRequestStatus(ctx, nil, requestID, "approved", adminID)
		if err != nil {
			return fmt.Errorf("setoran berhasil tapi gagal update status request: %w", err)
		}
	}

	return nil
}
