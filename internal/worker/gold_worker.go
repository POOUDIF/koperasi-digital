// Package worker — background worker untuk memproses tugas asinkron.
//
// Posisi dalam arsitektur (Event-Driven):
//
//	GoldService.BuyGold()
//	  └── RPush(queue:gold_mint, txID)   ← trigger setelah DB commit
//	          ↓  (Redis queue)
//	  GoldWorker.Start()
//	    └── BLPop(queue:gold_mint)       ← blocking, bangun saat ada ID masuk
//	          ↓
//	      GoldRepository.FindByID(txID)  ← ambil detail transaksi dari PostgreSQL
//	          ↓
//	      contract.CoopGold.Mint()       ← kirim transaksi mint ke Polygon
//	          ↓
//	      GoldRepository.UpdateStatusAndHash() → simpan tx_hash, status → 'processing'
//
// Keunggulan dibanding polling (time.Ticker):
//   - Zero latency: worker memproses transaksi sesaat setelah BuyGold berhasil.
//   - Zero wasted cycles: BLPop hanya membangunkan worker saat ada pekerjaan.
//   - Graceful shutdown: ctx cancellation men-unblock BLPop secara otomatis.
//
// Worker berjalan di goroutine terpisah dan berhenti secara graceful
// saat context di-cancel (SIGINT/SIGTERM dari main.go).
package worker

import (
	"context"
	"errors"
	"log"
	"math"
	"math/big"
	"strconv"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/redis/go-redis/v9"

	"koperasi-digital/internal/blockchain"
	"koperasi-digital/internal/blockchain/contract"
	"koperasi-digital/internal/model"
	"koperasi-digital/internal/repository"
)

// goldDecimals adalah jumlah decimal yang digunakan Smart Contract CoopGold.
// 1 gram emas = 10^4 = 10_000 unit on-chain.
// Konsisten dengan DECIMAL(10,4) di PostgreSQL dan decimals() di Solidity.
const goldDecimals = 4

// goldMintQueueKey adalah Redis key antrian yang dikonsumsi oleh worker.
// Harus identik dengan key yang digunakan GoldService saat RPush.
const goldMintQueueKey = "queue:gold_mint"

// GoldWorker adalah background worker event-driven yang memproses transaksi
// emas dari Redis queue menggunakan BLPop (Blocking Left Pop).
//
// Worker bangun hanya saat ada ID transaksi masuk ke queue — tidak ada polling.
// Saat menerima ID, worker:
//  1. Ambil detail transaksi dari PostgreSQL via FindByID.
//  2. Kirim transaksi Mint ke Smart Contract CoopGold di Polygon.
//  3. Update status dan tx_hash di PostgreSQL.
type GoldWorker struct {
	goldRepo        repository.GoldRepository
	userRepo        repository.UserRepository // untuk mengambil wallet_address anggota sebelum mint
	rdb             *redis.Client             // antrian transaksi — BLPop pada queue:gold_mint
	evmClient       *blockchain.Client        // nil jika blockchain tidak dikonfigurasi
	auth            *bind.TransactOpts        // nil jika private key tidak dikonfigurasi
	contractAddress common.Address            // alamat Smart Contract CoopGold
	blockchainReady bool                      // true jika semua dependensi blockchain tersedia
}

