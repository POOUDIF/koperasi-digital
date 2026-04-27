// Package service — logika bisnis modul Pembiayaan Syariah Murabahah.
//
// Posisi dalam arsitektur:
//
//	Handler → FinancingService → FinancingRepository → Database
//
// Service layer adalah satu-satunya tempat aturan bisnis murabahah diimplementasi:
// penghitungan margin, validasi kelayakan, pembentukan financing_number,
// dan generasi jadwal angsuran saat approval.
package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"koperasi-digital/internal/model"
	"koperasi-digital/internal/repository"
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
	// tapi didefinisikan di sini untuk defense-in-depth.
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

// murabahahMarginRate adalah nisbah keuntungan koperasi yang ditetapkan secara fixed.
// 10% dari principal_amount — sesuai kebijakan koperasi versi awal.
const murabahahMarginRate = 0.10

// FinancingService mendefinisikan kontrak logika bisnis untuk modul pembiayaan.
type FinancingService interface {
	// ApplyMurabahah memproses pengajuan pembiayaan akad murabahah.
	// Menghitung margin & total, generate nomor unik, lalu simpan dengan status "pending".
	ApplyMurabahah(ctx context.Context, userID int64, req model.ApplyFinancingRequest) (*model.Financing, error)

	// GetMyFinancings mengambil semua pengajuan pembiayaan milik user.
	GetMyFinancings(ctx context.Context, userID int64) ([]model.Financing, error)

	// ReviewFinancing memproses keputusan admin terhadap satu pengajuan.
	//   action "approve" → update status + generate jadwal angsuran (atomik via DB tx).
	//   action "reject"  → update status saja.
	// Hanya pengajuan berstatus 'pending' yang bisa direview.
	ReviewFinancing(ctx context.Context, financingID int64, adminID int64, action string) (*model.Financing, error)

	// GetMyInstallments mengambil semua cicilan milik satu pengajuan pembiayaan.
	// Memvalidasi bahwa pengajuan tersebut benar-benar milik userID yang diberikan.
	GetMyInstallments(ctx context.Context, userID int64, financingID int64) ([]model.FinancingInstallment, error)

	// PayMyInstallment memproses pembayaran satu cicilan oleh anggota.
	// Validasi yang dilakukan (secara berurutan):
	//  1. Cicilan harus ada dan milik pengajuan yang dimiliki userID.
	//  2. Status cicilan harus 'unpaid'.
	//  3. Rekening simpanan harus aktif, milik userID, dan saldonya mencukupi.
	// Seluruh operasi DB (debit saldo, log transaksi, update cicilan, cek pelunasan)
	// dijalankan secara atomik di repository layer.
	PayMyInstallment(ctx context.Context, userID int64, installmentID int64, req model.PayInstallmentRequest) error
}

// financingService adalah implementasi konkret FinancingService.
type financingService struct {
	financingRepo repository.FinancingRepository
}

// NewFinancingService membuat instance service dengan dependency diinject.
func NewFinancingService(financingRepo repository.FinancingRepository) FinancingService {
	return &financingService{financingRepo: financingRepo}
}

