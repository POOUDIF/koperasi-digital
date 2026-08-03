// Package service — logika bisnis modul Pembiayaan Syariah Murabahah.
// Posisi dalam arsitektur:
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"koperasi-digital/internal/model"
	"koperasi-digital/internal/repository"
	"koperasi-digital/internal/util"
)

// Sentinel errors modul pembiayaan.
// Handler memeriksa error ini untuk menentukan HTTP status code yang tepat.
var (
	// ErrFinancingNotFound dikembalikan saat pengajuan tidak ditemukan.
	ErrFinancingNotFound = errors.New("pengajuan pembiayaan tidak ditemukan")

	// ErrFinancingNotPending dikembalikan saat admin mencoba mereview pengajuan
	// yang sudah bukan berstatus 'pending' (sudah diproses sebelumnya).
	ErrFinancingNotPending = errors.New("pengajuan sudah diproses sebelumnya")

	// ErrInvalidReviewAction dikembalikan jika action selain "approve"/"reject" dikirim.
	// Dalam praktik sudah dicegah oleh binding:"oneof=approve reject" di handler,
	ErrInvalidReviewAction = errors.New("aksi review tidak valid, gunakan 'approve' atau 'reject'")

	// ErrInstallmentNotFound dikembalikan saat cicilan tidak ditemukan atau bukan
	// milik user yang sedang login (intentionally ambiguous untuk mencegah enumerasi).
	ErrInstallmentNotFound = errors.New("cicilan tidak ditemukan")

	// ErrInstallmentAlreadyPaid dikembalikan saat cicilan yang diminta sudah lunas.
	ErrInstallmentAlreadyPaid = errors.New("cicilan sudah dibayar sebelumnya")

	// ErrInsufficientBalance dikembalikan saat saldo rekening simpanan tidak cukup
	// untuk melunasi nominal cicilan yang diminta.
	ErrInsufficientBalance = errors.New("saldo rekening tidak mencukupi")
)

// Catatan: ErrSavingsAccountNotFound dan ErrAccountNotActive sudah dideklarasikan
// di saving_service.go dalam package yang sama — tidak perlu didefinisikan ulang.

// FinancingService mendefinisikan kontrak logika bisnis untuk modul pembiayaan.
type FinancingService interface {
	// ApplyMurabahah memproses pengajuan pembiayaan akad murabahah.
	// Menghitung margin & total, generate nomor unik, lalu simpan dengan status "pending".
	ApplyMurabahah(ctx context.Context, userID int64, req model.ApplyFinancingRequest) (*model.Financing, error)

	// GetMyFinancings mengambil semua pengajuan pembiayaan milik user.
	GetMyFinancings(ctx context.Context, userID int64) ([]model.Financing, error)

	// ReviewFinancing memproses keputusan admin terhadap satu pengajuan.
	// action "approve" → update status + generate jadwal angsuran (atomik via DB tx).
	ReviewFinancing(ctx context.Context, financingID int64, adminID int64, action string) (*model.Financing, error)

	// GetMyInstallments mengambil semua cicilan milik satu pengajuan pembiayaan.
	// Memvalidasi bahwa pengajuan tersebut benar-benar milik userID yang diberikan.
	GetMyInstallments(ctx context.Context, userID int64, financingID int64) ([]model.FinancingInstallment, error)

	// PayMyInstallment memproses pembayaran satu cicilan oleh anggota.
	// Validasi yang dilakukan (secara berurutan):
	PayMyInstallment(ctx context.Context, userID int64, installmentID int64, req model.PayInstallmentRequest) error
}

// financingService adalah implementasi konkret FinancingService.
type financingService struct {
	financingRepo repository.FinancingRepository
	marginRate    float64
}

// NewFinancingService membuat instance service dengan dependency diinject.
func NewFinancingService(financingRepo repository.FinancingRepository, marginRate float64) FinancingService {
	return &financingService{
		financingRepo: financingRepo,
		marginRate:    marginRate,
	}
}

