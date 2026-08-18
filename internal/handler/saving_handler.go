// Package handler — HTTP handler untuk modul simpanan syariah.
// Tanggung jawab SavingHandler:
package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"koperasi-digital/internal/middleware"
	"koperasi-digital/internal/model"
	"koperasi-digital/internal/repository"
	"koperasi-digital/internal/service"
)

// SavingHandler mengelompokkan dependency untuk endpoint-endpoint simpanan.
type SavingHandler struct {
	savingService service.SavingService
}

// NewSavingHandler membuat instance SavingHandler dengan dependency diinject.
// Menerima interface (bukan struct konkret) agar bisa di-mock saat testing.
func NewSavingHandler(savingService service.SavingService) *SavingHandler {
	return &SavingHandler{savingService: savingService}
}

// extractUserID mengambil user_id dari Gin context yang sudah diisi middleware JWT.
// Ini adalah helper privat yang menghilangkan duplikasi boilerplate di setiap handler.
func extractUserID(c *gin.Context) (int64, bool) {
	rawID, exists := c.Get(middleware.ContextKeyUserID)
	if !exists {
		return 0, false
	}

	// Gin menyimpan nilai sebagai `any`. Type-assert ke int64 sesuai dengan
	// tipe yang di-Set oleh middleware.RequireAuth.
	userID, ok := rawID.(int64)
	return userID, ok
}

// OpenAccount menangani POST /api/v1/savings/accounts.
// Membuka rekening simpanan baru untuk anggota yang sedang login.
func (h *SavingHandler) OpenAccount(c *gin.Context) {
	userID, ok := extractUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, errorResponse{Error: "sesi tidak valid, silakan login kembali"})
		return
	}

	var req model.OpenAccountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	account, err := h.savingService.OpenAccount(c.Request.Context(), userID, req)
	if err != nil {
		if errors.Is(err, service.ErrSavingsProductNotFound) {
			c.JSON(http.StatusNotFound, errorResponse{Error: "produk simpanan tidak ditemukan"})
			return
		}
		c.JSON(http.StatusInternalServerError, errorResponse{Error: "terjadi kesalahan pada server"})
		return
	}

	c.JSON(http.StatusCreated, account)
}

// GetAccounts menangani GET /api/v1/savings/accounts.
// Mengembalikan semua rekening simpanan milik user yang sedang login.
func (h *SavingHandler) GetAccounts(c *gin.Context) {
	userID, ok := extractUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, errorResponse{Error: "sesi tidak valid, silakan login kembali"})
		return
	}

	accounts, err := h.savingService.GetAccounts(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse{Error: "terjadi kesalahan pada server"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"accounts": accounts})
}

// Deposit menangani POST /api/v1/savings/deposit.
// Menyetor dana ke rekening simpanan milik user yang sedang login.
func (h *SavingHandler) Deposit(c *gin.Context) {
	userID, ok := extractUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, errorResponse{Error: "sesi tidak valid, silakan login kembali"})
		return
	}

	var req model.DepositRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	reqModel, err := h.savingService.DepositFund(c.Request.Context(), userID, req)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrSavingsAccountNotFound):
			c.JSON(http.StatusNotFound, errorResponse{Error: "rekening simpanan tidak ditemukan"})

		case errors.Is(err, service.ErrAccountNotActive):
			c.JSON(http.StatusUnprocessableEntity, errorResponse{Error: "rekening simpanan tidak aktif"})

		case errors.Is(err, service.ErrDepositBelowMinimum):
			c.JSON(http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})

		default:
			c.JSON(http.StatusInternalServerError, errorResponse{Error: "terjadi kesalahan pada server"})
		}
		return
	}

	c.JSON(http.StatusCreated, reqModel)
}

// GetAllTransactions menangani GET /api/v1/admin/transactions/saving.
// Mengembalikan daftar semua log transaksi simpanan (untuk admin).
func (h *SavingHandler) GetAllTransactions(c *gin.Context) {
	txs, err := h.savingService.GetAllTransactions(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse{Error: "gagal mengambil daftar semua log simpanan"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"transactions": txs})
}

// GetDepositRequests menangani GET /api/v1/savings/deposit-requests.
func (h *SavingHandler) GetDepositRequests(c *gin.Context) {
	userID, ok := extractUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, errorResponse{Error: "sesi tidak valid"})
		return
	}

	reqs, err := h.savingService.GetDepositRequests(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse{Error: "gagal mengambil riwayat setoran"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"deposit_requests": reqs})
}

// GetAllDepositRequestsAdmin menangani GET /api/v1/admin/savings/deposit-requests.
func (h *SavingHandler) GetAllDepositRequestsAdmin(c *gin.Context) {
	reqs, err := h.savingService.GetAllDepositRequestsAdmin(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse{Error: "gagal mengambil semua riwayat setoran"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"deposit_requests": reqs})
}

// ReviewDeposit menangani PUT /api/v1/admin/savings/deposit-requests/:id/review.
func (h *SavingHandler) ReviewDeposit(c *gin.Context) {
	adminID, ok := extractUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, errorResponse{Error: "sesi tidak valid"})
		return
	}

	var uri struct {
		ID int64 `uri:"id" binding:"required,gt=0"`
	}
	if err := c.ShouldBindUri(&uri); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "ID request tidak valid"})
		return
	}

	var req model.ReviewDepositRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	err := h.savingService.ReviewDeposit(c.Request.Context(), adminID, uri.ID, req)
	if err != nil {
		if errors.Is(err, repository.ErrDepositRequestNotFound) {
			c.JSON(http.StatusNotFound, errorResponse{Error: "permohonan setoran tidak ditemukan"})
			return
		}
		c.JSON(http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "review setoran berhasil disimpan"})
}

