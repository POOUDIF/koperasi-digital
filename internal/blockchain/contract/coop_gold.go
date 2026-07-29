// Package contract menyediakan Go binding untuk Smart Contract CoopGold (CGLD).
//
// File ini menggantikan output dari `abigen` yang biasanya di-generate secara otomatis.
// Dibuat secara manual karena hanya membutuhkan subset kecil dari fungsi kontrak
// (terutama Mint) dan untuk menghindari dependensi pada tool `abigen`.
//
// ABI sumber: contracts/CoopGold.abi.json
//
// Fungsi yang di-bind:
//   - Mint(to address, amount *big.Int, goldTxID *big.Int)
//
// Presisi decimal: 4 (1 gram = 10_000 unit on-chain).
package contract

import (
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// CoopGoldABI adalah ABI minimal untuk fungsi yang dipakai backend.
// Hanya berisi fungsi `mint` — fungsi lain (transfer, approve, dll.)
// tidak diperlukan karena hanya owner (backend) yang memanggil mint.
const CoopGoldABI = `[
  {
    "type": "function",
    "name": "mint",
    "inputs": [
      { "name": "to",       "type": "address" },
      { "name": "amount",   "type": "uint256" },
      { "name": "goldTxID", "type": "uint256" }
    ],
    "outputs": [],
    "stateMutability": "nonpayable"
  }
]`

// CoopGold adalah wrapper untuk berinteraksi dengan Smart Contract CoopGold.
//
// Struct ini mengikuti pola yang sama dengan output `abigen`:
//   - Menyimpan referensi ke kontrak via bind.BoundContract.
//   - Menyediakan method strongly-typed untuk setiap fungsi kontrak.
type CoopGold struct {
	contract *bind.BoundContract
}

// NewCoopGold membuat instance binding baru yang terhubung ke alamat kontrak.
//
// Parameter:
//   - address  : alamat kontrak CoopGold yang sudah di-deploy di Polygon.
//   - backend  : ethclient yang terhubung ke node RPC (dari blockchain.Client.Underlying()).
//
// Mengembalikan error jika ABI tidak bisa di-parse (seharusnya tidak terjadi
// kecuali ada kesalahan pada konstanta CoopGoldABI di atas).
func NewCoopGold(address common.Address, backend bind.ContractBackend) (*CoopGold, error) {
	parsed, err := abi.JSON(strings.NewReader(CoopGoldABI))
	if err != nil {
		return nil, err
	}

	contract := bind.NewBoundContract(address, parsed, backend, backend, backend)

	return &CoopGold{contract: contract}, nil
}

// Mint memanggil fungsi `mint(address to, uint256 amount, uint256 goldTxID)`
// pada Smart Contract CoopGold.
//
// Fungsi ini mencetak (mint) token CGLD ke wallet anggota setelah transaksi
// pembelian emas dikonfirmasi off-chain.
//
// Parameter:
//   - opts     : TransactOpts yang berisi private key owner untuk menandatangani tx.
//   - to       : alamat wallet Polygon milik anggota yang membeli emas.
//   - amount   : jumlah token dalam unit terkecil (gram × 10_000).
//                Contoh: 0.5 gram → 5_000.
//   - goldTxID : ID transaksi dari tabel gold_transactions (untuk audit trail on-chain).
//
// Mengembalikan *types.Transaction yang berisi tx hash setelah transaksi dikirim ke node.
// Catatan: transaksi belum tentu dikonfirmasi — status "processing" dulu, bukan "success".
func (c *CoopGold) Mint(opts *bind.TransactOpts, to common.Address, amount *big.Int, goldTxID *big.Int) (*types.Transaction, error) {
	return c.contract.Transact(opts, "mint", to, amount, goldTxID)
}
