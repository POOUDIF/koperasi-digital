// Package handler — HTTP handler untuk modul Pembiayaan Syariah.
//
// Tanggung jawab FinancingHandler:
//  1. Ekstrak user_id dari Gin context (diisi middleware JWT).
//  2. Baca & validasi request body JSON.
//  3. Delegasikan ke FinancingService.
//  4. Terjemahkan hasil/error menjadi HTTP response yang tepat.
//
// Catatan package-level: errorResponse dan extractUserID tidak didefinisikan
// ulang di sini karena sudah ada di user_handler.go dan saving_handler.go
// dalam package yang sama (package handler). Go mengizinkan penggunaan
// identifier yang dideklarasikan di file lain dalam satu package.
package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"koperasi-digital/internal/model"
	"koperasi-digital/internal/service"
)

// FinancingHandler mengelompokkan dependency untuk endpoint-endpoint pembiayaan.
type FinancingHandler struct {
	financingService service.FinancingService
}

// NewFinancingHandler membuat instance FinancingHandler dengan dependency diinject.
// Menerima interface (bukan struct konkret) untuk kemudahan testing.
func NewFinancingHandler(financingService service.FinancingService) *FinancingHandler {
	return &FinancingHandler{financingService: financingService}
}

// Apply menangani POST /api/v1/financing/apply.
//
// Endpoint ini dilindungi middleware JWT. Anggota mengirimkan nominal pembiayaan
// dan tenor; sistem menghitung margin, total, generate nomor unik, lalu menyimpan
// pengajuan dengan status "pending" untuk diproses admin/komite.
//
// Request body (JSON):
//
//	{
//	  "principal_amount": 15000000,
//	  "duration_months": 12
//	}
//
// Response sukses (201 Created):
//
//	{
//	  "id": 1,
//	  "financing_number": "FIN-MRB-1713261234567890",
//	  "user_id": 42,
//	  "akad": "murabahah",
//	  "principal_amount": 15000000,
//	  "margin_amount": 1500000,
//	  "total_payable": 16500000,
//	  "duration_months": 12,
//	  "status": "pending",
//	  "created_at": "2024-04-16T10:30:00Z"
//	}
//
// Response gagal:
//   - 400 Bad Request  : body tidak valid / field wajib kosong / nilai di luar batas
//   - 401 Unauthorized : token JWT tidak ada atau tidak valid
//   - 500 Internal     : error tak terduga di server
func (h *FinancingHandler) Apply(c *gin.Context) {
	// Ambil user_id dari Gin context yang sudah diisi oleh middleware.RequireAuth.
	// extractUserID didefinisikan di saving_handler.go dalam package yang sama.
	userID, ok := extractUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, errorResponse{Error: "sesi tidak valid, silakan login kembali"})
		return
	}

	var req model.ApplyFinancingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// Gin mengembalikan detail field mana yang gagal validasi —
		// aman dikirim ke client karena tidak mengandung informasi internal server.
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	financing, err := h.financingService.ApplyMurabahah(c.Request.Context(), userID, req)
	if err != nil {
		// Semua error dari service layer (termasuk DB error) diperlakukan sebagai
		// 500 karena tidak ada error bisnis khusus di endpoint ini yang perlu
		// diterjemahkan ke status code berbeda.
		// Jangan kirim detail error internal ke client.
		c.JSON(http.StatusInternalServerError, errorResponse{Error: "terjadi kesalahan pada server"})
		return
	}

	// 201 Created — pengajuan berhasil disimpan dengan status "pending".
	c.JSON(http.StatusCreated, financing)
}

// GetMyFinancings menangani GET /api/v1/financing.
//
// Mengembalikan semua pengajuan pembiayaan milik user yang sedang login,
// termasuk status terkini dari setiap pengajuan.
//
// Response sukses (200 OK):
//
//	{ "financings": [ { "id": 1, "financing_number": "FIN-MRB-...", ... } ] }
//
// Response gagal:
//   - 401 Unauthorized : token JWT tidak ada atau tidak valid
//   - 500 Internal     : error tak terduga di server
func (h *FinancingHandler) GetMyFinancings(c *gin.Context) {
	userID, ok := extractUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, errorResponse{Error: "sesi tidak valid, silakan login kembali"})
		return
	}

	financings, err := h.financingService.GetMyFinancings(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse{Error: "terjadi kesalahan pada server"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"financings": financings})
}