// ApplyMurabahah memproses pengajuan pembiayaan murabahah oleh anggota.
// Urutan langkah:
func (s *financingService) ApplyMurabahah(ctx context.Context, userID int64, req model.ApplyFinancingRequest) (*model.Financing, error) {
	// Kita gunakan helper roundTo4Decimals agar tidak ada pergeseran presisi.
	marginAmount := util.RoundTo4Decimals(req.PrincipalAmount * s.marginRate)
	totalPayable := util.RoundTo4Decimals(req.PrincipalAmount + marginAmount)

	var saved *model.Financing
	var err error

	// GAP-05: Retry loop (maks 3 kali) untuk konflik FinancingNumber yang jarang (race condition).
	maxAttempts := 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		financing := &model.Financing{
			FinancingNumber: fmt.Sprintf("FIN-MRB-%d-%d", time.Now().UnixNano(), attempt),
			UserID:          userID,
			Akad:            "murabahah",
			PrincipalAmount: req.PrincipalAmount,
			MarginAmount:    marginAmount,
			TotalPayable:    totalPayable,
			DurationMonths:  req.DurationMonths,
			Status:          "pending",
		}

		saved, err = s.financingRepo.CreateFinancing(ctx, financing)
		if err != nil {
			if errors.Is(err, repository.ErrDuplicateFinancingNumber) {
				// Bila clash, loop lagi. Jika iterasi habis, return error ke user.
				continue
			}
			return nil, fmt.Errorf("menyimpan pengajuan pembiayaan gagal: %w", err)
		}
		// Berhasil simpan.
		break
	}

	if err != nil { // Masih ada error sisa dari percobaan terakhir
		if errors.Is(err, repository.ErrDuplicateFinancingNumber) {
			return nil, fmt.Errorf("sistem sibuk membuat nomor pembiayaan, silakan coba lagi: %w", err)
		}
		// Safe fallback.
		return nil, fmt.Errorf("pengajuan pembiayaan gagal total: %w", err)
	}

	return saved, nil
}

// GetMyFinancings mengambil semua pengajuan pembiayaan milik user.
func (s *financingService) GetMyFinancings(ctx context.Context, userID int64) ([]model.Financing, error) {
	financings, err := s.financingRepo.FindByUserID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("mengambil daftar pembiayaan gagal: %w", err)
	}

	return financings, nil
}

// ReviewFinancing memproses keputusan admin (approve/reject) terhadap pengajuan.
// Aturan bisnis:
func (s *financingService) ReviewFinancing(ctx context.Context, financingID int64, adminID int64, action string) (*model.Financing, error) {
	// Langkah 1: Ambil data pengajuan.
	financing, err := s.financingRepo.FindByID(ctx, financingID)
	if err != nil {
		if errors.Is(err, repository.ErrFinancingNotFound) {
			return nil, ErrFinancingNotFound
		}
		return nil, fmt.Errorf("mengambil data pembiayaan gagal: %w", err)
	}

	// Langkah 2: Validasi status — hanya 'pending' yang bisa direview.
	if financing.Status != "pending" {
		return nil, ErrFinancingNotPending
	}

	// Langkah 3: Eksekusi aksi.
	switch action {
	case "approve":
		if err := s.handleApprove(ctx, financing, adminID); err != nil {
			return nil, err
		}

	case "reject":
		if err := s.financingRepo.UpdateStatus(ctx, financingID, "rejected", adminID); err != nil {
			if errors.Is(err, repository.ErrFinancingNotFound) {
				return nil, ErrFinancingNotFound
			}
			return nil, fmt.Errorf("menolak pengajuan gagal: %w", err)
		}

	default:
		// Defense-in-depth: seharusnya sudah dicegah oleh binding handler.
		return nil, ErrInvalidReviewAction
	}

	// Langkah 4: Ambil ulang data terbaru dari DB untuk dikembalikan ke handler.
	// Ini memastikan response mencerminkan state yang benar-benar tersimpan,
	updated, err := s.financingRepo.FindByID(ctx, financingID)
	if err != nil {
		return nil, fmt.Errorf("mengambil data pembiayaan setelah review gagal: %w", err)
	}

	return updated, nil
}

// handleApprove menghitung jadwal angsuran dan memanggil repository untuk
// menyimpan update status + semua angsuran dalam satu database transaction.
func (s *financingService) handleApprove(ctx context.Context, f *model.Financing, adminID int64) error {
	installments := s.generateInstallments(f)

	if err := s.financingRepo.ApproveWithInstallments(ctx, f.ID, adminID, installments); err != nil {
		if errors.Is(err, repository.ErrFinancingNotFound) {
			return ErrFinancingNotFound
		}
		return fmt.Errorf("menyetujui pengajuan gagal: %w", err)
	}

	return nil
}

