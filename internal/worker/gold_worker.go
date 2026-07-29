// Package worker — background worker untuk memproses tugas asinkron.
//
// Posisi dalam arsitektur:
//
//	main.go
//	  ├── HTTP Server (gin)         ← menangani request sinkron
//	  └── GoldWorker (goroutine)    ← memproses transaksi emas secara asinkron
//	          ↓
//	      GoldRepository.FindPending()  → cari transaksi pending di PostgreSQL
//	          ↓
//	      contract.CoopGold.Mint()      → kirim transaksi mint ke Polygon
//	          ↓
//	      GoldRepository.UpdateStatusAndHash() → simpan tx_hash, status → 'processing'
//
// Worker berjalan di goroutine terpisah dengan time.Ticker dan berhenti
// secara graceful saat context di-cancel (SIGINT/SIGTERM dari main.go).
package worker

import (
	"context"
	"log"
	"math"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"

	"koperasi-digital/internal/blockchain"
	"koperasi-digital/internal/blockchain/contract"
	"koperasi-digital/internal/repository"
)

// goldDecimals adalah jumlah decimal yang digunakan Smart Contract CoopGold.
// 1 gram emas = 10^4 = 10_000 unit on-chain.
// Konsisten dengan DECIMAL(10,4) di PostgreSQL dan decimals() di Solidity.
const goldDecimals = 4

// GoldWorker adalah background worker yang secara periodik memeriksa
// transaksi emas berstatus 'pending' dan mengirimnya ke Smart Contract.
//
// Alur per-transaksi:
//  1. Query transaksi pending dari PostgreSQL.
//  2. Konversi gram_amount → uint256 (× 10^4).
//  3. Panggil CoopGold.Mint(to, amount, goldTxID) di Polygon.
//  4. Simpan tx_hash dan update status → 'processing'.
type GoldWorker struct {
	goldRepo        repository.GoldRepository
	evmClient       *blockchain.Client     // nil jika blockchain tidak dikonfigurasi
	auth            *bind.TransactOpts     // nil jika private key tidak dikonfigurasi
	contractAddress common.Address         // alamat Smart Contract CoopGold
	blockchainReady bool                   // true jika semua dependensi blockchain tersedia
	interval        time.Duration
}

// NewGoldWorker membuat instance GoldWorker baru.
//
// Parameter:
//   - goldRepo         : repository untuk query dan update transaksi di database.
//   - evmClient        : klien blockchain (boleh nil — worker akan skip operasi on-chain).
//   - ownerPrivateKey  : hex-encoded private key owner Smart Contract (boleh kosong).
//   - contractAddr     : alamat kontrak CoopGold (boleh kosong).
//   - interval         : jeda antar-tick (default: 5 detik).
//
// Jika evmClient, ownerPrivateKey, atau contractAddr tidak tersedia,
// worker akan berjalan dalam mode "log-only" (tidak mengirim transaksi).
func NewGoldWorker(
	goldRepo repository.GoldRepository,
	evmClient *blockchain.Client,
	ownerPrivateKey string,
	contractAddr string,
	interval time.Duration,
) *GoldWorker {
	w := &GoldWorker{
		goldRepo: goldRepo,
		interval: interval,
	}

	// Validasi semua dependensi blockchain tersedia.
	// Jika salah satu tidak ada, worker berjalan dalam mode log-only.
	if evmClient == nil || ownerPrivateKey == "" || contractAddr == "" {
		log.Println("[gold-worker] mode log-only — dependensi blockchain belum lengkap")
		return w
	}

	// Parse alamat kontrak.
	parsedAddr, err := blockchain.ParseContractAddress(contractAddr)
	if err != nil {
		log.Printf("[gold-worker] alamat kontrak tidak valid: %v — mode log-only", err)
		return w
	}

	// Buat TransactOpts (Tx signer).
	auth, err := evmClient.NewTransactOpts(ownerPrivateKey)
	if err != nil {
		log.Printf("[gold-worker] gagal membuat TransactOpts: %v — mode log-only", err)
		return w
	}

	w.evmClient = evmClient
	w.auth = auth
	w.contractAddress = parsedAddr
	w.blockchainReady = true

	log.Printf("[gold-worker] blockchain dikonfigurasi — kontrak: %s", parsedAddr.Hex())

	return w
}