// NewGoldWorker membuat instance GoldWorker baru.
//
// Parameter:
//   - goldRepo        : repository untuk query dan update transaksi di database.
//   - rdb             : Redis client untuk BLPop pada queue:gold_mint (wajib).
//   - evmClient       : klien blockchain (boleh nil — worker akan skip operasi on-chain).
//   - ownerPrivateKey : hex-encoded private key owner Smart Contract (boleh kosong).
//   - contractAddr    : alamat kontrak CoopGold (boleh kosong).
//
// Jika evmClient, ownerPrivateKey, atau contractAddr tidak tersedia,
// worker akan berjalan dalam mode "log-only" (tidak mengirim transaksi on-chain),
// namun tetap mengkonsumsi antrian Redis agar queue tidak menumpuk.
func NewGoldWorker(
	goldRepo repository.GoldRepository,
	userRepo repository.UserRepository,
	rdb *redis.Client,
	evmClient *blockchain.Client,
	ownerPrivateKey string,
	contractAddr string,
) *GoldWorker {
	w := &GoldWorker{
		goldRepo: goldRepo,
		userRepo: userRepo,
		rdb:      rdb,
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
// Loop berjalan selamanya menggunakan BLPop (blocking) pada "queue:gold_mint":
//   - Timeout 0  : BLPop menunggu tanpa batas sampai ada data masuk.
//   - ctx cancel : BLPop otomatis berhenti saat context di-cancel (graceful shutdown).
//   - Setiap ID  : fetch detail dari PostgreSQL → proses → update status.
//
// Tidak ada time.Ticker — zero polling, zero wasted CPU cycles.
func (w *GoldWorker) Start(ctx context.Context) {
	if w.blockchainReady {
		log.Println("[gold-worker] dimulai — mode: event-driven (BLPop), blockchain: aktif")
	} else {
		log.Println("[gold-worker] dimulai — mode: event-driven (BLPop), blockchain: log-only")
	}

	for {
		// BLPop dengan timeout 0 = blocking tanpa batas sampai ada data.
		// go-redis/v9 menghormati context: saat ctx di-cancel (Ctrl+C / SIGTERM),
		// BLPop akan return dengan error context.Canceled — graceful shutdown berfungsi.
		//
		// result format: []string{keyName, value}
		// result[0] = "queue:gold_mint"
		// result[1] = strconv.FormatInt(txID, 10)
		result, err := w.rdb.BLPop(ctx, 0, goldMintQueueKey).Result()

		if err != nil {
			// Jika error disebabkan oleh context cancellation (ctx di-cancel saat
			// shutdown), berhenti dengan bersih tanpa log error yang menakutkan.
			if isContextError(err) {
				log.Println("[gold-worker] menerima sinyal shutdown, berhenti.")
				return
			}
			// Error jaringan Redis atau koneksi terputus — log dan coba lagi.
			// Worker tidak berhenti agar bisa reconnect saat Redis kembali online.
			log.Printf("[gold-worker] BLPop error: %v — mencoba kembali...", err)
			continue
		}

		// Validasi format hasil BLPop (seharusnya selalu 2 elemen).
		if len(result) < 2 {
			log.Printf("[gold-worker] BLPop hasil tidak valid: %v", result)
			continue
		}

		// Parse ID transaksi dari string ke int64.
		txID, parseErr := strconv.ParseInt(result[1], 10, 64)
		if parseErr != nil {
			log.Printf("[gold-worker] gagal parse ID transaksi '%s': %v", result[1], parseErr)
			continue
		}

		log.Printf("[gold-worker] menerima ID transaksi dari queue: %d", txID)

		// Proses transaksi dalam fungsi terpisah dengan recovery dari panic.
		w.processTransaction(ctx, txID)
	}
}

// Recover menjalankan startup recovery untuk dua jenis transaksi yang terjebak:
//
//  1. Status 'pending' — transaksi sudah tersimpan di DB tapi belum sempat masuk
//     ke Redis queue karena server crash sebelum/saat RPush:
//     → RPush ulang ke "queue:gold_mint" agar BLPop loop memprosesnya.
//
//  2. Status 'processing' dengan tx_hash — transaksi sudah dikirim ke blockchain
//     tapi goroutine awaitReceipt terbunuh saat server shutdown/crash:
//     → Ambil transaksi dari chain via tx_hash → launch awaitReceipt ulang.
//
// Panggil SEBELUM go goldWorker.Start(ctx) di main.go.
// Berjalan secara sinkron — selesai sebelum Start() dipanggil.
func (w *GoldWorker) Recover(ctx context.Context) {
	log.Println("[gold-worker] memulai startup recovery...")

	w.recoverPending(ctx)
	w.recoverProcessing(ctx)

	log.Println("[gold-worker] startup recovery selesai.")
}

// recoverPending mengambil semua transaksi 'pending' dari DB dan me-RPush
// ID-nya ke Redis queue agar BLPop loop segera memprosesnya.
//
// Idempoten: jika ID sudah ada di queue (redundant push), worker akan skip
// via guard "status != pending" di processTransaction.
func (w *GoldWorker) recoverPending(ctx context.Context) {
	pendingTxs, err := w.goldRepo.FindPending(ctx)
	if err != nil {
		log.Printf("[gold-worker] recovery: gagal query transaksi pending: %v", err)
		return
	}

	if len(pendingTxs) == 0 {
		log.Println("[gold-worker] recovery: tidak ada transaksi pending.")
		return
	}

	log.Printf("[gold-worker] recovery: menemukan %d transaksi pending, me-requeue...", len(pendingTxs))

	for _, tx := range pendingTxs {
		if pushErr := w.rdb.RPush(ctx, goldMintQueueKey, tx.ID).Err(); pushErr != nil {
			log.Printf("[gold-worker] recovery: gagal requeue transaksi ID=%d: %v", tx.ID, pushErr)
			continue
		}
		log.Printf("[gold-worker] recovery: transaksi ID=%d di-requeue ke '%s'", tx.ID, goldMintQueueKey)
	}
}

// recoverProcessing mengambil semua transaksi 'processing' dari DB,
// mengambil objek transaksi dari blockchain via tx_hash, lalu melanjutkan
// goroutine awaitReceipt yang terbunuh saat server sebelumnya mati.
//
// Jika blockchain tidak tersedia (evmClient nil atau blockchainReady false),
// recovery ini dilewati — transaksi tetap 'processing' sampai operator
// melakukan intervensi manual.
func (w *GoldWorker) recoverProcessing(ctx context.Context) {
	if !w.blockchainReady {
		log.Println("[gold-worker] recovery: blockchain tidak aktif — skip recovery transaksi processing")
		return
	}

	processingTxs, err := w.goldRepo.FindProcessing(ctx)
	if err != nil {
		log.Printf("[gold-worker] recovery: gagal query transaksi processing: %v", err)
		return
	}

	if len(processingTxs) == 0 {
		log.Println("[gold-worker] recovery: tidak ada transaksi processing.")
		return
	}

	log.Printf("[gold-worker] recovery: menemukan %d transaksi processing, melanjutkan awaitReceipt...", len(processingTxs))

	for _, tx := range processingTxs {
		if tx.TxHash == nil || *tx.TxHash == "" {
			log.Printf("[gold-worker] recovery: transaksi ID=%d tidak memiliki tx_hash, dilewati", tx.ID)
			continue
		}

		txHash := common.HexToHash(*tx.TxHash)

		// Ambil objek transaksi dari blockchain menggunakan tx_hash yang tersimpan di DB.
		chainTx, _, err := w.evmClient.Underlying().TransactionByHash(ctx, txHash)
		if err != nil {
			log.Printf("[gold-worker] recovery: gagal ambil tx dari chain untuk ID=%d (hash=%s): %v",
				tx.ID, *tx.TxHash, err)
			continue
		}

		// Buat binding kontrak untuk resume awaitReceipt.
		coopGold, err := contract.NewCoopGold(w.contractAddress, w.evmClient.Underlying())
		if err != nil {
			log.Printf("[gold-worker] recovery: gagal buat binding kontrak untuk ID=%d: %v", tx.ID, err)
			continue
		}

		log.Printf("[gold-worker] recovery: resume awaitReceipt untuk ID=%d (hash=%s)", tx.ID, *tx.TxHash)

		// Capture tx.ID untuk goroutine closure.
		goldTxID := tx.ID
		go w.awaitReceipt(ctx, goldTxID, coopGold, chainTx)
	}
}

// processTransaction mengambil dan memproses satu transaksi emas berdasarkan ID.
//
// Dipisahkan dari Start() agar:
//   - Panic di satu transaksi tidak menghentikan seluruh worker loop.
//   - Logika bisa diperluas (misalnya: retry, dead letter queue) tanpa
//     mengubah struktur loop utama.
func (w *GoldWorker) processTransaction(ctx context.Context, txID int64) {
	// Recover dari panic agar worker tidak crash dan loop tetap berjalan.
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[gold-worker] PANIC tertangkap saat memproses ID=%d: %v", txID, r)
		}
	}()

	// Ambil detail transaksi dari PostgreSQL berdasarkan ID.
	tx, err := w.goldRepo.FindByID(ctx, txID)
	if err != nil {
		log.Printf("[gold-worker] gagal mengambil transaksi ID=%d: %v", txID, err)
		return
	}

	log.Printf("[gold-worker] memproses transaksi ID=%d | user=%d | %.4f gram | Rp%.2f | status=%s",
		tx.ID, tx.UserID, tx.GramAmount, tx.TotalRupiah, tx.Status)

	// Guard: skip jika transaksi sudah tidak lagi berstatus 'pending'.
	// Ini bisa terjadi jika ID yang sama masuk 2x ke queue (rare edge case).
	if tx.Status != "pending" {
		log.Printf("[gold-worker] transaksi ID=%d status='%s' (bukan pending), dilewati", txID, tx.Status)
		return
	}

	// --- Ambil wallet_address anggota dari database ---
	// wallet_address adalah *string (nullable) — anggota wajib mengisi sebelum bisa
	// menerima token emas on-chain.
	recipientAddr, ok := w.resolveRecipientWallet(ctx, txID, tx.UserID)
	if !ok {
		// resolveRecipientWallet sudah menangani refund dan logging.
		return
	}

	if !w.blockchainReady {
		log.Printf("[gold-worker] [log-only] transaksi ID=%d diterima — blockchain belum dikonfigurasi | wallet: %s",
			txID, recipientAddr.Hex())
		return
	}

	// Kirim transaksi Mint ke Smart Contract dengan alamat penerima yang benar.
	w.mintOnChain(ctx, tx.ID, tx.GramAmount, recipientAddr)
}

