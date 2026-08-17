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

// ErrProfileNotFound dikembalikan saat profil KYC belum diisi.
var ErrProfileNotFound = errors.New("profil tidak ditemukan")

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

	// GetAll mengambil semua data user dari database.
	GetAll(ctx context.Context) ([]model.User, error)

	// UpsertProfile menyimpan atau memperbarui profil KYC user.
	UpsertProfile(ctx context.Context, profile *model.UserProfile) error

	// GetProfileByUserID mengambil data KYC user.
	GetProfileByUserID(ctx context.Context, userID int64) (*model.UserProfile, error)
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

// GetAll mengambil semua baris dari tabel users.
func (r *postgresUserRepository) GetAll(ctx context.Context) ([]model.User, error) {
	query := `
		SELECT id, nama_lengkap, email, password_hash, role, wallet_address, status, created_at, updated_at
		FROM   users
		ORDER BY created_at DESC
	`

	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query all users gagal: %w", err)
	}
	defer rows.Close()

	var users []model.User
	for rows.Next() {
		var u model.User
		if err := rows.Scan(
			&u.ID,
			&u.NamaLengkap,
			&u.Email,
			&u.PasswordHash,
			&u.Role,
			&u.WalletAddress,
			&u.Status,
			&u.CreatedAt,
			&u.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan user gagal: %w", err)
		}
		users = append(users, u)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterasi users gagal: %w", err)
	}

	return users, nil
}

// UpsertProfile melakukan insert atau update data profil pengguna (KYC).
// Memanfaatkan ON CONFLICT DO UPDATE dari PostgreSQL.
func (r *postgresUserRepository) UpsertProfile(ctx context.Context, p *model.UserProfile) error {
	query := `
		INSERT INTO user_profiles (
			user_id, nik, phone_number, address, job_title, 
			monthly_income, emergency_contact_name, emergency_contact_phone
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (user_id) DO UPDATE SET
			nik = EXCLUDED.nik,
			phone_number = EXCLUDED.phone_number,
			address = EXCLUDED.address,
			job_title = EXCLUDED.job_title,
			monthly_income = EXCLUDED.monthly_income,
			emergency_contact_name = EXCLUDED.emergency_contact_name,
			emergency_contact_phone = EXCLUDED.emergency_contact_phone,
			updated_at = NOW()
	`

	_, err := r.db.ExecContext(ctx, query,
		p.UserID, p.NIK, p.PhoneNumber, p.Address, p.JobTitle,
		p.MonthlyIncome, p.EmergencyContactName, p.EmergencyContactPhone,
	)
	if err != nil {
		return fmt.Errorf("upsert profil gagal: %w", err)
	}

	return nil
}

// GetProfileByUserID mengambil profil KYC pengguna berdasarkan user_id.
func (r *postgresUserRepository) GetProfileByUserID(ctx context.Context, userID int64) (*model.UserProfile, error) {
	query := `
		SELECT user_id, nik, phone_number, address, job_title, 
		       monthly_income, emergency_contact_name, emergency_contact_phone,
		       created_at, updated_at
		FROM   user_profiles
		WHERE  user_id = $1
		LIMIT  1
	`

	p := &model.UserProfile{}
	err := r.db.QueryRowContext(ctx, query, userID).Scan(
		&p.UserID, &p.NIK, &p.PhoneNumber, &p.Address, &p.JobTitle,
		&p.MonthlyIncome, &p.EmergencyContactName, &p.EmergencyContactPhone,
		&p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrProfileNotFound
		}
		return nil, fmt.Errorf("query profil gagal: %w", err)
	}

	return p, nil
}