// Start menjalankan loop utama worker di dalam goroutine pemanggil.
//
// Panggil dengan: go goldWorker.Start(ctx)
//
// Loop berjalan setiap interval (default 5 detik):
//  1. Query transaksi emas berstatus 'pending' via GoldRepository.
//  2. Kirim transaksi Mint ke Smart Contract (atau log-only jika blockchain tidak tersedia).
//  3. Berhenti otomatis saat ctx di-cancel (graceful shutdown).
func (w *GoldWorker) Start(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	if w.blockchainReady {
		log.Printf("[gold-worker] dimulai — interval: %s, mode: blockchain", w.interval)
	} else {
		log.Printf("[gold-worker] dimulai — interval: %s, mode: log-only", w.interval)
	}

	for {
		select {
		case <-ctx.Done():
			log.Println("[gold-worker] menerima sinyal shutdown, berhenti.")
			return

		case <-ticker.C:
			w.processPendingTransactions(ctx)
		}
	}
}

// processPendingTransactions mengambil dan memproses semua transaksi pending.
//
// Dipisahkan dari Start() agar:
//   - Mudah di-test secara independen.
//   - Panic di satu tick tidak menghentikan worker (recover di sini).
//   - Logika bisa diperluas tanpa mengubah struktur loop utama.
func (w *GoldWorker) processPendingTransactions(ctx context.Context) {
	// Recover dari panic agar worker tidak crash — log error dan lanjut ke tick berikutnya.
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[gold-worker] PANIC tertangkap: %v", r)
		}
	}()

	txs, err := w.goldRepo.FindPending(ctx)
	if err != nil {
		log.Printf("[gold-worker] error saat query transaksi pending: %v", err)
		return
	}

	if len(txs) == 0 {
		return
	}

	log.Printf("[gold-worker] ditemukan %d transaksi pending", len(txs))

	for _, tx := range txs {
		log.Printf("[gold-worker] memproses transaksi ID=%d | user=%d | %.4f gram | Rp%.2f",
			tx.ID, tx.UserID, tx.GramAmount, tx.TotalRupiah)

		if !w.blockchainReady {
			log.Printf("[gold-worker] [log-only] skip transaksi ID=%d — blockchain belum dikonfigurasi", tx.ID)
			continue
		}

		// --- Kirim transaksi Mint ke Smart Contract ---
		w.mintOnChain(ctx, tx.ID, tx.GramAmount)
	}
}

// mintOnChain mengirim satu transaksi Mint ke Smart Contract CoopGold.
//
// Alur:
//  1. Konversi gram_amount (float64) → big.Int dengan presisi 4 desimal.
//  2. Buat instance binding CoopGold.
//  3. Panggil Mint(auth, recipientAddr, amount, goldTxID).
//  4. Ambil tx_hash dan update database.
//
// Catatan: recipientAddr sementara menggunakan address owner karena model User
// belum memiliki kolom wallet_address. Akan diubah di tahap berikutnya.
func (w *GoldWorker) mintOnChain(ctx context.Context, goldTxID int64, gramAmount float64) {
	// --- 1. Konversi gram → uint256 ---
	// 0.5 gram × 10^4 = 5_000 unit on-chain.
	// math.Round memastikan tidak ada floating-point error saat konversi.
	unitAmount := int64(math.Round(gramAmount * math.Pow10(goldDecimals)))
	amount := big.NewInt(unitAmount)

	// --- 2. Buat instance binding kontrak ---
	coopGold, err := contract.NewCoopGold(w.contractAddress, w.evmClient.Underlying())
	if err != nil {
		log.Printf("[gold-worker] gagal membuat binding kontrak: %v", err)
		return
	}

	// --- 3. Tentukan alamat penerima ---
	// TODO: Ganti dengan wallet_address anggota dari tabel users.
	// Sementara, token dikirim ke address owner sebagai placeholder.
	recipientAddr := w.auth.From

	// --- 4. Panggil Mint ---
	chainTx, err := coopGold.Mint(w.auth, recipientAddr, amount, big.NewInt(goldTxID))
	if err != nil {
		log.Printf("[gold-worker] GAGAL mint transaksi ID=%d: %v", goldTxID, err)
		// Update status ke 'failed' tanpa tx_hash.
		if updateErr := w.goldRepo.UpdateStatusAndHash(ctx, goldTxID, "failed", ""); updateErr != nil {
			log.Printf("[gold-worker] gagal update status failed ID=%d: %v", goldTxID, updateErr)
		}
		return
	}

	// --- 5. Simpan tx_hash dan update status → 'processing' ---
	txHash := chainTx.Hash().Hex()
	if err := w.goldRepo.UpdateStatusAndHash(ctx, goldTxID, "processing", txHash); err != nil {
		log.Printf("[gold-worker] gagal update status processing ID=%d: %v", goldTxID, err)
		return
	}

	log.Printf("[gold-worker] ✓ transaksi ID=%d berhasil dikirim — tx_hash: %s | amount: %d unit",
		goldTxID, txHash, unitAmount)
}