// resolveRecipientWallet mengambil dan memvalidasi wallet_address anggota dari database.
//
// Jika wallet_address nil (belum diset oleh anggota):
//   - Trigger refund saldo secara atomik (goldRepo.RefundFailedTransaction)
//   - Log peringatan agar admin bisa menindaklanjuti
//   - Return (zero, false) → caller harus return tanpa memproses lebih lanjut
//
// Jika berhasil, return (common.Address, true).
func (w *GoldWorker) resolveRecipientWallet(ctx context.Context, goldTxID int64, userID int64) (common.Address, bool) {
	user, err := w.userRepo.FindByID(ctx, userID)
	if err != nil {
		log.Printf("[gold-worker] gagal mengambil data user ID=%d untuk transaksi ID=%d: %v", userID, goldTxID, err)
		return common.Address{}, false
	}

	// Validasi: wallet_address harus sudah diset oleh anggota.
	if !isValidWallet(user) {
		log.Printf("[gold-worker] PERINGATAN: user ID=%d belum set wallet_address — transaksi ID=%d akan di-refund",
			userID, goldTxID)

		// Refund saldo anggota secara atomik karena token tidak bisa dikirimkan.
		// RefundFailedTransaction menangani status 'pending' dan 'processing'.
		if refundErr := w.goldRepo.RefundFailedTransaction(ctx, goldTxID); refundErr != nil {
			log.Printf("[gold-worker] KRITIS: refund gagal untuk transaksi ID=%d (wallet tidak ada): %v",
				goldTxID, refundErr)
		} else {
			log.Printf("[gold-worker] ✓ refund selesai untuk transaksi ID=%d — wallet belum dikonfigurasi user", goldTxID)
		}
		return common.Address{}, false
	}

	// Parse hex string wallet address ke common.Address.
	addr := common.HexToAddress(*user.WalletAddress)
	log.Printf("[gold-worker] wallet penerima untuk user ID=%d: %s", userID, addr.Hex())
	return addr, true
}

