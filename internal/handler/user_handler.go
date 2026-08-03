// Package handler berisi HTTP handler — titik masuk request dari client.
// Tanggung jawab handler:
package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"koperasi-digital/internal/middleware"
	"koperasi-digital/internal/model"
	"koperasi-digital/internal/service"
)

// UserHandler mengelompokkan dependency yang dibutuhkan oleh endpoint-endpoint user.
type UserHandler struct {
	userService service.UserService
}

// NewUserHandler membuat instance UserHandler dengan dependency diinject.
// Menerima interface (bukan struct konkret) agar mudah diganti saat testing.
func NewUserHandler(userService service.UserService) *UserHandler {
	return &UserHandler{userService: userService}
}

// errorResponse adalah struktur JSON standar untuk semua response error.
type errorResponse struct {
	Error string `json:"error"`
}

// Register menangani POST /api/v1/register.
// Request body (JSON):
func (h *UserHandler) Register(c *gin.Context) {
	var req model.RegisterRequest

	// ShouldBindJSON membaca body dan memvalidasi tag `binding`.
	// Jika validasi gagal (field kosong, format email salah, dsb.), ia mengembalikan error.
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	// Panggil service layer — handler tidak perlu tahu cara hash password atau simpan data.
	result, err := h.userService.Register(c.Request.Context(), req)
	if err != nil {
		// Periksa jenis error untuk menentukan HTTP status code yang tepat.
		if errors.Is(err, service.ErrEmailAlreadyExists) {
			c.JSON(http.StatusConflict, errorResponse{Error: err.Error()})
			return
		}
		// Error tak terduga: log di server, kembalikan pesan generik ke client.
		// JANGAN kirim detail error internal ke client — bisa bocorkan informasi sensitif.
		c.JSON(http.StatusInternalServerError, errorResponse{Error: "terjadi kesalahan pada server"})
		return
	}

	c.JSON(http.StatusCreated, result)
}

// Login menangani POST /api/v1/login.
// Request body (JSON):
func (h *UserHandler) Login(c *gin.Context) {
	var req model.LoginRequest

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	result, err := h.userService.Login(c.Request.Context(), req)
	if err != nil {
		if errors.Is(err, service.ErrInvalidCredentials) {
			// 401 Unauthorized — jangan bocorkan apakah email ada atau password yang salah.
			c.JSON(http.StatusUnauthorized, errorResponse{Error: "email atau password tidak valid"})
			return
		}
		if errors.Is(err, service.ErrAccountSuspended) {
			// 403 Forbidden — kredensial valid tapi akun dinonaktifkan/diblokir admin.
			// Berbeda dari 401: client tahu ini bukan masalah password, tapi kebijakan akun.
			c.JSON(http.StatusForbidden, errorResponse{Error: err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, errorResponse{Error: "terjadi kesalahan pada server"})
		return
	}

	c.JSON(http.StatusOK, result)
}

// GetProfile menangani GET /api/v1/profile.
// Route ini dilindungi oleh middleware.RequireAuth — request yang sampai ke sini
func (h *UserHandler) GetProfile(c *gin.Context) {
	// Ambil user_id yang sudah disematkan oleh middleware.RequireAuth.
	// c.Get mengembalikan (value any, exists bool) — kita periksa keduanya.
	rawUserID, exists := c.Get(middleware.ContextKeyUserID)
	if !exists {
		// Kondisi ini tidak seharusnya terjadi jika middleware dipasang dengan benar,
		// tapi kita tangani secara defensif agar bug konfigurasi tidak menyebabkan
		c.JSON(http.StatusUnauthorized, errorResponse{Error: "sesi tidak valid, silakan login kembali"})
		return
	}

	// Gin menyimpan nilai sebagai `any`. Kita lakukan type assertion ke int64
	// sesuai dengan tipe yang di-Set oleh middleware.
	userID, ok := rawUserID.(int64)
	if !ok {
		c.JSON(http.StatusInternalServerError, errorResponse{Error: "terjadi kesalahan pada server"})
		return
	}

	user, err := h.userService.GetProfile(c.Request.Context(), userID)
	if err != nil {
		if errors.Is(err, service.ErrUserNotFound) {
			c.JSON(http.StatusNotFound, errorResponse{Error: "akun tidak ditemukan"})
			return
		}
		if errors.Is(err, service.ErrAccountSuspended) {
			// 403 Forbidden — akun di-suspend setelah token diterbitkan.
			// Client sebaiknya menghapus token lokal dan menampilkan pesan ke user.
			c.JSON(http.StatusForbidden, errorResponse{Error: err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, errorResponse{Error: "terjadi kesalahan pada server"})
		return
	}

	// model.User sudah memiliki `json:"-"` pada PasswordHash,
	// sehingga field tersebut tidak akan muncul di response JSON.
	c.JSON(http.StatusOK, user)
}

// Logout menangani POST /api/v1/logout.
// Route ini dilindungi oleh middleware.RequireAuth.
func (h *UserHandler) Logout(c *gin.Context) {
	authHeader := c.GetHeader("Authorization")
	if authHeader == "" {
		c.JSON(http.StatusUnauthorized, errorResponse{Error: "header Authorization tidak ada"})
		return
	}

	scheme, tokenStr, found := strings.Cut(authHeader, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || tokenStr == "" {
		c.JSON(http.StatusUnauthorized, errorResponse{Error: "format Authorization harus 'Bearer <token>'"})
		return
	}

	if err := h.userService.Logout(c.Request.Context(), tokenStr); err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse{Error: "terjadi kesalahan saat memproses logout"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "logout berhasil"})
}
