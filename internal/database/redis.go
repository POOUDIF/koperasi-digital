// Package database — inisialisasi dan manajemen koneksi Redis.
//
// Posisi dalam arsitektur:
//
//	main.go
//	  ├── database.New(cfg.DB)         → koneksi PostgreSQL
//	  └── database.NewRedisClient(...)  → koneksi Redis
//	          ↓
//	      GoldRepository  (caching harga emas)
//	      GoldService     (push ke message queue)
//	      GoldWorker      (consume dari message queue)
package database

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
)

// NewRedisClient membuat dan memvalidasi koneksi ke server Redis.
//
// Parameter:
//   - addr : alamat Redis dalam format "host:port", contoh "localhost:6379".
//
// Fungsi ini memanggil Ping (dengan timeout 5 detik) untuk memastikan Redis
// benar-benar dapat dijangkau sebelum aplikasi mulai menerima request.
//
// Caller wajib memanggil client.Close() saat aplikasi berhenti untuk memastikan
// semua koneksi di-release dengan bersih.
func NewRedisClient(addr string) (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{
		Addr: addr,

		// DialTimeout: batas waktu untuk membuka koneksi baru ke Redis.
		// Jika Redis tidak merespons dalam 5 detik, koneksi dianggap gagal.
		DialTimeout: 5 * time.Second,

		// ReadTimeout / WriteTimeout: batas waktu per-operasi.
		// Mencegah goroutine hang saat Redis lambat merespons.
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,

		// PoolSize: jumlah maksimum koneksi dalam pool.
		// Nilai 10 cukup untuk beban sedang; sesuaikan berdasarkan kebutuhan.
		PoolSize: 10,
	})

	// Ping memverifikasi koneksi aktif sebelum server mulai.
	// Gunakan context dengan timeout agar startup tidak hang terlalu lama.
	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(pingCtx).Err(); err != nil {
		return nil, fmt.Errorf("ping ke Redis gagal: %w", err)
	}

	log.Printf("[Redis] terhubung ke %s", addr)

	return client, nil
}