// Review menangani PUT /api/v1/admin/financing/:id/review.
//
// Endpoint ini dilindungi oleh dua middleware yang dirangkai:
//   - RequireAuth   : memastikan request membawa JWT yang valid.
//   - RequireRole   : memastikan user memiliki role admin/pengurus/super_admin.
//
// Admin mengirimkan keputusan "approve" atau "reject".
// Jika approve, sistem secara otomatis menghasilkan jadwal angsuran bulanan.
//
// URL param:
//   - :id — primary key pengajuan yang akan direview
//
// Request body (JSON):
//
//	{ "action": "approve" }   atau   { "action": "reject" }
//
// Response sukses (200 OK) — data financing dengan status terbaru:
//
//	{
//	  "id": 1,
//	  "financing_number": "FIN-MRB-...",
//	  "status": "approved",
//	  "reviewed_by": 99,
//	  "reviewed_at": "2024-04-16T10:30:00Z",
//	  ...
//	}
//
// Response gagal:
//   - 400 Bad Request         : body tidak valid / :id bukan angka
//   - 401 Unauthorized        : token JWT tidak ada atau tidak valid
//   - 403 Forbidden           : role tidak mencukupi (bukan admin/pengurus/super_admin)
//   - 404 Not Found           : pengajuan dengan id tersebut tidak ditemukan
//   - 409 Conflict            : pengajuan sudah pernah diproses (bukan 'pending')
//   - 500 Internal            : error tak terduga di server
func (h *FinancingHandler) Review(c *gin.Context) {
	// Ambil admin_id dari Gin context — diisi oleh RequireAuth.
	adminID, ok := extractUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, errorResponse{Error: "sesi tidak valid, silakan login kembali"})
		return
	}

	// Parse :id dari URL param — harus berupa integer positif.
	rawID := c.Param("id")
	financingID, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || financingID <= 0 {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "parameter id tidak valid"})
		return
	}

	var req model.ReviewFinancingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// binding:"oneof=approve reject" memastikan hanya dua nilai yang valid.
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	updated, err := h.financingService.ReviewFinancing(c.Request.Context(), financingID, adminID, req.Action)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrFinancingNotFound):
			c.JSON(http.StatusNotFound, errorResponse{Error: "pengajuan pembiayaan tidak ditemukan"})

		case errors.Is(err, service.ErrFinancingNotPending):
			// 409 Conflict — resource ada tapi kondisinya tidak memungkinkan operasi ini.
			c.JSON(http.StatusConflict, errorResponse{Error: "pengajuan sudah pernah diproses sebelumnya"})

		default:
			c.JSON(http.StatusInternalServerError, errorResponse{Error: "terjadi kesalahan pada server"})
		}
		return
	}

	c.JSON(http.StatusOK, updated)
}

// GetInstallments menangani GET /api/v1/financing/:id/installments.
//
// Mengembalikan daftar jadwal cicilan untuk satu pengajuan pembiayaan milik user.
// Endpoint dilindungi middleware JWT; hanya pemilik pengajuan yang bisa mengaksesnya.
//
// URL param:
//   - :id — primary key pengajuan (financing_id)
//
// Response sukses (200 OK):
//
//	{ "installments": [ { "id": 1, "installment_number": 1, "amount_due": 1375000, ... } ] }
//
// Response gagal:
//   - 400 Bad Request  : :id bukan angka valid
//   - 401 Unauthorized : token JWT tidak ada atau tidak valid
//   - 404 Not Found    : pengajuan tidak ada atau bukan milik user ini
//   - 500 Internal     : error tak terduga di server
func (h *FinancingHandler) GetInstallments(c *gin.Context) {
	userID, ok := extractUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, errorResponse{Error: "sesi tidak valid, silakan login kembali"})
		return
	}

	rawID := c.Param("id")
	financingID, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || financingID <= 0 {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "parameter id tidak valid"})
		return
	}

	installments, err := h.financingService.GetMyInstallments(c.Request.Context(), userID, financingID)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrFinancingNotFound):
			c.JSON(http.StatusNotFound, errorResponse{Error: "pengajuan pembiayaan tidak ditemukan"})
		default:
			c.JSON(http.StatusInternalServerError, errorResponse{Error: "terjadi kesalahan pada server"})
		}
		return
	}

	c.JSON(http.StatusOK, gin.H{"installments": installments})
}

// PayInstallment menangani POST /api/v1/financing/installments/:id/pay.
//
// Anggota membayar satu cicilan dengan mendebet saldo rekening simpanannya.
// Seluruh operasi (debit saldo, log transaksi, update cicilan, cek pelunasan penuh)
// dijalankan secara atomik — tidak ada state parsial jika terjadi kegagalan.
//
// URL param:
//   - :id — primary key cicilan yang akan dibayar
//
// Request body (JSON):
//
//	{ "savings_account_id": 3 }
//
// Response sukses (200 OK):
//
//	{ "message": "pembayaran cicilan berhasil" }
//
// Response gagal:
//   - 400 Bad Request            : :id bukan angka / body tidak valid
//   - 401 Unauthorized           : token JWT tidak ada atau tidak valid
//   - 404 Not Found              : cicilan atau rekening tidak ditemukan
//   - 409 Conflict               : cicilan sudah dibayar sebelumnya
//   - 422 Unprocessable Entity   : saldo tidak cukup / rekening tidak aktif
//   - 500 Internal               : error tak terduga di server
func (h *FinancingHandler) PayInstallment(c *gin.Context) {
	userID, ok := extractUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, errorResponse{Error: "sesi tidak valid, silakan login kembali"})
		return
	}

	rawID := c.Param("id")
	installmentID, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || installmentID <= 0 {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "parameter id cicilan tidak valid"})
		return
	}

	var req model.PayInstallmentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	err = h.financingService.PayMyInstallment(c.Request.Context(), userID, installmentID, req)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInstallmentNotFound):
			c.JSON(http.StatusNotFound, errorResponse{Error: "cicilan tidak ditemukan"})
		case errors.Is(err, service.ErrSavingsAccountNotFound):
			c.JSON(http.StatusNotFound, errorResponse{Error: "rekening simpanan tidak ditemukan"})
		case errors.Is(err, service.ErrInstallmentAlreadyPaid):
			c.JSON(http.StatusConflict, errorResponse{Error: "cicilan sudah dibayar sebelumnya"})
		case errors.Is(err, service.ErrInsufficientBalance):
			c.JSON(http.StatusUnprocessableEntity, errorResponse{Error: "saldo rekening tidak mencukupi"})
		case errors.Is(err, service.ErrAccountNotActive):
			c.JSON(http.StatusUnprocessableEntity, errorResponse{Error: "rekening simpanan tidak aktif"})
		default:
			c.JSON(http.StatusInternalServerError, errorResponse{Error: "terjadi kesalahan pada server"})
		}
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "pembayaran cicilan berhasil"})
}
