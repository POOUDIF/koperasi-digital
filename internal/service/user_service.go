// Package service berisi logika bisnis aplikasi.
// Layer ini berada di antara handler (HTTP) dan repository (database):
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"

	"koperasi-digital/internal/model"
	"koperasi-digital/internal/repository"
)

// Sentinel errors yang didefinisikan di service layer.
// Handler akan memeriksa error ini untuk menentukan HTTP status code yang tepat.
var (
	ErrInvalidCredentials = errors.New("email atau password salah")
	ErrEmailAlreadyExists = errors.New("email sudah terdaftar")
	ErrUserNotFound       = errors.New("user tidak ditemukan")

	// ErrAccountSuspended dikembalikan saat user mencoba login dengan akun
	// yang berstatus 'inactive' atau 'banned'.
	ErrAccountSuspended = errors.New("akun tidak aktif atau diblokir, hubungi admin koperasi")
)

// jwtClaims mendefinisikan payload yang disematkan di dalam token JWT.
// Meng-embed jwt.RegisteredClaims agar field standar (exp, iat, iss) otomatis tersedia.
type jwtClaims struct {
	UserID int64  `json:"user_id"`
	Email  string `json:"email"`
	jwt.RegisteredClaims
}

// UserService mendefinisikan kontrak logika bisnis untuk entitas User.
// Handler layer hanya bergantung pada interface ini — bukan implementasi konkret.
type UserService interface {
	// Register memvalidasi data, meng-hash password, menyimpan user, lalu mengembalikan token.
	Register(ctx context.Context, req model.RegisterRequest) (*model.AuthResponse, error)

	// Login memverifikasi kredensial dan mengembalikan token JWT jika valid.
	Login(ctx context.Context, req model.LoginRequest) (*model.AuthResponse, error)

	// GetProfile mengambil data user berdasarkan ID.
	// Dipanggil setelah middleware memverifikasi JWT dan menyematkan user_id ke context.
	GetProfile(ctx context.Context, userID int64) (*model.User, error)

	// Logout memasukkan token aktif ke dalam Redis blocklist.
	Logout(ctx context.Context, tokenStr string) error
}

// userService adalah implementasi konkret UserService.
type userService struct {
	userRepo  repository.UserRepository // dependency ke repository layer
	jwtSecret []byte                    // kunci penandatangan token JWT
	jwtTTL    time.Duration             // masa berlaku token
	rdb       *redis.Client             // redis connection
}

// NewUserService membuat instance service baru dengan dependency yang diinject.
// Menerima:
func NewUserService(
	userRepo repository.UserRepository,
	jwtSecret string,
	jwtTTL time.Duration,
	rdb *redis.Client,
) UserService {
	return &userService{
		userRepo:  userRepo,
		jwtSecret: []byte(jwtSecret),
		jwtTTL:    jwtTTL,
		rdb:       rdb,
	}
}

// Register menangani pendaftaran anggota koperasi baru.
// Urutan langkah:
func (s *userService) Register(ctx context.Context, req model.RegisterRequest) (*model.AuthResponse, error) {
	// Langkah 1: Hash password.
	// bcrypt.DefaultCost = 10. Kita naikkan ke 12 untuk resistansi brute-force
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.Password), 12)
	if err != nil {
		return nil, fmt.Errorf("hashing password gagal: %w", err)
	}

	// Langkah 2: Simpan user ke database.
	newUser := &model.User{
		NamaLengkap:  req.NamaLengkap,
		Email:        req.Email,
		PasswordHash: string(hashedPassword),
	}

	savedUser, err := s.userRepo.Insert(ctx, newUser)
	if err != nil {
		// Terjemahkan error repository ke error service agar handler tidak perlu
		// tahu detail package repository.
		if errors.Is(err, repository.ErrEmailAlreadyExists) {
			return nil, ErrEmailAlreadyExists
		}
		return nil, fmt.Errorf("menyimpan user gagal: %w", err)
	}

	// Langkah 3: Generate JWT.
	token, err := s.generateToken(savedUser)
	if err != nil {
		return nil, fmt.Errorf("generate token gagal: %w", err)
	}

	return &model.AuthResponse{
		Token: token,
		User:  *savedUser,
	}, nil
}

// Login memverifikasi kredensial anggota koperasi.
// Urutan langkah:
func (s *userService) Login(ctx context.Context, req model.LoginRequest) (*model.AuthResponse, error) {
	// Langkah 1: Cari user berdasarkan email.
	user, err := s.userRepo.FindByEmail(ctx, req.Email)
	if err != nil {
		if errors.Is(err, repository.ErrUserNotFound) {
			// Kembalikan error generik — jangan bocorkan bahwa email tidak ditemukan.
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("mencari user gagal: %w", err)
	}

	// Langkah 2: Verifikasi password dengan bcrypt.
	// bcrypt.CompareHashAndPassword mengembalikan error jika password tidak cocok.
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		return nil, ErrInvalidCredentials
	}

	// Langkah 3: Validasi status akun.
	// Pemeriksaan ini dilakukan SETELAH verifikasi password berhasil.
	if user.Status != "active" {
		return nil, ErrAccountSuspended
	}

	// Langkah 4: Generate JWT.
	token, err := s.generateToken(user)
	if err != nil {
		return nil, fmt.Errorf("generate token gagal: %w", err)
	}

	return &model.AuthResponse{
		Token: token,
		User:  *user,
	}, nil
}

// GetProfile mengambil data profil user berdasarkan userID yang sudah diverifikasi
// oleh middleware JWT.
func (s *userService) GetProfile(ctx context.Context, userID int64) (*model.User, error) {
	user, err := s.userRepo.FindByID(ctx, userID)
	if err != nil {
		if errors.Is(err, repository.ErrUserNotFound) {
			// Bisa terjadi jika akun dihapus setelah token diterbitkan.
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("mengambil profil user gagal: %w", err)
	}

	// Validasi status: jika akun di-suspend setelah token diterbitkan,
	// kembalikan error agar client tahu sesi sudah tidak valid.
	if user.Status != "active" {
		return nil, ErrAccountSuspended
	}

	return user, nil
}

// Logout menyimpan token ke dalam Redis blocklist.
// Token akan kedaluwarsa sesuai waktu JWTTTL server sehingga ruang Redis
func (s *userService) Logout(ctx context.Context, tokenStr string) error {
	key := "jwt_revoked:" + tokenStr
	err := s.rdb.SetEx(ctx, key, "revoked", s.jwtTTL).Err()
	if err != nil {
		return fmt.Errorf("gagal memblokir token pada memori: %w", err)
	}
	return nil
}

// generateToken membuat JWT yang ditandatangani dengan HMAC-SHA256 (HS256).
// Payload (claims) berisi:
func (s *userService) generateToken(user *model.User) (string, error) {
	now := time.Now()

	claims := jwtClaims{
		UserID: user.ID,
		Email:  user.Email,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.jwtTTL)),
			Issuer:    "koperasi-digital",
		},
	}

	// jwt.NewWithClaims membuat token yang belum ditandatangani.
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	// SignedString menandatangani token dan mengembalikan string JWT siap pakai.
	signedToken, err := token.SignedString(s.jwtSecret)
	if err != nil {
		return "", fmt.Errorf("menandatangani token gagal: %w", err)
	}

	return signedToken, nil
}
