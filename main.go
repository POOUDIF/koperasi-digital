// Titik masuk (entry point) aplikasi Koperasi Digital.
//
// Urutan inisialisasi:
//  1. Load konfigurasi dari environment variables (atau .env)
//  2. Buka koneksi database dengan connection pool
//  3. Inisialisasi router & daftarkan semua route
//  4. Jalankan HTTP server
package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"

	"koperasi-digital/internal/blockchain"
	"koperasi-digital/internal/config"
	"koperasi-digital/internal/database"
	"koperasi-digital/internal/handler"
	"koperasi-digital/internal/middleware"
	"koperasi-digital/internal/repository"
	"koperasi-digital/internal/service"
	"koperasi-digital/internal/worker"
)

func main() {
	// --- 1. Load .env (opsional, diabaikan jika file tidak ada) ---
	// Di production, environment variables biasanya di-inject langsung oleh
	// orchestrator (Docker, Kubernetes) sehingga file .env tidak diperlukan.
	if err := godotenv.Load(); err != nil {
		log.Println("[config] file .env tidak ditemukan, menggunakan environment variables sistem")
	}

	// --- 2. Load konfigurasi ---
	cfg := config.Load()

	// Set mode Gin sesuai environment (production mematikan debug logs).
	if cfg.AppEnv == "production" {
		gin.SetMode(gin.ReleaseMode)
	}

	// --- 3. Buka koneksi database ---
	db, err := database.New(cfg.DB)
	if err != nil {
		log.Fatalf("[db] gagal terhubung ke database: %v", err)
	}
	// Pastikan semua koneksi pool ditutup saat aplikasi berhenti.
	defer func() {
		if err := db.Close(); err != nil {
			log.Printf("[db] error saat menutup koneksi: %v", err)
		}
	}()

	// --- 4. Inisialisasi Redis client ---
	// Redis digunakan untuk dua tujuan:
	//   1. Caching harga emas (TTL 15 menit) — mengurangi beban query PostgreSQL.
	//   2. Message queue "queue:gold_mint" — trigger event-driven ke GoldWorker.
	// Jika Redis gagal terhubung, aplikasi dihentikan (Fatal) karena arsitektur
	// event-driven bergantung pada Redis untuk memproses transaksi.
	redisClient, err := database.NewRedisClient(cfg.RedisURL)
	if err != nil {
		log.Fatalf("[redis] gagal terhubung ke Redis: %v", err)
	}
	defer func() {
		if err := redisClient.Close(); err != nil {
			log.Printf("[redis] error saat menutup koneksi: %v", err)
		}
	}()

	// --- 5. Inisialisasi EVM Client (opsional) ---
	// EVM client hanya diinisialisasi jika POLYGON_RPC_URL dikonfigurasi.
	// Jika kosong, fitur blockchain dinonaktifkan dan server tetap berjalan normal.
	// Ini memungkinkan development offline tanpa memerlukan koneksi ke node Polygon.
	var evmClient *blockchain.Client
	if cfg.PolygonRPCURL != "" {
		var evmErr error
		evmClient, evmErr = blockchain.NewEVMClient(cfg.PolygonRPCURL)
		if evmErr != nil {
			// Tidak fatal — server tetap bisa melayani request off-chain.
			// Worker blockchain yang membutuhkan evmClient akan skip jika nil.
			log.Printf("[blockchain] peringatan: gagal terhubung ke node EVM: %v", evmErr)
		} else {
			defer evmClient.Close()
		}
	} else {
		log.Println("[blockchain] POLYGON_RPC_URL tidak dikonfigurasi, fitur blockchain dinonaktifkan")
	}

	// --- 6. Buat cancellable context untuk background worker ---
	// Context ini digunakan sebagai sinyal shutdown untuk semua goroutine
	// background (worker). Saat cancel() dipanggil, semua worker yang
	// listening pada ctx.Done() akan berhenti secara graceful.
	// BLPop di GoldWorker juga akan di-unblock oleh context cancellation.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// --- 7. Setup router ---
	router := setupRouter(cfg, db, redisClient, evmClient)

	// --- 8. Inisialisasi dan jalankan background worker (Event-Driven) ---
	// GoldWorker memproses transaksi emas menggunakan BLPop pada "queue:gold_mint".
	// Worker bangun hanya saat ada transaksi baru — tidak ada polling.
	// Jika OwnerPrivateKey atau GoldContractAddress kosong, worker berjalan
	// dalam mode "log-only" (tidak mengirim transaksi ke blockchain).
	goldRepo := repository.NewGoldRepository(db, redisClient)
	goldWorker := worker.NewGoldWorker(goldRepo, redisClient, evmClient, cfg.OwnerPrivateKey, cfg.GoldContractAddress)
	go goldWorker.Start(ctx)

	// --- 8. Jalankan server dengan graceful shutdown ---
	// Graceful shutdown memastikan request yang sedang diproses selesai dulu
	// sebelum server benar-benar berhenti — penting agar tidak ada transaksi
	// koperasi yang terpotong di tengah jalan.
	srv := &http.Server{
		Addr:    ":" + cfg.ServerPort,
		Handler: router,

		// Timeout diset eksplisit untuk mencegah koneksi menggantung selamanya.
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Jalankan server di goroutine terpisah agar kita bisa menunggu sinyal OS.
	go func() {
		log.Printf("[server] berjalan di http://localhost:%s (env: %s)", cfg.ServerPort, cfg.AppEnv)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("[server] error fatal: %v", err)
		}
	}()

	// Tunggu sinyal SIGINT (Ctrl+C) atau SIGTERM (dari Docker/Kubernetes).
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("[server] menerima sinyal shutdown, menyelesaikan request yang berjalan...")

	// Langkah 1: Hentikan semua background worker terlebih dahulu.
	// cancel() mengirim sinyal ke ctx.Done() sehingga GoldWorker (dan worker
	// lain di masa depan) berhenti memproses pekerjaan baru.
	cancel()
	log.Println("[server] background worker dihentikan.")

	// Langkah 2: Shutdown HTTP server.
	// Beri waktu maksimal 30 detik untuk menyelesaikan request yang sedang berjalan.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("[server] shutdown paksa: %v", err)
	}

	log.Println("[server] berhenti dengan bersih.")
}

