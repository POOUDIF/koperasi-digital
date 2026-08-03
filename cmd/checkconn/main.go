// cmd/checkconn adalah tool CLI untuk memverifikasi koneksi ke node EVM.
// Penggunaan:
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/joho/godotenv"

	"koperasi-digital/internal/blockchain"
)

func main() {
	// Load .env jika ada (diabaikan jika tidak ada, sama seperti main server).
	_ = godotenv.Load()

	rpcURL := os.Getenv("POLYGON_RPC_URL")
	if rpcURL == "" {
		log.Fatal("[checkconn] POLYGON_RPC_URL belum diset. Tambahkan ke .env atau environment variable.")
	}

	fmt.Printf("[checkconn] menghubungkan ke: %s\n", rpcURL)

	// Beri timeout 10 detik — jika node tidak merespons dalam waktu ini,
	// kemungkinan besar URL salah atau jaringan bermasalah.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := blockchain.NewEVMClient(rpcURL)
	if err != nil {
		log.Fatalf("[checkconn] koneksi gagal: %v", err)
	}
	defer client.Close()

	// Ambil nomor blok terbaru sebagai konfirmasi bahwa node aktif dan sinkron.
	blockNumber, err := client.Underlying().BlockNumber(ctx)
	if err != nil {
		log.Fatalf("[checkconn] gagal membaca nomor blok: %v", err)
	}

	fmt.Printf("[checkconn] ✓ koneksi berhasil!\n")
	fmt.Printf("[checkconn] blok terbaru  : #%d\n", blockNumber)
	fmt.Println("[checkconn] node siap digunakan.")
}
