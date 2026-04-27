// Package middleware menyediakan Gin middleware yang dapat dirangkai ke route manapun.
package middleware

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// Context key yang dipakai untuk menyimpan & membaca klaim JWT di Gin context.
// Diekspor sebagai konstanta agar handler tidak perlu menulis ulang string literal —
// satu titik definisi mencegah typo yang sulit di-debug.
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
//
// Alur kerja middleware:
//  1. Baca header "Authorization: Bearer <token>".
//  2. Parse dan verifikasi signature token menggunakan jwtSecret.
//  3. Periksa bahwa token belum kadaluarsa (ditangani otomatis oleh library jwt/v5).
//  4. Sematkan user_id dan email ke Gin context agar bisa dibaca handler berikutnya.
//  5. Jika ada langkah yang gagal → hentikan request dengan 401, handler tidak dijalankan.
//
// Menerima jwtSecret sebagai parameter (bukan global variable) agar middleware bisa
// di-test secara independen dengan secret berbeda.
func RequireAuth(jwtSecret string) gin.HandlerFunc {
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

		// Langkah 3: Parse dan verifikasi token.
		// Fungsi keyFunc dipanggil oleh library untuk mendapatkan kunci verifikasi.
		// Kita periksa algoritma di sini untuk mencegah "algorithm confusion attack" —
		// serangan di mana penyerang mengganti header alg menjadi "none" atau "RS256"
		// agar server menggunakan kunci yang salah.
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

// abortUnauthorized menghentikan request dan mengembalikan 401 dengan pesan error.
// Menggunakan c.AbortWithStatusJSON agar handler setelahnya tidak ikut dijalankan.
func abortUnauthorized(c *gin.Context, message string) {
	c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": message})
}
