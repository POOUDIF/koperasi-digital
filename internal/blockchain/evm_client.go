// Package blockchain menyediakan klien untuk berinteraksi dengan jaringan EVM
// (Ethereum Virtual Machine), khususnya Polygon dan Amoy Testnet.
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
// Tujuan pembungkusan (wrapper pattern):
type Client struct {
	eth     *ethclient.Client
	chainID int64 // disimpan untuk pembuatan TransactOpts
}

// NewEVMClient menghubungkan backend ke node EVM via JSON-RPC dan memverifikasi
// koneksi dengan membaca Chain ID dari node.
func NewEVMClient(rpcURL string) (*Client, error) {
	// ethclient.Dial mendukung HTTP, HTTPS, WS, dan WSS — pilihan URL
	// ditentukan oleh provider (Alchemy, Infura, node sendiri, dsb.).
	eth, err := ethclient.Dial(rpcURL)
	if err != nil {
		return nil, fmt.Errorf("gagal terhubung ke node EVM (%s): %w", rpcURL, err)
	}

	// Ping: minta Chain ID sebagai verifikasi bahwa koneksi benar-benar aktif
	// dan node merespons dengan benar.
	chainID, err := eth.ChainID(context.Background())
	if err != nil {
		eth.Close() // bebaskan koneksi sebelum return error
		return nil, fmt.Errorf("verifikasi chain ID gagal (node terhubung tapi tidak merespons): %w", err)
	}

	log.Printf("[blockchain] EVM client aktif — chain ID: %s", chainID.String())

	return &Client{eth: eth, chainID: chainID.Int64()}, nil
}

// Underlying mengembalikan *ethclient.Client asli dari go-ethereum.
// Digunakan oleh komponen tingkat lanjut yang membutuhkan akses penuh ke API
func (c *Client) Underlying() *ethclient.Client {
	return c.eth
}

// Close menutup koneksi RPC ke node EVM dan membebaskan resource yang terkait.
// Selalu panggil via defer setelah NewEVMClient berhasil:
func (c *Client) Close() {
	c.eth.Close()
}

// NewTransactOpts membuat bind.TransactOpts dari private key hex string.
// TransactOpts digunakan untuk menandatangani setiap transaksi yang dikirim
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
func ParseContractAddress(addressHex string) (common.Address, error) {
	if !common.IsHexAddress(addressHex) {
		return common.Address{}, fmt.Errorf("alamat kontrak tidak valid: %s", addressHex)
	}
	return common.HexToAddress(addressHex), nil
}

// GetTransactor membuat *bind.TransactOpts dari private key hex string.
// Fungsi ini adalah alias publik dari NewTransactOpts dengan nama yang lebih
func (c *Client) GetTransactor(privateKeyHex string) (*bind.TransactOpts, error) {
	return c.NewTransactOpts(privateKeyHex)
}