// isValidWallet mengembalikan true jika user sudah memiliki wallet_address yang valid.
// Wallet dianggap valid jika field tidak nil dan tidak berupa string kosong.
func isValidWallet(user *model.User) bool {
	return user.WalletAddress != nil && *user.WalletAddress != ""
}

// mintOnChain mengirim satu transaksi Mint ke Smart Contract CoopGold.
//
// Alur:
//  1. Konversi gram_amount (float64) → big.Int dengan presisi 4 desimal.
//  2. Buat instance binding CoopGold.
//  3. Panggil Mint(auth, recipientAddr, amount, goldTxID).
//  4. Simpan tx_hash dan update status → 'processing'.
//  5. Launch goroutine awaitReceipt untuk menunggu konfirmasi blok.
//
// recipientAddr adalah alamat wallet anggota yang sudah divalidasi oleh resolveRecipientWallet.
func (w *GoldWorker) mintOnChain(ctx context.Context, goldTxID int64, gramAmount float64, recipientAddr common.Address) {
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

	// --- 3. Panggil Mint dengan alamat wallet anggota ---
	// recipientAddr sudah divalidasi oleh resolveRecipientWallet — bukan owner.
	chainTx, err := coopGold.Mint(w.auth, recipientAddr, amount, big.NewInt(goldTxID))
	if err != nil {
		log.Printf("[gold-worker] GAGAL mint transaksi ID=%d: %v", goldTxID, err)
		// Update status ke 'failed' tanpa tx_hash — belum ada on-chain activity,
		// tidak perlu refund karena tx tidak sempat di-broadcast ke chain.
		if updateErr := w.goldRepo.UpdateStatusAndHash(ctx, goldTxID, "failed", ""); updateErr != nil {
			log.Printf("[gold-worker] gagal update status failed ID=%d: %v", goldTxID, updateErr)
		}
		return
	}

	// --- 5. Simpan tx_hash dan update status → 'processing' ---
	txHash := chainTx.Hash().Hex()
	if err := w.goldRepo.UpdateStatusAndHash(ctx, goldTxID, "processing", txHash); err != nil {
		log.Printf("[gold-worker] gagal update status processing ID=%d: %v", goldTxID, err)
		// Transaksi sudah di-broadcast tapi DB belum terupdate.
		// tx_hash sudah ada di chain — tidak ada tindakan darurat yang bisa dilakukan.
		// Monitoring / reconciliation tool perlu menangani ini.
		return
	}

	log.Printf("[gold-worker] transaksi ID=%d dikirim ke chain — tx_hash: %s | amount: %d unit",
		goldTxID, txHash, unitAmount)

	// --- 6. Launch goroutine untuk menunggu konfirmasi blok ---
	// awaitReceipt berjalan di goroutine terpisah agar BLPop loop tidak blocked.
	// Worker tetap bisa memproses ID berikutnya dari queue (~2 detik konfirmasi Polygon).
	go w.awaitReceipt(ctx, goldTxID, coopGold, chainTx)
}

