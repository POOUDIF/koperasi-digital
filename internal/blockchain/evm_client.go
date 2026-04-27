// Package blockchain menyediakan klien untuk berinteraksi dengan jaringan EVM
// (Ethereum Virtual Machine), khususnya Polygon dan Amoy Testnet.
//
// Arsitektur integrasi Web3 di Koperasi Digital:
//
//	HTTP Handler
//	    ↓ (off-chain, sinkron)
//	GoldService  ──→  GoldRepository  ──→  PostgreSQL
//	    ↓ (on-chain, asinkron via worker)
//	blockchain.Client  ──→  SmartContract (Polygon)
//
// Alur dua-tahap dipilih karena transaksi blockchain bersifat asinkron dan
// tidak deterministik dalam hal waktu konfirmasi. Dengan mencatat transaksi
// off-chain terlebih dahulu (status: pending), endpoint HTTP bisa merespons
// cepat tanpa menunggu konfirmasi blok (~2 detik di Polygon).
// Worker terpisah kemudian mengirim transaksi ke chain dan memperbarui status.
package blockchain

import (
	"context"
	"fmt"
	"log"

	"github.com/ethereum/go-ethereum/ethclient"
)

// Client membungkus ethclient.Client milik go-ethereum.
//
// Tujuan pembungkusan (wrapper pattern):
//   - Menyembunyikan detail library dari layer service/handler.
//   - Memudahkan penambahan retry logic, circuit breaker, atau metrics di satu tempat.
//   - Menyederhanakan mock saat unit testing (cukup mock interface, bukan ethclient asli).
type Client struct {
	eth *ethclient.Client
}

// NewEVMClient menghubungkan backend ke node EVM via JSON-RPC dan memverifikasi
// koneksi dengan membaca Chain ID dari node.
//
// Parameter rpcURL adalah WebSocket atau HTTPS endpoint node Polygon, contoh:
//   - Amoy Testnet : "https://rpc-amoy.polygon.technology"
//   - Polygon Mainnet: "https://polygon-rpc.com"
//   - Alchemy/Infura : "https://polygon-mainnet.g.alchemy.com/v2/<API_KEY>"
//
// Fungsi ini mengembalikan error jika:
//   - URL tidak bisa dihubungi (node offline, firewall, URL salah).
//   - Node merespons tapi tidak bisa mengembalikan Chain ID (versi tidak kompatibel).
//
// Panggil Close() via defer di main.go setelah NewEVMClient berhasil.
func NewEVMClient(rpcURL string) (*Client, error) {
	// ethclient.Dial mendukung HTTP, HTTPS, WS, dan WSS — pilihan URL
	// ditentukan oleh provider (Alchemy, Infura, node sendiri, dsb.).
	eth, err := ethclient.Dial(rpcURL)
	if err != nil {
		return nil, fmt.Errorf("gagal terhubung ke node EVM (%s): %w", rpcURL, err)
	}

	// Ping: minta Chain ID sebagai verifikasi bahwa koneksi benar-benar aktif
	// dan node merespons dengan benar.
	//
	// Chain ID penting karena:
	//   - Memastikan kita terhubung ke jaringan yang benar (bukan mainnet saat development).
	//   - Wajib disertakan di setiap signed transaction untuk mencegah replay attack
	//     (EIP-155: transaksi yang ditandatangani di testnet tidak valid di mainnet).
	//
	// Referensi Chain ID:
	//   80002  → Polygon Amoy Testnet
	//   137    → Polygon Mainnet
	//   1      → Ethereum Mainnet
	chainID, err := eth.ChainID(context.Background())
	if err != nil {
		eth.Close() // bebaskan koneksi sebelum return error
		return nil, fmt.Errorf("verifikasi chain ID gagal (node terhubung tapi tidak merespons): %w", err)
	}

	log.Printf("[blockchain] EVM client aktif — chain ID: %s", chainID.String())

	return &Client{eth: eth}, nil
}

// Underlying mengembalikan *ethclient.Client asli dari go-ethereum.
//
// Digunakan oleh komponen tingkat lanjut yang membutuhkan akses penuh ke API
// go-ethereum: baca saldo token, subscribe event log, kirim raw transaction,
// atau panggil fungsi Smart Contract via ABI.
//
// Gunakan dengan hati-hati — caller yang memanggil eth.Close() langsung akan
// merusak Client ini. Untuk menutup koneksi, selalu gunakan Client.Close().
func (c *Client) Underlying() *ethclient.Client {
	return c.eth
}

// Close menutup koneksi RPC ke node EVM dan membebaskan resource yang terkait.
//
// Selalu panggil via defer setelah NewEVMClient berhasil:
//
//	client, err := blockchain.NewEVMClient(rpcURL)
//	if err != nil { ... }
//	defer client.Close()
func (c *Client) Close() {
	c.eth.Close()
}
