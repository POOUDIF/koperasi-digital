// Titik masuk (entry point) aplikasi Koperasi Digital.
// Urutan inisialisasi:
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// --- 7. Setup router ---
	router := setupRouter(cfg, db, redisClient, evmClient)

	// --- 8. Inisialisasi dan jalankan background worker (Event-Driven) ---
	// GoldWorker memproses transaksi emas menggunakan BLPop pada "queue:gold_mint".
	goldRepo := repository.NewGoldRepository(db, redisClient)
	// userRepo diinject ke worker agar bisa mengambil wallet_address anggota sebelum mint.
	// Menggunakan connection pool yang sama (db) — tidak ada koneksi ekstra.
	userRepo := repository.NewUserRepository(db)
	goldWorker := worker.NewGoldWorker(goldRepo, userRepo, redisClient, evmClient, cfg.OwnerPrivateKey, cfg.GoldContractAddress)

	// Recover dijalankan secara sinkron SEBELUM Start() agar transaksi yang
	// terjebak akibat crash/restart sebelumnya langsung diproses:
	goldWorker.Recover(ctx)

	go goldWorker.Start(ctx)

	// --- 8. Jalankan server dengan graceful shutdown ---
	// Graceful shutdown memastikan request yang sedang diproses selesai dulu
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
func setupRouter(cfg *config.Config, db *sql.DB, rdb *redis.Client, evmClient *blockchain.Client) *gin.Engine {
	router := gin.New()

	// --- CORS harus didaftarkan PERTAMA, sebelum middleware lain ---
	// Mengapa penting urutan ini?
	router.Use(cors.New(cors.Config{
		// Satu origin eksplisit — lebih aman daripada wildcard "*" yang
		// tidak kompatibel dengan AllowCredentials: true.
		AllowOrigins: []string{cfg.FrontendURL},

		// Metode HTTP yang diizinkan lintas origin.
		AllowMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},

		// Header yang boleh dikirim oleh browser dalam request.
		// Authorization : untuk JWT Bearer token.
		AllowHeaders: []string{"Authorization", "Content-Type"},

		// Header response yang boleh dibaca oleh JavaScript di browser.
		ExposeHeaders: []string{"Content-Length"},

		// Wajib true agar browser menyertakan Cookie dalam cross-origin request.
		// Tanpa ini, js-cookie tidak akan bisa membaca token yang tersimpan.
		AllowCredentials: true,

		// Preflight response di-cache browser selama 12 jam.
		// Browser tidak akan mengirim OPTIONS lagi untuk kombinasi method+header
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
		rdb,
	)
	savingSvc := service.NewSavingService(savingRepo)
	financingSvc := service.NewFinancingService(financingRepo, cfg.MurabahahMarginRate)
	goldSvc := service.NewGoldService(goldRepo, rdb)

	// Layer 3: Handler — tahu tentang Service (via interface), bukan Repository
	userH := handler.NewUserHandler(userSvc)
	savingH := handler.NewSavingHandler(savingSvc)
	financingH := handler.NewFinancingHandler(financingSvc)
	goldH := handler.NewGoldHandler(goldSvc)

	// --- Registrasi route ---
	// Semua endpoint dikelompokkan di bawah prefix /api/v1
	v1 := router.Group("/api/v1")
	{
		// Health-check: tidak butuh autentikasi, dipanggil oleh load-balancer.
		healthH := handler.NewHealthHandler(db)
		v1.GET("/health", healthH.Check)

		// Endpoint publik — tidak memerlukan JWT.
		// Dilindungi oleh rate limiter agar tidak bisa dispam untuk eksploitasi brute-force/BOT.
		v1.POST("/register", middleware.RateLimit(), userH.Register)
		v1.POST("/login", middleware.RateLimit(), userH.Login)
		v1.GET("/gold/price", goldH.GetPrice) // Harga emas publik

		// Endpoint terproteksi — setiap route di grup ini wajib menyertakan
		// header "Authorization: Bearer <token>" yang valid, dan akun user
		protected := v1.Group("",
			middleware.RequireAuth(cfg.JWTSecret, rdb),
			middleware.RequireActiveUserDB(db),
		)
		{
			protected.GET("/profile", userH.GetProfile)
			protected.POST("/logout", userH.Logout)

			// --- Modul Simpanan Syariah ---
			protected.POST("/savings/accounts", savingH.OpenAccount) // Buka rekening baru
			protected.GET("/savings/accounts", savingH.GetAccounts)  // Lihat semua rekening & saldo
			protected.POST("/savings/deposit", savingH.Deposit)      // Setor tunai

			// --- Modul Pembiayaan Syariah (anggota) ---
			protected.POST("/financing/apply", financingH.Apply)                         // Ajukan pembiayaan baru
			protected.GET("/financing", financingH.GetMyFinancings)                      // Lihat daftar pembiayaan saya
			protected.GET("/financing/:id/installments", financingH.GetInstallments)     // Lihat jadwal cicilan
			protected.POST("/financing/installments/:id/pay", financingH.PayInstallment) // Bayar satu cicilan

			// --- Modul Emas Digital ---
			protected.POST("/gold/buy", goldH.BuyGold)   // Beli emas, debet saldo Wadiah
			protected.POST("/gold/sell", goldH.SellGold) // Jual emas, kredit saldo Wadiah
		}

		// --- Grup Admin: RequireAuth + RequireRole dirantai ---
		// Semua route di bawah /admin memerlukan dua lapis validasi:
		admin := v1.Group("/admin",
			middleware.RequireAuth(cfg.JWTSecret, rdb),
			middleware.RequireRole(db, "pengurus", "admin", "super_admin"),
		)
		{
			// Review pengajuan pembiayaan: approve atau reject.
			admin.PUT("/financing/:id/review", financingH.Review)
		}
	}

	return router
}