// awaitReceipt menunggu konfirmasi blockchain dan mengupdate status transaksi.
//
// Fungsi ini berjalan di goroutine terpisah (dipanggil via `go w.awaitReceipt(...)`).
//
// Alur:
//  1. bind.WaitMined() — blocking sampai transaksi masuk ke blok.
//  2. Cek receipt.Status:
//     → 1 (sukses) : parse event GoldMinted untuk validasi, update DB → 'success'.
//     → 0 (reverted): trigger RefundFailedTransaction (atomik: refund + status failed).
//
// Graceful shutdown: jika ctx di-cancel saat menunggu, WaitMined return
// error context.Canceled — ditangani oleh isContextError, status tetap
// 'processing' di DB dan bisa di-recover saat server restart.
func (w *GoldWorker) awaitReceipt(ctx context.Context, goldTxID int64, coopGold *contract.CoopGold, chainTx *types.Transaction) {
	log.Printf("[gold-worker] menunggu konfirmasi blok untuk ID=%d (tx: %s)...",
		goldTxID, chainTx.Hash().Hex())

	// WaitMined memblokir sampai transaksi dikonfirmasi oleh minimal 1 blok.
	// Pada Polygon Amoy, biasanya ~2 detik. Pada Mainnet bisa lebih lama.
	receipt, err := bind.WaitMined(ctx, w.evmClient.Underlying(), chainTx)
	if err != nil {
		if isContextError(err) {
			// Shutdown bersih — status tetap 'processing' di DB.
			// Recovery: saat server restart, transaksi ini bisa di-check ulang
			// via tx_hash yang sudah tersimpan.
			log.Printf("[gold-worker] WaitMined dibatalkan saat shutdown — ID=%d status tetap 'processing'", goldTxID)
			return
		}
		// Error jaringan (node down, timeout, dll.) — log dan biarkan status 'processing'.
		// Jangan trigger refund karena transaksi mungkin sudah masuk ke chain.
		log.Printf("[gold-worker] WaitMined error untuk ID=%d: %v — status tetap 'processing'", goldTxID, err)
		return
	}

	// --- Cek receipt.Status ---
	switch receipt.Status {
	case types.ReceiptStatusSuccessful: // == 1
		// Transaksi dikonfirmasi sukses oleh EVM.

		// Opsional: parse event GoldMinted untuk validasi goldTxID on-chain.
		// Ini memberikan kepastian bahwa event yang tepat ter-emit di blok ini.
		w.validateGoldMintedEvent(receipt, goldTxID, coopGold)

		// Update status → 'success'
		if updErr := w.goldRepo.UpdateTransactionStatus(ctx, goldTxID, "success"); updErr != nil {
			log.Printf("[gold-worker] gagal update status success ID=%d: %v", goldTxID, updErr)
			return
		}
		log.Printf("[gold-worker] ✓ transaksi ID=%d dikonfirmasi ON-CHAIN — status: success | blok: %s",
			goldTxID, receipt.BlockNumber.String())

	case types.ReceiptStatusFailed: // == 0
		// Transaksi di-revert oleh EVM (contoh: require() gagal di Solidity).
		// Trigger refund atomik: kembalikan saldo + catat log + set status failed.
		log.Printf("[gold-worker] transaksi ID=%d di-REVERT oleh EVM — memulai proses refund...", goldTxID)

		if refundErr := w.goldRepo.RefundFailedTransaction(ctx, goldTxID); refundErr != nil {
			log.Printf("[gold-worker] KRITIS: refund gagal untuk ID=%d: %v", goldTxID, refundErr)
			// Jangan update status — biarkan tetap 'processing' agar monitoring
			// bisa mendeteksi dan operator bisa intervensi manual.
			return
		}
		log.Printf("[gold-worker] ✓ refund selesai untuk transaksi ID=%d — saldo anggota dikembalikan", goldTxID)

	default:
		log.Printf("[gold-worker] receipt.Status tidak dikenal (%d) untuk ID=%d — tidak ada aksi",
			receipt.Status, goldTxID)
	}
}

