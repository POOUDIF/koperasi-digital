// Package handler — HTTP handler untuk modul Jual Beli Emas Digital.
// Tanggung jawab GoldHandler:
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
// Mengembalikan harga emas terbaru yang ditetapkan koperasi.
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
// Anggota mengirimkan jumlah gram yang ingin dibeli dan ID rekening simpanan
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
// Anggota menjual aset emas mereka. Sistem secara atomik mengkreditkan dana rupiah
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

// GetAllTransactions menangani GET /api/v1/admin/transactions/gold.
// Mengembalikan daftar semua transaksi emas (untuk admin).
func (h *GoldHandler) GetAllTransactions(c *gin.Context) {
	txs, err := h.goldService.GetAllTransactions(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse{Error: "gagal mengambil daftar semua transaksi emas"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"transactions": txs})
}
