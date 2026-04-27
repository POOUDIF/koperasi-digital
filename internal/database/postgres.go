// Package database menangani inisialisasi dan manajemen connection pool ke PostgreSQL.
package database

import (
	"database/sql"
	"fmt"
	"log"
	"time"

	"koperasi-digital/internal/config"

	_ "github.com/lib/pq" // driver PostgreSQL, diregistrasikan via side-effect import
)

// New membuka koneksi ke PostgreSQL dan mengkonfigurasi connection pool-nya.
//
// Connection pool penting karena:
//   - Membuka koneksi baru itu mahal (TCP handshake + auth).
//   - Pool mendaur-ulang koneksi yang sudah ada → latency lebih rendah.
//   - Batas MaxOpenConns mencegah database kewalahan saat traffic tinggi.
//
// Fungsi ini memanggil db.Ping() untuk memastikan database benar-benar bisa dicapai
// sebelum server mulai menerima request.
func New(cfg config.DBConfig) (*sql.DB, error) {
	db, err := sql.Open("postgres", cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("membuka koneksi database gagal: %w", err)
	}

	// --- Konfigurasi Connection Pool ---

	// MaxOpenConns: total koneksi (idle + aktif) yang boleh ada bersamaan.
	// Jika semua koneksi sedang dipakai, request baru akan menunggu sampai ada yang bebas.
	db.SetMaxOpenConns(cfg.MaxOpenConns)

	// MaxIdleConns: koneksi yang tetap disimpan di pool meski tidak dipakai.
	// Harus <= MaxOpenConns. Nilai terlalu besar membuang resource; terlalu kecil
	// memaksa pool sering membuka koneksi baru.
	db.SetMaxIdleConns(cfg.MaxIdleConns)

	// ConnMaxLifetime: batas usia sebuah koneksi sejak pertama dibuka.
	// Berguna untuk mengatasi koneksi "basi" akibat firewall atau load-balancer
	// yang memutus koneksi idle di sisi infrastruktur tanpa memberi tahu aplikasi.
	db.SetConnMaxLifetime(time.Duration(cfg.ConnMaxLifetimeSec) * time.Second)

	// Verifikasi koneksi aktif sebelum server mulai.
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping ke database gagal: %w", err)
	}

	log.Printf(
		"[DB] terhubung ke %s:%s/%s — pool: max_open=%d, max_idle=%d, lifetime=%ds",
		cfg.Host, cfg.Port, cfg.Name,
		cfg.MaxOpenConns, cfg.MaxIdleConns, cfg.ConnMaxLifetimeSec,
	)

	return db, nil
}
