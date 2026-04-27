// Package handler — HTTP handler untuk modul simpanan syariah.
//
// Tanggung jawab SavingHandler:
//  1. Membaca user_id dari Gin context (diinject oleh middleware JWT).
//  2. Membaca & memvalidasi request body JSON.
//  3. Memanggil SavingService.
//  4. Menerjemahkan hasil/error service menjadi HTTP response yang tepat.
//
// Handler TIDAK boleh berisi logika bisnis (validasi min_deposit, authorization
// kepemilikan rekening, dsb.) — semua itu ada di service layer.
package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"koperasi-digital/internal/middleware"
	"koperasi-digital/internal/model"
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
//
// Ini adalah helper privat yang menghilangkan duplikasi boilerplate di setiap handler.
// Mengembalikan (userID, true) jika berhasil, atau (0, false) jika gagal.
//
// Kegagalan di sini seharusnya tidak terjadi jika middleware.RequireAuth terpasang
// dengan benar di router, tapi kita tangani secara defensif agar bug konfigurasi
// tidak menyebabkan data user lain bocor.
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
//
// Membuka rekening simpanan baru untuk anggota yang sedang login.
//
// Request body (JSON):
//
//	{ "savings_product_id": 1 }
//
// Response sukses (201 Created):
//
//	{ "id": 5, "user_id": 12, "savings_product_id": 1, "balance": 0, "status": "active", ... }
//
// Response gagal:
//   - 400 Bad Request  : body tidak valid / savings_product_id tidak dikirim
//   - 401 Unauthorized : token JWT tidak ada atau tidak valid
//   - 404 Not Found    : produk simpanan tidak ditemukan
//   - 500 Internal     : error tak terduga di server
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
//
// Mengembalikan semua rekening simpanan milik user yang sedang login.
//
// Response sukses (200 OK):
//
//	{ "accounts": [ { "id": 5, "balance": 150000, ... }, ... ] }
//
// Catatan: jika user belum punya rekening, "accounts" berupa array kosong ([]),
// bukan null — agar client tidak perlu pengecekan null tambahan.
//
// Response gagal:
//   - 401 Unauthorized : token JWT tidak ada atau tidak valid
//   - 500 Internal     : error tak terduga di server
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
//
// Menyetor dana ke rekening simpanan milik user yang sedang login.
// Operasi ini atomik: balance terupdate DAN log transaksi tercatat,
// atau keduanya batal jika ada kegagalan di tengah jalan.
//
// Request body (JSON):
//
//	{ "account_id": 5, "amount": 100000, "reference_id": "TRF-20240416-001" }
//
// Response sukses (200 OK):
//
//	{ "message": "setoran berhasil" }
//
// Response gagal:
//   - 400 Bad Request         : body tidak valid / amount <= 0
//   - 401 Unauthorized        : token JWT tidak ada atau tidak valid
//   - 404 Not Found           : rekening tidak ditemukan (atau bukan milik user ini)
//   - 422 Unprocessable Entity: rekening tidak aktif / nominal di bawah minimum
//   - 500 Internal            : error tak terduga di server
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

	err := h.savingService.DepositFund(c.Request.Context(), userID, req)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrSavingsAccountNotFound):
			// Rekening tidak ditemukan ATAU bukan milik user ini.
			// Respons 404 (bukan 403) untuk mencegah enumerasi ID rekening.
			c.JSON(http.StatusNotFound, errorResponse{Error: "rekening simpanan tidak ditemukan"})

		case errors.Is(err, service.ErrAccountNotActive):
			// 422 Unprocessable Entity: request valid secara format, tapi tidak bisa diproses
			// karena kondisi bisnis tidak terpenuhi (rekening dibekukan/ditutup).
			c.JSON(http.StatusUnprocessableEntity, errorResponse{Error: "rekening simpanan tidak aktif"})

		case errors.Is(err, service.ErrDepositBelowMinimum):
			// Pesan error sudah menyertakan nilai minimum dari service layer,
			// sehingga client bisa menampilkannya langsung ke user.
			c.JSON(http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})

		default:
			c.JSON(http.StatusInternalServerError, errorResponse{Error: "terjadi kesalahan pada server"})
		}
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "setoran berhasil"})
}