// validateGoldMintedEvent mem-parse log receipt untuk menemukan dan memvalidasi
// event GoldMinted yang sesuai dengan goldTxID.
//
// Fungsi ini bersifat informatif (non-blocking): jika event tidak ditemukan
// atau goldTxID tidak cocok, hanya di-log sebagai peringatan — tidak menggagalkan
// alur utama. Transaksi on-chain tetap dianggap sukses jika receipt.Status == 1.
func (w *GoldWorker) validateGoldMintedEvent(receipt *types.Receipt, goldTxID int64, coopGold *contract.CoopGold) {
	for _, vLog := range receipt.Logs {
		event, err := coopGold.ParseGoldMinted(*vLog)
		if err != nil {
			// Bukan event GoldMinted — lewati
			continue
		}

		// Validasi: goldTxID on-chain harus cocok dengan yang ada di DB.
		if event.GoldTxID.Int64() != goldTxID {
			log.Printf("[gold-worker] PERINGATAN: GoldTxID on-chain (%d) tidak cocok dengan DB (%d)",
				event.GoldTxID.Int64(), goldTxID)
			continue
		}

		log.Printf("[gold-worker] event GoldMinted tervalidasi — to: %s | amount: %s unit | txID: %d",
			event.To.Hex(), event.Amount.String(), goldTxID)
		return // event pertama yang cocok sudah cukup
	}
}

// isContextError mengembalikan true jika error disebabkan oleh context cancellation.
// Digunakan untuk membedakan shutdown yang disengaja dari error jaringan.
func isContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
