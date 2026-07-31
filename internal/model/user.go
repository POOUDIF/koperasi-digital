// Package model mendefinisikan struct domain yang merepresentasikan entitas bisnis.
// Struct di sini adalah "bahasa bersama" antar layer: database, service, dan handler.
package model

import "time"

// User merepresentasikan anggota koperasi yang terdaftar di sistem.
//
// Tag `db` dipakai oleh query manual (lib/pq) untuk mapping kolom.
// Tag `json:"-"` pada PasswordHash memastikan hash TIDAK PERNAH bocor ke response API.
type User struct {
	ID           int64  `db:"id"            json:"id"`
	NamaLengkap  string `db:"nama_lengkap"  json:"nama_lengkap"`
	Email        string `db:"email"         json:"email"`
	PasswordHash string `db:"password_hash" json:"-"` // tidak pernah dikirim ke client

	// Role menentukan hak akses user di sistem RBAC.
	// Nilai: "anggota" | "pengurus" | "admin" | "super_admin"
	// Default di database: "anggota"
	Role string `db:"role" json:"role"`

	// WalletAddress adalah alamat wallet Polygon (EVM) milik anggota.
	// NULL jika anggota belum menghubungkan wallet mereka.
	// Diisi saat anggota klik "Connect Wallet" di frontend (ethers.js).
	// Format: "0x" + 40 karakter hex, contoh: "0xAbCd...1234".
	WalletAddress *string `db:"wallet_address" json:"wallet_address,omitempty"`

	// Status menentukan kondisi akun:
	//   active   : bisa login dan melakukan semua transaksi.
	//   inactive : akun belum diaktivasi atau dinonaktifkan sementara.
	//   banned   : diblokir permanen oleh admin/pengurus.
	Status string `db:"status" json:"status"`

	CreatedAt time.Time `db:"created_at" json:"created_at"`
	UpdatedAt time.Time `db:"updated_at" json:"updated_at"`
}

// RegisterRequest adalah payload JSON yang dikirim client saat mendaftar.
// Validasi dilakukan oleh Gin (tag `binding`), bukan di struct ini sendiri.
type RegisterRequest struct {
	NamaLengkap string `json:"nama_lengkap" binding:"required,min=3"`
	Email       string `json:"email"        binding:"required,email"`
	Password    string `json:"password"     binding:"required,min=8"`
}

// LoginRequest adalah payload JSON yang dikirim client saat login.
type LoginRequest struct {
	Email    string `json:"email"    binding:"required,email"`
	Password string `json:"password" binding:"required"`
}

// AuthResponse adalah respons yang dikembalikan setelah register atau login berhasil.
// Berisi data user (tanpa password) dan JWT token untuk request berikutnya.
type AuthResponse struct {
	Token string `json:"token"`
	User  User   `json:"user"`
}