// ApplyMurabahah memproses pengajuan pembiayaan murabahah oleh anggota.
//
// Urutan langkah:
//  1. Hitung margin_amount  = principal_amount × 10%.
//  2. Hitung total_payable  = principal_amount + margin_amount.
//  3. Generate financing_number unik berbasis unix nanosecond.
//  4. Bangun struct Financing dengan status "pending".
//  5. Simpan ke database via repository.
func (s *financingService) ApplyMurabahah(ctx context.Context, userID int64, req model.ApplyFinancingRequest) (*model.Financing, error) {
	marginAmount := req.PrincipalAmount * murabahahMarginRate
	totalPayable := req.PrincipalAmount + marginAmount

	financing := &model.Financing{
		FinancingNumber: fmt.Sprintf("FIN-MRB-%d", time.Now().UnixNano()),
		UserID:          userID,
		Akad:            "murabahah",
		PrincipalAmount: req.PrincipalAmount,
		MarginAmount:    marginAmount,
		TotalPayable:    totalPayable,
		DurationMonths:  req.DurationMonths,
		Status:          "pending",
	}

	saved, err := s.financingRepo.CreateFinancing(ctx, financing)
	if err != nil {
		if errors.Is(err, repository.ErrDuplicateFinancingNumber) {
			return nil, fmt.Errorf("terjadi konflik nomor pembiayaan, silakan coba lagi: %w", err)
		}
		return nil, fmt.Errorf("menyimpan pengajuan pembiayaan gagal: %w", err)
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
//
// Aturan bisnis:
//  1. Pengajuan harus ada di database.
//  2. Status saat ini harus 'pending' — tidak boleh mereview yang sudah diproses.
//  3. Jika approve: generate jadwal angsuran dan simpan semuanya secara atomik.
//  4. Jika reject: update status saja.
//  5. Kembalikan data financing terbaru sebagai respons.
//
// Generasi Jadwal Angsuran (Murabahah Flat):
//   - Nominal per angsuran = ceil(total_payable / duration_months) untuk semua kecuali
//     angsuran terakhir yang mendapat sisa (menghindari selisih akibat pembulatan).
//   - Tanggal jatuh tempo = tanggal approval + N bulan (N = nomor urut angsuran).
//   - Pembulatan ke 4 desimal dengan math.Round agar nilai DECIMAL(19,4) di DB akurat.
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
	// termasuk reviewed_at yang di-generate oleh database (NOW()).
	updated, err := s.financingRepo.FindByID(ctx, financingID)
	if err != nil {
		return nil, fmt.Errorf("mengambil data pembiayaan setelah review gagal: %w", err)
	}

	return updated, nil
}

// handleApprove menghitung jadwal angsuran dan memanggil repository untuk
// menyimpan update status + semua angsuran dalam satu database transaction.
//
// Ini adalah helper private — hanya dipanggil dari ReviewFinancing.
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
//
// Algoritma pembulatan yang adil (banker's rounding sederhana):
//   - Hitung nominal "ideal" per angsuran = total_payable / duration_months.
//   - Bulatkan ke 4 desimal menggunakan math.Round.
//   - Angsuran terakhir mendapat sisa = total_payable - (nominal × (n-1))
//     untuk memastikan total semua angsuran = total_payable persis.
//
// Contoh: total_payable = 16.500.000, duration = 12 bulan
//   → per angsuran = 1.375.000,0000
//   → angsuran 1–11 = 1.375.000,0000
//   → angsuran 12   = 16.500.000 - (1.375.000 × 11) = 1.375.000,0000 (pas)
//
// Tanggal jatuh tempo mulai 1 bulan dari sekarang (hari approval).
func (s *financingService) generateInstallments(f *model.Financing) []model.FinancingInstallment {
	n := f.DurationMonths

	// Bulatkan ke 4 desimal menggunakan faktor 10^4 = 10000.
	rawPerInstallment := f.TotalPayable / float64(n)
	perInstallment := math.Round(rawPerInstallment*10000) / 10000

	now := time.Now()
	installments := make([]model.FinancingInstallment, n)

	for i := 0; i < n; i++ {
		amount := perInstallment

		// Angsuran terakhir: sisa dari total agar tidak ada selisih pembulatan.
		if i == n-1 {
			paid := perInstallment * float64(n-1)
			amount = math.Round((f.TotalPayable-paid)*10000) / 10000
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
//
// Aturan otorisasi:
//   - Ambil data pengajuan terlebih dahulu dan pastikan user_id-nya cocok.
//   - Kembalikan ErrFinancingNotFound jika tidak ada atau bukan milik userID
//     (mencegah anggota mengintip cicilan milik orang lain via ID brute-force).
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
//
// Validasi yang dilakukan sebelum memanggil repository:
//  1. Cicilan harus ada (ErrInstallmentNotFound jika tidak).
//  2. Pengajuan induk harus milik userID (mencegah anggota membayar cicilan orang lain).
//  3. Status cicilan harus 'unpaid' (ErrInstallmentAlreadyPaid jika sudah paid).
//
// Validasi di dalam DB transaction (repository layer):
//  4. Rekening simpanan harus ada dan milik userID (ErrSavingsAccountNotFound).
//  5. Rekening harus berstatus 'active' (ErrAccountNotActive).
//  6. Saldo harus >= amount_due cicilan (ErrInsufficientBalance).
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
