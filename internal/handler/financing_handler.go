// Package handler — HTTP handler untuk modul Pembiayaan Syariah.
// Tanggung jawab FinancingHandler:
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
// Endpoint ini dilindungi middleware JWT. Anggota mengirimkan nominal pembiayaan
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
		c.JSON(http.StatusInternalServerError, errorResponse{Error: "terjadi kesalahan pada server"})
		return
	}

	// 201 Created — pengajuan berhasil disimpan dengan status "pending".
	c.JSON(http.StatusCreated, financing)
}

// GetMyFinancings menangani GET /api/v1/financing.
// Mengembalikan semua pengajuan pembiayaan milik user yang sedang login,
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
// Endpoint ini dilindungi oleh dua middleware yang dirangkai:
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
// Mengembalikan daftar jadwal cicilan untuk satu pengajuan pembiayaan milik user.
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
// Anggota membayar satu cicilan dengan mendebet saldo rekening simpanannya.
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