// setupRouter mendaftarkan semua route dan middleware ke Gin engine.
// Dipisahkan dari main() agar mudah di-test secara independen.
//
// Dependency Injection dilakukan di sini: DB → Repository → Service → Handler.
// Setiap layer hanya tahu tentang layer di bawahnya melalui interface.
func setupRouter(cfg *config.Config, db *sql.DB, rdb *redis.Client, evmClient *blockchain.Client) *gin.Engine {
	router := gin.New()

	// --- CORS harus didaftarkan PERTAMA, sebelum middleware lain ---
	//
	// Mengapa penting urutan ini?
	// Browser mengirim preflight request (OPTIONS) sebelum request utama.
	// Jika CORS tidak ditangani sebelum middleware autentikasi berjalan,
	// preflight akan ditolak dengan 401 — padahal preflight tidak membawa token.
	// Akibatnya browser memblokir seluruh request dari frontend.
	//
	// Konfigurasi ini mengizinkan HANYA origin FrontendURL (dari config/.env).
	// Di production, ganti FRONTEND_URL ke domain frontend sesungguhnya.
	router.Use(cors.New(cors.Config{
		// Satu origin eksplisit — lebih aman daripada wildcard "*" yang
		// tidak kompatibel dengan AllowCredentials: true.
		AllowOrigins: []string{cfg.FrontendURL},

		// Metode HTTP yang diizinkan lintas origin.
		AllowMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},

		// Header yang boleh dikirim oleh browser dalam request.
		//   Authorization : untuk JWT Bearer token.
		//   Content-Type  : agar browser boleh kirim JSON body.
		AllowHeaders: []string{"Authorization", "Content-Type"},

		// Header response yang boleh dibaca oleh JavaScript di browser.
		ExposeHeaders: []string{"Content-Length"},

		// Wajib true agar browser menyertakan Cookie dalam cross-origin request.
		// Tanpa ini, js-cookie tidak akan bisa membaca token yang tersimpan.
		AllowCredentials: true,

		// Preflight response di-cache browser selama 12 jam.
		// Browser tidak akan mengirim OPTIONS lagi untuk kombinasi method+header
		// yang sama selama durasi ini — mengurangi jumlah request ke server.
		MaxAge: 12 * time.Hour,
	}))

	// Middleware bawaan: recovery dari panic + structured logging.
	router.Use(gin.Recovery())
	router.Use(gin.Logger())

	// --- Inisialisasi dependency tree ---
	// Layer 1: Repository — tahu tentang *sql.DB
	userRepo := repository.NewUserRepository(db)
	savingRepo := repository.NewSavingRepository(db)
	financingRepo := repository.NewFinancingRepository(db)
	goldRepo := repository.NewGoldRepository(db, rdb)

	// Layer 2: Service — tahu tentang Repository (via interface), bukan *sql.DB
	userSvc := service.NewUserService(
		userRepo,
		cfg.JWTSecret,
		cfg.JWTTokenTTL,
	)
	savingSvc := service.NewSavingService(savingRepo)
	financingSvc := service.NewFinancingService(financingRepo)
	goldSvc := service.NewGoldService(goldRepo, rdb)

	// Layer 3: Handler — tahu tentang Service (via interface), bukan Repository
	userH := handler.NewUserHandler(userSvc)
	savingH := handler.NewSavingHandler(savingSvc)
	financingH := handler.NewFinancingHandler(financingSvc)
	goldH := handler.NewGoldHandler(goldSvc)

	// --- Registrasi route ---
	// Semua endpoint dikelompokkan di bawah prefix /api/v1
	// sehingga mudah menambahkan versi API baru (/api/v2) di masa depan.
	v1 := router.Group("/api/v1")
	{
		// Health-check: tidak butuh autentikasi, dipanggil oleh load-balancer.
		healthH := handler.NewHealthHandler(db)
		v1.GET("/health", healthH.Check)

		// Endpoint publik — tidak memerlukan JWT.
		v1.POST("/register", userH.Register)
		v1.POST("/login", userH.Login)
		v1.GET("/gold/price", goldH.GetPrice) // Harga emas publik

		// Endpoint terproteksi — setiap route di grup ini wajib menyertakan
		// header "Authorization: Bearer <token>" yang valid.
		// middleware.RequireAuth memverifikasi token, lalu menyematkan user_id
		// dan email ke Gin context sebelum handler dijalankan.
		protected := v1.Group("", middleware.RequireAuth(cfg.JWTSecret))
		{
			protected.GET("/profile", userH.GetProfile)

			// --- Modul Simpanan Syariah ---
			protected.POST("/savings/accounts", savingH.OpenAccount) // Buka rekening baru
			protected.GET("/savings/accounts", savingH.GetAccounts)  // Lihat semua rekening & saldo
			protected.POST("/savings/deposit", savingH.Deposit)      // Setor tunai

			// --- Modul Pembiayaan Syariah (anggota) ---
			protected.POST("/financing/apply", financingH.Apply)                             // Ajukan pembiayaan baru
			protected.GET("/financing", financingH.GetMyFinancings)                          // Lihat daftar pembiayaan saya
			protected.GET("/financing/:id/installments", financingH.GetInstallments)         // Lihat jadwal cicilan
			protected.POST("/financing/installments/:id/pay", financingH.PayInstallment)     // Bayar satu cicilan

			// --- Modul Emas Digital ---
			protected.POST("/gold/buy", goldH.BuyGold) // Beli emas, debet saldo Wadiah
		}

		// --- Grup Admin: RequireAuth + RequireRole dirantai ---
		//
		// Semua route di bawah /admin memerlukan dua lapis validasi:
		//   1. RequireAuth  : token JWT harus valid.
		//   2. RequireRole  : role user harus salah satu dari yang diizinkan.
		//
		// Urutan middleware PENTING: RequireAuth harus lebih dulu karena
		// RequireRole membaca user_id dari context yang diisi RequireAuth.
		admin := v1.Group("/admin",
			middleware.RequireAuth(cfg.JWTSecret),
			middleware.RequireRole(db, "pengurus", "admin", "super_admin"),
		)
		{
			// Review pengajuan pembiayaan: approve atau reject.
			admin.PUT("/financing/:id/review", financingH.Review)
		}
	}

	return router
}
