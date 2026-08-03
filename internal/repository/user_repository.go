// Package repository menangani semua operasi baca/tulis ke database.
// Pola yang digunakan: Repository Interface + Concrete Implementation.
package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lib/pq"

	"koperasi-digital/internal/model"
)

// ErrUserNotFound dikembalikan saat user dengan kriteria tertentu tidak ditemukan.
// Didefinisikan di sini agar service layer bisa membandingkan error secara eksplisit
var ErrUserNotFound = errors.New("user tidak ditemukan")

// ErrEmailAlreadyExists dikembalikan saat mencoba menyimpan email yang sudah terdaftar.
var ErrEmailAlreadyExists = errors.New("email sudah terdaftar")

// pgUniqueViolation adalah kode error PostgreSQL untuk pelanggaran unique constraint.
const pgUniqueViolation = "23505"

// UserRepository mendefinisikan kontrak operasi database untuk entitas User.
// Service layer hanya boleh berkomunikasi lewat interface ini, bukan lewat
type UserRepository interface {
	// Insert menyimpan user baru ke database dan mengembalikan user dengan ID yang diisi.
	Insert(ctx context.Context, user *model.User) (*model.User, error)

	// FindByEmail mencari user berdasarkan email.
	// Mengembalikan ErrUserNotFound jika tidak ada.
	FindByEmail(ctx context.Context, email string) (*model.User, error)

	// FindByID mencari user berdasarkan primary key.
	// Mengembalikan ErrUserNotFound jika tidak ada.
	FindByID(ctx context.Context, id int64) (*model.User, error)
}

// postgresUserRepository adalah implementasi UserRepository yang menggunakan PostgreSQL.
// Huruf kecil di awal nama struct berarti ia tidak diekspor — hanya bisa diakses
type postgresUserRepository struct {
	db *sql.DB
}

// NewUserRepository membuat instance repository baru.
// Mengembalikan interface (bukan *postgresUserRepository) agar caller tidak
func NewUserRepository(db *sql.DB) UserRepository {
	return &postgresUserRepository{db: db}
}

// Insert menyimpan user baru ke tabel `users`.
// Klausa RETURNING dipakai untuk mendapatkan id, role, status, dan timestamps
func (r *postgresUserRepository) Insert(ctx context.Context, user *model.User) (*model.User, error) {
	query := `
		INSERT INTO users (nama_lengkap, email, password_hash)
		VALUES ($1, $2, $3)
		RETURNING id, role, wallet_address, status, created_at, updated_at
	`

	row := r.db.QueryRowContext(ctx, query, user.NamaLengkap, user.Email, user.PasswordHash)

	err := row.Scan(
		&user.ID, &user.Role, &user.WalletAddress, &user.Status,
		&user.CreatedAt, &user.UpdatedAt,
	)
	if err != nil {
		// Type-assert ke *pq.Error untuk memeriksa kode error PostgreSQL.
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code == pgUniqueViolation {
			return nil, ErrEmailAlreadyExists
		}
		return nil, fmt.Errorf("insert user gagal: %w", err)
	}

	return user, nil
}

// FindByID mengambil satu baris user berdasarkan primary key (id).
// Dipakai oleh GetProfile setelah middleware memverifikasi JWT — kita sudah
func (r *postgresUserRepository) FindByID(ctx context.Context, id int64) (*model.User, error) {
	query := `
		SELECT id, nama_lengkap, email, password_hash, role, wallet_address, status, created_at, updated_at
		FROM   users
		WHERE  id = $1
		LIMIT  1
	`

	user := &model.User{}
	err := r.db.QueryRowContext(ctx, query, id).Scan(
		&user.ID,
		&user.NamaLengkap,
		&user.Email,
		&user.PasswordHash,
		&user.Role,
		&user.WalletAddress,
		&user.Status,
		&user.CreatedAt,
		&user.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("query user by id gagal: %w", err)
	}

	return user, nil
}

// FindByEmail mengambil satu baris user berdasarkan kolom email.
// sql.ErrNoRows (dikembalikan saat tidak ada baris yang cocok) diterjemahkan
func (r *postgresUserRepository) FindByEmail(ctx context.Context, email string) (*model.User, error) {
	query := `
		SELECT id, nama_lengkap, email, password_hash, role, wallet_address, status, created_at, updated_at
		FROM   users
		WHERE  email = $1
		LIMIT  1
	`

	user := &model.User{}
	err := r.db.QueryRowContext(ctx, query, email).Scan(
		&user.ID,
		&user.NamaLengkap,
		&user.Email,
		&user.PasswordHash,
		&user.Role,
		&user.WalletAddress,
		&user.Status,
		&user.CreatedAt,
		&user.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("query user by email gagal: %w", err)
	}

	return user, nil
}
