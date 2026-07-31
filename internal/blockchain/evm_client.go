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
	"crypto/ecdsa"
	"fmt"
	"log"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

// Client membungkus ethclient.Client milik go-ethereum.
//
// Tujuan pembungkusan (wrapper pattern):
//   - Menyembunyikan detail library dari layer service/handler.
//   - Memudahkan penambahan retry logic, circuit breaker, atau metrics di satu tempat.
//   - Menyederhanakan mock saat unit testing (cukup mock interface, bukan ethclient asli).
type Client struct {
	eth     *ethclient.Client
	chainID int64 // disimpan untuk pembuatan TransactOpts
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

	return &Client{eth: eth, chainID: chainID.Int64()}, nil
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

// NewTransactOpts membuat bind.TransactOpts dari private key hex string.
//
// TransactOpts digunakan untuk menandatangani setiap transaksi yang dikirim
// ke Smart Contract. Objek ini berisi:
//   - Private key → untuk signing (ECDSA secp256k1).
//   - Chain ID    → untuk EIP-155 replay protection.
//   - GasPrice    → nil agar node menentukan otomatis via eth_gasPrice.
//   - GasLimit    → 0 agar node mengestimasi via eth_estimateGas.
//
// Parameter:
//   - privateKeyHex: private key dalam format hex, dengan atau tanpa prefix "0x".
//     Contoh: "0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
//
// Mengembalikan error jika:
//   - privateKeyHex bukan hex yang valid.
//   - Gagal membuat signer untuk chain ID yang aktif.
func (c *Client) NewTransactOpts(privateKeyHex string) (*bind.TransactOpts, error) {
	// Hapus prefix "0x" jika ada — crypto.HexToECDSA tidak menerima prefix.
	privateKeyHex = strings.TrimPrefix(privateKeyHex, "0x")

	privateKey, err := crypto.HexToECDSA(privateKeyHex)
	if err != nil {
		return nil, fmt.Errorf("gagal parse private key: %w", err)
	}

	auth, err := bind.NewKeyedTransactorWithChainID(privateKey, common.Big0.SetInt64(c.chainID))
	if err != nil {
		return nil, fmt.Errorf("gagal membuat TransactOpts untuk chain ID %d: %w", c.chainID, err)
	}

	// GasPrice = nil → node akan menentukan harga gas otomatis via eth_gasPrice.
	// Ini lebih aman daripada hardcode karena gas price di Polygon bisa fluktuatif.
	auth.GasPrice = nil

	// GasLimit = 0 → node akan mengestimasi gas via eth_estimateGas.
	// Untuk fungsi mint sederhana, estimasi node biasanya akurat.
	auth.GasLimit = 0

	log.Printf("[blockchain] TransactOpts berhasil dibuat — address: %s",
		crypto.PubkeyToAddress(*privateKey.Public().(*ecdsa.PublicKey)).Hex())

	return auth, nil
}

// ParseContractAddress memvalidasi dan mengonversi string alamat kontrak
// menjadi common.Address.
//
// Mengembalikan error jika format alamat tidak valid (bukan hex 40 karakter).
func ParseContractAddress(addressHex string) (common.Address, error) {
	if !common.IsHexAddress(addressHex) {
		return common.Address{}, fmt.Errorf("alamat kontrak tidak valid: %s", addressHex)
	}
	return common.HexToAddress(addressHex), nil
}

// GetTransactor membuat *bind.TransactOpts dari private key hex string.
//
// Fungsi ini adalah alias publik dari NewTransactOpts dengan nama yang lebih
// deskriptif. Gunakan GetTransactor saat membuat signer untuk setiap batch
// transaksi baru (misalnya: tiap sesi worker restart) karena TransactOpts
// tidak goroutine-safe jika digunakan bersamaan dari beberapa goroutine.
//
// Parameter:
//   - privateKeyHex: private key dalam format hex, dengan atau tanpa prefix "0x".
//
// Contoh penggunaan:
//
//	auth, err := evmClient.GetTransactor(cfg.OwnerPrivateKey)
//	if err != nil { ... }
//	tx, err := coopGold.Mint(auth, recipientAddr, amount, goldTxID)
func (c *Client) GetTransactor(privateKeyHex string) (*bind.TransactOpts, error) {
	return c.NewTransactOpts(privateKeyHex)
}
