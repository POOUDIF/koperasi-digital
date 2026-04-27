// Package middleware — RBAC middleware berbasis role yang tersimpan di database.
package middleware

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
)

// ContextKeyRole adalah key yang digunakan untuk menyimpan role user di Gin context
// setelah RequireRole berhasil memverifikasi akses.
const ContextKeyRole = "role"

// RequireRole mengembalikan Gin HandlerFunc yang memvalidasi role user.
//
// Middleware ini HARUS dirantai setelah RequireAuth karena bergantung pada
// ContextKeyUserID yang sudah disematkan RequireAuth ke dalam Gin context.
//
// Alur kerja:
//  1. Baca user_id dari Gin context (diset oleh RequireAuth).
//  2. Query kolom `role` dari tabel `users` berdasarkan user_id tersebut.
//  3. Periksa apakah role user ada dalam daftar allowedRoles.
//  4. Jika ya  → sematkan role ke context dan lanjutkan ke handler.
//  5. Jika tidak → hentikan request dengan 403 Forbidden.
//
// Mengapa query database per request, bukan simpan role di JWT?
// Jika role disimpan di JWT, perubahan role (misalnya pencabutan hak admin)
// tidak langsung berlaku — user masih bisa menggunakan token lama sampai expired.
// Dengan query DB, perubahan role berlaku efektif segera pada request berikutnya.
// Trade-off: satu extra query DB per request admin (dapat dikurangi dengan cache
// jika volume tinggi di iterasi berikutnya).
//
// Contoh penggunaan di router:
//
//	adminGroup := v1.Group("/admin",
//	    middleware.RequireAuth(cfg.JWTSecret),
//	    middleware.RequireRole(db, "admin", "super_admin"),
//	)
func RequireRole(db *sql.DB, allowedRoles ...string) gin.HandlerFunc {
	// Bangun set untuk lookup O(1) — lebih efisien daripada iterasi slice
	// setiap kali middleware dijalankan, terutama jika allowedRoles panjang.
	allowed := make(map[string]struct{}, len(allowedRoles))
	for _, r := range allowedRoles {
		allowed[r] = struct{}{}
	}

	return func(c *gin.Context) {
		// Langkah 1: Ambil user_id dari context yang sudah diisi RequireAuth.
		// Jika tidak ada, berarti middleware dipakai tanpa RequireAuth — konfigurasi salah.
		rawID, exists := c.Get(ContextKeyUserID)
		if !exists {
			// Ini adalah bug konfigurasi router, bukan kesalahan user.
			// Kembalikan 401 agar client tahu session-nya tidak valid.
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "sesi tidak valid, silakan login kembali",
			})
			return
		}

		userID, ok := rawID.(int64)
		if !ok {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error": "terjadi kesalahan pada server",
			})
			return
		}

		// Langkah 2: Query role dari database.
		// Kita hanya SELECT satu kolom — query ini sangat ringan dan
		// dapat di-cache di masa depan jika diperlukan.
		var role string
		err := db.QueryRowContext(
			c.Request.Context(),
			`SELECT role FROM users WHERE id = $1 LIMIT 1`,
			userID,
		).Scan(&role)

		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				// Token valid tapi user sudah dihapus dari database.
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
					"error": "akun tidak ditemukan, silakan login kembali",
				})
				return
			}
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error": "terjadi kesalahan pada server",
			})
			return
		}

		// Langkah 3: Periksa apakah role user ada dalam daftar yang diizinkan.
		if _, permitted := allowed[role]; !permitted {
			// 403 Forbidden — user terautentikasi tapi tidak punya hak akses.
			// Pesan sengaja generik agar tidak mengekspos daftar role yang valid.
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": "akses ditolak: hak akses tidak mencukupi",
			})
			return
		}

		// Langkah 4: Sematkan role ke context agar handler downstream bisa membacanya
		// tanpa perlu query database lagi (misal: untuk logging atau audit trail).
		c.Set(ContextKeyRole, role)

		c.Next()
	}
}
