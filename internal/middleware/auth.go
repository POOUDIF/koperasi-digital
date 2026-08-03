// Package middleware menyediakan Gin middleware yang dapat dirangkai ke route manapun.
package middleware

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
)

// Context key yang dipakai untuk menyimpan & membaca klaim JWT di Gin context.
// Diekspor sebagai konstanta agar handler tidak perlu menulis ulang string literal —
const (
	ContextKeyUserID = "user_id" // nilai: int64
	ContextKeyEmail  = "email"   // nilai: string
)

// authClaims mencerminkan struktur payload yang dihasilkan oleh service.generateToken.
// Field harus cocok persis agar jwt.ParseWithClaims berhasil mendekode nilainya.
type authClaims struct {
	UserID int64  `json:"user_id"`
	Email  string `json:"email"`
	jwt.RegisteredClaims
}

// RequireAuth mengembalikan Gin HandlerFunc yang memvalidasi JWT pada setiap request.
// Alur kerja middleware:
func RequireAuth(jwtSecret string, rdb *redis.Client) gin.HandlerFunc {
	secret := []byte(jwtSecret)

	return func(c *gin.Context) {
		// Langkah 1: Ambil header Authorization.
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			abortUnauthorized(c, "header Authorization tidak ada")
			return
		}

		// Langkah 2: Pastikan formatnya "Bearer <token>".
		// strings.Cut lebih bersih daripada strings.Split untuk kasus dua bagian.
		scheme, tokenStr, found := strings.Cut(authHeader, " ")
		if !found || !strings.EqualFold(scheme, "Bearer") || tokenStr == "" {
			abortUnauthorized(c, "format Authorization harus 'Bearer <token>'")
			return
		}

		// Langkah 2.5: Cek token dari Redis blocklist
		// GAP-10: Token blocklist untuk otorisasi stateful
		isRevoked, err := rdb.Exists(c.Request.Context(), "jwt_revoked:"+tokenStr).Result()
		if err == nil && isRevoked > 0 {
			abortUnauthorized(c, "sesi telah diakhiri, silakan login kembali")
			return
		}

		// Langkah 3: Parse dan verifikasi token.
		// Fungsi keyFunc dipanggil oleh library untuk mendapatkan kunci verifikasi.
		claims := &authClaims{}
		token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, errors.New("algoritma signing tidak valid")
			}
			return secret, nil
		})

		if err != nil || !token.Valid {
			abortUnauthorized(c, "token tidak valid atau sudah kadaluarsa")
			return
		}

		// Langkah 4: Sematkan klaim ke Gin context.
		// Handler selanjutnya bisa membaca ini dengan c.GetInt64(ContextKeyUserID).
		c.Set(ContextKeyUserID, claims.UserID)
		c.Set(ContextKeyEmail, claims.Email)

		// Lanjutkan ke handler berikutnya dalam chain.
		c.Next()
	}
}

// RequireActiveUserDB mengembalikan Gin HandlerFunc yang memverifikasi bahwa akun user
// masih berstatus 'active' di database pada setiap request.
func RequireActiveUserDB(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Ambil user_id dari context yang sudah diisi RequireAuth.
		// Jika tidak ada, berarti middleware dipakai tanpa RequireAuth — konfigurasi salah.
		rawID, exists := c.Get(ContextKeyUserID)
		if !exists {
			abortUnauthorized(c, "sesi tidak valid, silakan login kembali")
			return
		}

		userID, ok := rawID.(int64)
		if !ok {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error": "terjadi kesalahan pada server",
			})
			return
		}

		// Query kolom status dari database.
		// Hanya SELECT satu kolom — query ini sangat ringan.
		var status string
		err := db.QueryRowContext(
			c.Request.Context(),
			`SELECT status FROM users WHERE id = $1 LIMIT 1`,
			userID,
		).Scan(&status)

		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				// Token valid tapi user sudah dihapus dari database.
				abortUnauthorized(c, "akun tidak ditemukan, silakan login kembali")
				return
			}
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error": "terjadi kesalahan pada server",
			})
			return
		}

		// Hanya status 'active' yang diizinkan mengakses endpoint protected.
		// 'inactive' dan 'banned' dikembalikan 403 Forbidden agar client tahu
		if status != "active" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": "akun tidak aktif atau diblokir, hubungi admin koperasi",
			})
			return
		}

		c.Next()
	}
}

// abortUnauthorized menghentikan request dan mengembalikan 401 dengan pesan error.
// Menggunakan c.AbortWithStatusJSON agar handler setelahnya tidak ikut dijalankan.
func abortUnauthorized(c *gin.Context, message string) {
	c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": message})
}
