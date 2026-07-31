// Package config menangani semua konfigurasi aplikasi yang dibaca dari environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config menyimpan seluruh konfigurasi aplikasi secara strongly-typed.
// Nilai dibaca dari environment variables (atau file .env via godotenv).
type Config struct {
	// Server
	ServerPort string // Port HTTP server, contoh: "8080"
	AppEnv     string // Environment: "development", "production"

	// JWT
	JWTSecret   string        // Kunci rahasia untuk sign token, WAJIB diisi
	JWTTokenTTL time.Duration // Masa berlaku token, default 24 jam

	// Database
	DB DBConfig

	// Margin Pembiayaan
	// Nilai desimal yang mengontrol rasio keuntungan Murabahah terhadap principal
	// Default: 0.10 (10%)
	MurabahahMarginRate float64

	// Frontend
	// FrontendURL adalah origin yang diizinkan oleh middleware CORS.
	// Development : http://localhost:3000
	// Production  : https://koperasi.id  (atau domain frontend production)
	FrontendURL string

	// Redis
	// RedisURL adalah alamat server Redis dalam format "host:port".
	// Digunakan untuk caching harga emas dan message queue transaksi.
	// Default: "localhost:6379"
	RedisURL string

	// Blockchain
	// PolygonRPCURL adalah URL endpoint JSON-RPC node Polygon.
	// Contoh:
	//   Amoy Testnet  : https://rpc-amoy.polygon.technology
	//   Polygon Mainnet: https://polygon-rpc.com
	//   Alchemy       : https://polygon-mainnet.g.alchemy.com/v2/<API_KEY>
	// Jika kosong, EVM client tidak diinisialisasi (fitur blockchain dinonaktifkan).
	PolygonRPCURL string

	// OwnerPrivateKey adalah hex-encoded private key wallet owner Smart Contract.
	// Digunakan oleh GoldWorker untuk menandatangani transaksi mint/burn on-chain.
	// Format: dengan atau tanpa prefix "0x", contoh: "0xabc123..." atau "abc123..."
	// Jika kosong, GoldWorker akan skip operasi blockchain (hanya log).
	//
	// KEAMANAN: Jangan pernah commit nilai ini ke version control.
	// Gunakan environment variable atau secret manager di production.
	OwnerPrivateKey string

	// GoldContractAddress adalah alamat Smart Contract CoopGold di Polygon.
	// Format: "0x" + 40 karakter hexadecimal.
	// Contoh: "0x1234567890abcdef1234567890abcdef12345678"
	// Jika kosong, GoldWorker akan skip operasi blockchain.
	GoldContractAddress string
}

// DBConfig menyimpan parameter koneksi PostgreSQL beserta pengaturan connection pool.
type DBConfig struct {
	Host     string
	Port     string
	User     string
	Password string
	Name     string
	SSLMode  string

	// Connection Pool — mengontrol jumlah koneksi yang dibuka ke database.
	// MaxOpenConns : batas atas koneksi aktif (0 = tidak terbatas, hindari ini di prod).
	// MaxIdleConns : koneksi yang tetap "hidup" dalam pool meski sedang tidak dipakai.
	// ConnMaxLifetimeSec : berapa detik sebuah koneksi boleh hidup sebelum dibuang & diganti.
	MaxOpenConns       int
	MaxIdleConns       int
	ConnMaxLifetimeSec int
}

// DSN (Data Source Name) mengembalikan connection string PostgreSQL yang siap dipakai oleh sql.Open.
func (d DBConfig) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		d.Host, d.Port, d.User, d.Password, d.Name, d.SSLMode,
	)
}

// Load membaca environment variables dan mengembalikan Config yang sudah terisi.
// Gunakan helper getEnv agar nilai default bisa didefinisikan inline.
func Load() *Config {
	return &Config{
		ServerPort: getEnv("SERVER_PORT", "8080"),
		AppEnv:     getEnv("APP_ENV", "development"),

		// JWT_SECRET harus diisi di production — gunakan string acak minimal 32 karakter.
		// Contoh generate: openssl rand -base64 32
		JWTSecret:   getEnv("JWT_SECRET", "ganti-dengan-secret-yang-kuat-di-production"),
		JWTTokenTTL: time.Duration(getEnvInt("JWT_TOKEN_TTL_HOURS", 24)) * time.Hour,

		FrontendURL:   getEnv("FRONTEND_URL", "http://localhost:3000"),
		RedisURL:      getEnv("REDIS_URL", "localhost:6379"),
		PolygonRPCURL: getEnv("POLYGON_RPC_URL", ""),

		MurabahahMarginRate: getEnvFloat("MURABAHAH_MARGIN_RATE", 0.10),

		// Blockchain — Smart Contract
		OwnerPrivateKey:     getEnv("OWNER_PRIVATE_KEY", ""),
		GoldContractAddress: getEnv("GOLD_CONTRACT_ADDRESS", ""),

		DB: DBConfig{
			Host:     getEnv("DB_HOST", "localhost"),
			Port:     getEnv("DB_PORT", "5432"),
			User:     getEnv("DB_USER", "postgres"),
			Password: getEnv("DB_PASSWORD", ""),
			Name:     getEnv("DB_NAME", "koperasi_digital"),
			SSLMode:  getEnv("DB_SSLMODE", "disable"),

			// Nilai default pool sudah cukup untuk beban sedang.
			// Sesuaikan berdasarkan spesifikasi server database Anda.
			MaxOpenConns:       getEnvInt("DB_MAX_OPEN_CONNS", 25),
			MaxIdleConns:       getEnvInt("DB_MAX_IDLE_CONNS", 10),
			ConnMaxLifetimeSec: getEnvInt("DB_CONN_MAX_LIFETIME_SEC", 300), // 5 menit
		},
	}
}

// --- helpers ---

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func getEnvFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return fallback
}