// generateInstallments menghasilkan slice jadwal angsuran untuk pembiayaan murabahah flat.
// Algoritma pembulatan yang adil (banker's rounding sederhana):
func (s *financingService) generateInstallments(f *model.Financing) []model.FinancingInstallment {
	n := f.DurationMonths

	// Bulatkan ke 4 desimal menggunakan helper agar seragam.
	rawPerInstallment := f.TotalPayable / float64(n)
	perInstallment := util.RoundTo4Decimals(rawPerInstallment)

	now := time.Now()
	installments := make([]model.FinancingInstallment, n)

	for i := 0; i < n; i++ {
		amount := perInstallment

		// Angsuran terakhir: sisa dari total agar tidak ada selisih pembulatan.
		if i == n-1 {
			paid := util.RoundTo4Decimals(perInstallment * float64(n-1))
			amount = util.RoundTo4Decimals(f.TotalPayable - paid)
		}

		installments[i] = model.FinancingInstallment{
			InstallmentNumber: i + 1,
			AmountDue:         amount,
			AmountPaid:        0,
			// Jatuh tempo dimulai 1 bulan setelah approval, lalu bertambah per bulan.
			DueDate: now.AddDate(0, i+1, 0),
			Status:  "unpaid",
		}
	}

	return installments
}

// GetMyInstallments mengambil semua cicilan milik satu pengajuan pembiayaan.
// Aturan otorisasi:
func (s *financingService) GetMyInstallments(ctx context.Context, userID int64, financingID int64) ([]model.FinancingInstallment, error) {
	financing, err := s.financingRepo.FindByID(ctx, financingID)
	if err != nil {
		if errors.Is(err, repository.ErrFinancingNotFound) {
			return nil, ErrFinancingNotFound
		}
		return nil, fmt.Errorf("mengambil data pembiayaan gagal: %w", err)
	}

	if financing.UserID != userID {
		// Sengaja kembalikan "not found" — bukan "forbidden" — agar penyerang
		// tidak bisa memastikan apakah sebuah financing ID valid atau tidak.
		return nil, ErrFinancingNotFound
	}

	installments, err := s.financingRepo.GetInstallmentsByFinancingID(ctx, financingID)
	if err != nil {
		return nil, fmt.Errorf("mengambil daftar cicilan gagal: %w", err)
	}

	return installments, nil
}

// PayMyInstallment memproses pembayaran satu cicilan oleh anggota.
// Validasi yang dilakukan sebelum memanggil repository:
func (s *financingService) PayMyInstallment(ctx context.Context, userID int64, installmentID int64, req model.PayInstallmentRequest) error {
	// Langkah 1: Ambil data cicilan.
	installment, err := s.financingRepo.GetInstallmentByID(ctx, installmentID)
	if err != nil {
		if errors.Is(err, repository.ErrInstallmentNotFound) {
			return ErrInstallmentNotFound
		}
		return fmt.Errorf("mengambil data cicilan gagal: %w", err)
	}

	// Langkah 2: Validasi kepemilikan via pengajuan induk.
	financing, err := s.financingRepo.FindByID(ctx, installment.FinancingID)
	if err != nil {
		if errors.Is(err, repository.ErrFinancingNotFound) {
			return ErrInstallmentNotFound // sembunyikan detail internal
		}
		return fmt.Errorf("mengambil data pembiayaan gagal: %w", err)
	}

	if financing.UserID != userID {
		return ErrInstallmentNotFound // bukan "forbidden" — mencegah enumerasi
	}

	// Langkah 3: Validasi status cicilan.
	if installment.Status == "paid" {
		return ErrInstallmentAlreadyPaid
	}

	// Langkah 4–6 + semua operasi DB diserahkan ke repository yang menjalankannya
	// dalam satu database transaction atomik.
	err = s.financingRepo.PayInstallment(
		ctx,
		installmentID,
		installment.FinancingID,
		installment.AmountDue,
		req.SavingsAccountID,
		userID,
	)
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrSavingsAccountNotFound):
			return ErrSavingsAccountNotFound
		case errors.Is(err, repository.ErrAccountNotActive):
			return ErrAccountNotActive
		case errors.Is(err, repository.ErrInsufficientBalance):
			return ErrInsufficientBalance
		case errors.Is(err, repository.ErrInstallmentAlreadyPaid):
			// Race condition: cicilan dibayar oleh request lain di jeda waktu validasi di atas.
			return ErrInstallmentAlreadyPaid
		default:
			return fmt.Errorf("pembayaran cicilan gagal: %w", err)
		}
	}

	return nil
}
