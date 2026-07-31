// Package handler — HTTP handler untuk modul Jual Beli Emas Digital.
//
// Tanggung jawab GoldHandler:
//  1. Ekstrak user_id dari Gin context (diisi middleware JWT).
//  2. Baca & validasi request body JSON.
//  3. Delegasikan ke GoldService.
//  4. Terjemahkan hasil/error menjadi HTTP response yang tepat.
package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"koperasi-digital/internal/model"
	"koperasi-digital/internal/service"
)

// GoldHandler mengelompokkan dependency untuk endpoint-endpoint emas.
type GoldHandler struct {
	goldService service.GoldService
}

// NewGoldHandler membuat instance GoldHandler dengan dependency diinject.
func NewGoldHandler(goldService service.GoldService) *GoldHandler {
	return &GoldHandler{goldService: goldService}
}

// GetPrice menangani GET /api/v1/gold/price.
//
// Mengembalikan harga emas terbaru yang ditetapkan koperasi.
// Endpoint ini tidak memerlukan autentikasi — harga emas bersifat publik.
//
// Response sukses (200 OK):
//
//	{
//	  "id": 1,
//	  "buy_price_per_gram": 1698000,
//	  "sell_price_per_gram": 1672000,
//	  "updated_at": "2026-04-16T00:00:00Z"
//	}
//
// Response gagal:
//   - 503 Service Unavailable : belum ada data harga (migration belum dijalankan / admin belum isi)
//   - 500 Internal            : error tak terduga di server
func (h *GoldHandler) GetPrice(c *gin.Context) {
	price, err := h.goldService.GetCurrentPrice(c.Request.Context())
	if err != nil {
		if errors.Is(err, service.ErrGoldPriceNotAvailable) {
			// 503 lebih tepat dari 404 — resource "harga" memang seharusnya ada,
			// tapi layanan belum siap karena data harga belum diisi admin.
			c.JSON(http.StatusServiceUnavailable, errorResponse{Error: err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, errorResponse{Error: "terjadi kesalahan pada server"})
		return
	}

	c.JSON(http.StatusOK, price)
}

// BuyGold menangani POST /api/v1/gold/buy.
//
// Anggota mengirimkan jumlah gram yang ingin dibeli dan ID rekening simpanan
// Wadiah yang akan didebet. Sistem secara atomik memotong saldo dan mencatat
// transaksi emas dengan status 'pending'.
//
// Status 'pending' berarti transaksi berhasil dicatat off-chain — blockchain
// worker akan mengambilnya dan mengirimkannya ke Smart Contract Polygon secara
// asinkron (di iterasi berikutnya).
//
// Request body (JSON):
//
//	{
//	  "gram_amount": 0.5,
//	  "savings_account_id": 3
//	}
//
// Response sukses (201 Created):
//
//	{
//	  "id": 42,
//	  "user_id": 7,
//	  "type": "buy",
//	  "gram_amount": 0.5,
//	  "price_per_gram": 1698000,
//	  "total_rupiah": 849000,
//	  "status": "pending",
//	  "created_at": "2026-04-16T10:30:00Z"
//	}
//
// Response gagal:
//   - 400 Bad Request            : body tidak valid / field wajib kosong
//   - 401 Unauthorized           : token JWT tidak ada atau tidak valid
//   - 404 Not Found              : rekening simpanan tidak ditemukan atau bukan milik user
//   - 422 Unprocessable Entity   : saldo tidak cukup / rekening tidak aktif
//   - 503 Service Unavailable    : harga emas belum dikonfigurasi admin
//   - 500 Internal               : error tak terduga di server
func (h *GoldHandler) BuyGold(c *gin.Context) {
	userID, ok := extractUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, errorResponse{Error: "sesi tidak valid, silakan login kembali"})
		return
	}

	var req model.BuyGoldRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	goldTx, err := h.goldService.BuyGold(c.Request.Context(), userID, req)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrGoldPriceNotAvailable):
			c.JSON(http.StatusServiceUnavailable, errorResponse{Error: err.Error()})

		case errors.Is(err, service.ErrSavingsAccountNotFound):
			c.JSON(http.StatusNotFound, errorResponse{Error: "rekening simpanan tidak ditemukan"})

		case errors.Is(err, service.ErrAccountNotActive):
			c.JSON(http.StatusUnprocessableEntity, errorResponse{Error: "rekening simpanan tidak aktif"})

		case errors.Is(err, service.ErrInsufficientBalance):
			c.JSON(http.StatusUnprocessableEntity, errorResponse{Error: "saldo rekening tidak mencukupi untuk pembelian emas ini"})

		default:
			c.JSON(http.StatusInternalServerError, errorResponse{Error: "terjadi kesalahan pada server"})
		}
		return
	}

	// 201 Created — transaksi emas berhasil dicatat, menunggu konfirmasi blockchain.
	c.JSON(http.StatusCreated, goldTx)
}

// SellGold menangani POST /api/v1/gold/sell.
//
// Anggota menjual aset emas mereka. Sistem secara atomik mengkreditkan dana rupiah
// ke dalam rekening Wadiah, sekaligus merekam histori transaksi.
//
// Request body (JSON):
//
//	{
//	  "gram_amount": 0.5,
//	  "savings_account_id": 3
//	}
func (h *GoldHandler) SellGold(c *gin.Context) {
	userID, ok := extractUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, errorResponse{Error: "sesi tidak valid, silakan login kembali"})
		return
	}

	var req model.SellGoldRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	goldTx, err := h.goldService.SellGold(c.Request.Context(), userID, req)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrExceedsTransactionLimit):
			c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		case errors.Is(err, service.ErrGoldPriceNotAvailable):
			c.JSON(http.StatusServiceUnavailable, errorResponse{Error: err.Error()})
		case errors.Is(err, service.ErrSavingsAccountNotFound):
			c.JSON(http.StatusNotFound, errorResponse{Error: "rekening simpanan tidak ditemukan atau bukan milik Anda"})
		case errors.Is(err, service.ErrAccountNotActive):
			c.JSON(http.StatusUnprocessableEntity, errorResponse{Error: "rekening simpanan tidak aktif"})
		default:
			c.JSON(http.StatusInternalServerError, errorResponse{Error: "terjadi kesalahan internal saat menjual emas"})
		}
		return
	}

	c.JSON(http.StatusCreated, goldTx)
}
