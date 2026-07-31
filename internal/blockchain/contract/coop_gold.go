// Package contract menyediakan Go binding untuk Smart Contract CoopGold (CGLD).
//
// # Tentang File Ini
//
// File ini adalah pengganti output `abigen` yang dibuat secara manual.
// Pendekatan manual dipilih agar:
//   - Tidak bergantung tool eksternal di CI/CD pipeline.
//   - Bisa dikustomisasi (contoh: tambah context, retry, logging).
//   - Tetap type-safe dan konsisten dengan ABI sumber.
//
// Untuk meregenerate ulang via abigen (opsional):
//
//	go install github.com/ethereum/go-ethereum/cmd/abigen@latest
//	abigen \
//	  --abi contracts/CoopGold.abi.json \
//	  --pkg contract \
//	  --type CoopGold \
//	  --out internal/blockchain/contract/coop_gold.go
//
// Sumber ABI: contracts/CoopGold.abi.json
// Network   : Polygon Amoy Testnet (Chain ID: 80002)
// Decimals  : 4  (1 gram = 10_000 unit on-chain)
package contract

import (
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// CoopGoldABI adalah ABI lengkap dari Smart Contract CoopGold.
// Mencakup semua fungsi dan event yang didefinisikan di contracts/CoopGold.abi.json.
//
// Digunakan oleh NewCoopGold untuk membangun bind.BoundContract.
const CoopGoldABI = `[
  {
    "type": "constructor",
    "inputs": [],
    "stateMutability": "nonpayable"
  },
  {
    "type": "function",
    "name": "mint",
    "inputs": [
      { "name": "to",       "type": "address", "internalType": "address"  },
      { "name": "amount",   "type": "uint256", "internalType": "uint256"  },
      { "name": "goldTxID", "type": "uint256", "internalType": "uint256"  }
    ],
    "outputs": [],
    "stateMutability": "nonpayable"
  },
  {
    "type": "function",
    "name": "burnFrom",
    "inputs": [
      { "name": "account",  "type": "address", "internalType": "address"  },
      { "name": "amount",   "type": "uint256", "internalType": "uint256"  },
      { "name": "goldTxID", "type": "uint256", "internalType": "uint256"  }
    ],
    "outputs": [],
    "stateMutability": "nonpayable"
  },
  {
    "type": "function",
    "name": "decimals",
    "inputs": [],
    "outputs": [{ "name": "", "type": "uint8", "internalType": "uint8" }],
    "stateMutability": "pure"
  },
  {
    "type": "function",
    "name": "totalSupply",
    "inputs": [],
    "outputs": [{ "name": "", "type": "uint256", "internalType": "uint256" }],
    "stateMutability": "view"
  },
  {
    "type": "function",
    "name": "balanceOf",
    "inputs": [
      { "name": "account", "type": "address", "internalType": "address" }
    ],
    "outputs": [{ "name": "", "type": "uint256", "internalType": "uint256" }],
    "stateMutability": "view"
  },
  {
    "type": "function",
    "name": "balanceInUnits",
    "inputs": [
      { "name": "account", "type": "address", "internalType": "address" }
    ],
    "outputs": [{ "name": "", "type": "uint256", "internalType": "uint256" }],
    "stateMutability": "view"
  },
  {
    "type": "function",
    "name": "transfer",
    "inputs": [
      { "name": "to",    "type": "address", "internalType": "address" },
      { "name": "value", "type": "uint256", "internalType": "uint256" }
    ],
    "outputs": [{ "name": "", "type": "bool", "internalType": "bool" }],
    "stateMutability": "nonpayable"
  },
  {
    "type": "function",
    "name": "transferFrom",
    "inputs": [
      { "name": "from",  "type": "address", "internalType": "address" },
      { "name": "to",    "type": "address", "internalType": "address" },
      { "name": "value", "type": "uint256", "internalType": "uint256" }
    ],
    "outputs": [{ "name": "", "type": "bool", "internalType": "bool" }],
    "stateMutability": "nonpayable"
  },
  {
    "type": "function",
    "name": "approve",
    "inputs": [
      { "name": "spender", "type": "address", "internalType": "address" },
      { "name": "value",   "type": "uint256", "internalType": "uint256" }
    ],
    "outputs": [{ "name": "", "type": "bool", "internalType": "bool" }],
    "stateMutability": "nonpayable"
  },
  {
    "type": "function",
    "name": "allowance",
    "inputs": [
      { "name": "owner",   "type": "address", "internalType": "address" },
      { "name": "spender", "type": "address", "internalType": "address" }
    ],
    "outputs": [{ "name": "", "type": "uint256", "internalType": "uint256" }],
    "stateMutability": "view"
  },
  {
    "type": "function",
    "name": "owner",
    "inputs": [],
    "outputs": [{ "name": "", "type": "address", "internalType": "address" }],
    "stateMutability": "view"
  },
  {
    "type": "function",
    "name": "transferOwnership",
    "inputs": [
      { "name": "newOwner", "type": "address", "internalType": "address" }
    ],
    "outputs": [],
    "stateMutability": "nonpayable"
  },
  {
    "type": "function",
    "name": "renounceOwnership",
    "inputs": [],
    "outputs": [],
    "stateMutability": "nonpayable"
  },
  {
    "type": "function",
    "name": "name",
    "inputs": [],
    "outputs": [{ "name": "", "type": "string", "internalType": "string" }],
    "stateMutability": "view"
  },
  {
    "type": "function",
    "name": "symbol",
    "inputs": [],
    "outputs": [{ "name": "", "type": "string", "internalType": "string" }],
    "stateMutability": "view"
  },
  {
    "type": "event",
    "name": "GoldMinted",
    "inputs": [
      { "name": "to",       "type": "address", "indexed": true  },
      { "name": "amount",   "type": "uint256", "indexed": false },
      { "name": "goldTxID", "type": "uint256", "indexed": true  }
    ],
    "anonymous": false
  },
  {
    "type": "event",
    "name": "GoldBurned",
    "inputs": [
      { "name": "from",     "type": "address", "indexed": true  },
      { "name": "amount",   "type": "uint256", "indexed": false },
      { "name": "goldTxID", "type": "uint256", "indexed": true  }
    ],
    "anonymous": false
  },
  {
    "type": "event",
    "name": "Transfer",
    "inputs": [
      { "name": "from",  "type": "address", "indexed": true  },
      { "name": "to",    "type": "address", "indexed": true  },
      { "name": "value", "type": "uint256", "indexed": false }
    ],
    "anonymous": false
  },
  {
    "type": "event",
    "name": "Approval",
    "inputs": [
      { "name": "owner",   "type": "address", "indexed": true  },
      { "name": "spender", "type": "address", "indexed": true  },
      { "name": "value",   "type": "uint256", "indexed": false }
    ],
    "anonymous": false
  },
  {
    "type": "event",
    "name": "OwnershipTransferred",
    "inputs": [
      { "name": "previousOwner", "type": "address", "indexed": true },
      { "name": "newOwner",      "type": "address", "indexed": true }
    ],
    "anonymous": false
  }
]`

// =============================================================================
// Event Structs
// =============================================================================

// CoopGoldGoldMinted merepresentasikan event GoldMinted dari Smart Contract.
//
// Event ini dipancarkan setiap kali fungsi mint() berhasil dieksekusi.
// Backend bisa mem-parse event ini dari receipt untuk memverifikasi bahwa
// mint benar-benar tercatat on-chain sebelum mengubah status ke 'success'.
type CoopGoldGoldMinted struct {
	To       common.Address // alamat wallet anggota yang menerima token
	Amount   *big.Int       // jumlah token dalam unit terkecil (gram × 10_000)
	GoldTxID *big.Int       // ID dari tabel gold_transactions di PostgreSQL
	Raw      types.Log      // raw log data
}

// CoopGoldGoldBurned merepresentasikan event GoldBurned dari Smart Contract.
//
// Event ini dipancarkan setiap kali fungsi burnFrom() berhasil dieksekusi.
type CoopGoldGoldBurned struct {
	From     common.Address // alamat wallet anggota yang tokennya dibakar
	Amount   *big.Int       // jumlah token yang dibakar
	GoldTxID *big.Int       // ID dari tabel gold_transactions di PostgreSQL
	Raw      types.Log      // raw log data
}

// =============================================================================
// CoopGold — Binding Struct
// =============================================================================

// CoopGold adalah wrapper type-safe untuk berinteraksi dengan Smart Contract CoopGold.
//
// Struct ini mengikuti pola yang sama dengan output `abigen`:
//   - Menyimpan referensi ke kontrak via bind.BoundContract.
//   - Menyimpan ABI hasil parse untuk keperluan event decoding.
//   - Menyediakan method strongly-typed untuk setiap fungsi dan event.
type CoopGold struct {
	contract *bind.BoundContract
	abi      abi.ABI // disimpan untuk event parsing (ParseGoldMinted, ParseGoldBurned)
}

// NewCoopGold membuat instance binding baru yang terhubung ke alamat kontrak.
//
// Parameter:
//   - address : alamat kontrak CoopGold yang sudah di-deploy di Polygon.
//   - backend : ethclient yang terhubung ke node RPC (dari blockchain.Client.Underlying()).
//
// Mengembalikan error jika ABI tidak bisa di-parse (seharusnya tidak terjadi).
func NewCoopGold(address common.Address, backend bind.ContractBackend) (*CoopGold, error) {
	parsed, err := abi.JSON(strings.NewReader(CoopGoldABI))
	if err != nil {
		return nil, err
	}

	c := bind.NewBoundContract(address, parsed, backend, backend, backend)

	return &CoopGold{contract: c, abi: parsed}, nil
}

// =============================================================================
// Write Functions (state-changing — membutuhkan gas & signing)
// =============================================================================

// Mint memanggil fungsi `mint(address to, uint256 amount, uint256 goldTxID)`
// pada Smart Contract.
//
// Fungsi ini mencetak token CGLD ke wallet anggota setelah pembelian emas
// dikonfirmasi off-chain (saldo Wadiah sudah dipotong di PostgreSQL).
//
// Parameter:
//   - opts     : TransactOpts berisi private key owner untuk signing.
//   - to       : alamat wallet Polygon milik anggota pembeli emas.
//   - amount   : jumlah token dalam unit terkecil (gram × 10_000).
//   - goldTxID : ID dari tabel gold_transactions untuk audit trail on-chain.
//
// Mengembalikan *types.Transaction — ambil hash via tx.Hash().Hex().
// Status transaksi masih 'pending' di jaringan — belum tentu dikonfirmasi.
func (c *CoopGold) Mint(opts *bind.TransactOpts, to common.Address, amount *big.Int, goldTxID *big.Int) (*types.Transaction, error) {
	return c.contract.Transact(opts, "mint", to, amount, goldTxID)
}

// BurnFrom memanggil fungsi `burnFrom(address account, uint256 amount, uint256 goldTxID)`
// pada Smart Contract.
//
// Fungsi ini menghancurkan token CGLD dari wallet anggota saat mereka menjual
// emas kembali ke koperasi. Hanya bisa dipanggil oleh owner (backend).
//
// Parameter:
//   - opts     : TransactOpts berisi private key owner untuk signing.
//   - account  : alamat wallet anggota yang menjual emas.
//   - amount   : jumlah token yang dihancurkan (gram × 10_000).
//   - goldTxID : ID dari tabel gold_transactions untuk audit trail.
func (c *CoopGold) BurnFrom(opts *bind.TransactOpts, account common.Address, amount *big.Int, goldTxID *big.Int) (*types.Transaction, error) {
	return c.contract.Transact(opts, "burnFrom", account, amount, goldTxID)
}

// Transfer memanggil fungsi ERC-20 standard `transfer(address to, uint256 value)`.
//
// Digunakan untuk memindahkan token CGLD antar wallet. Pengirim adalah
// wallet yang melakukan signing (opts.From).
func (c *CoopGold) Transfer(opts *bind.TransactOpts, to common.Address, value *big.Int) (*types.Transaction, error) {
	return c.contract.Transact(opts, "transfer", to, value)
}

// =============================================================================
// Read Functions (call — tidak menggunakan gas, tidak perlu signing)
// =============================================================================

// Decimals membaca jumlah desimal token dari kontrak.
// Untuk CoopGold selalu mengembalikan 4 (1 gram = 10_000 unit).
func (c *CoopGold) Decimals(opts *bind.CallOpts) (uint8, error) {
	var out []interface{}
	if err := c.contract.Call(opts, &out, "decimals"); err != nil {
		return 0, err
	}
	return out[0].(uint8), nil
}

// TotalSupply membaca total token CGLD yang saat ini beredar.
// Nilai dalam unit terkecil — bagi 10_000 untuk mendapatkan gram.
func (c *CoopGold) TotalSupply(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	if err := c.contract.Call(opts, &out, "totalSupply"); err != nil {
		return nil, err
	}
	return out[0].(*big.Int), nil
}

// BalanceOf membaca saldo token CGLD milik `account` dalam unit terkecil.
// Bagi 10_000 untuk mendapatkan nilai dalam gram.
func (c *CoopGold) BalanceOf(opts *bind.CallOpts, account common.Address) (*big.Int, error) {
	var out []interface{}
	if err := c.contract.Call(opts, &out, "balanceOf", account); err != nil {
		return nil, err
	}
	return out[0].(*big.Int), nil
}

// BalanceInUnits adalah alias untuk BalanceOf — mengembalikan saldo dalam unit terkecil.
// Disediakan karena kontrak CoopGold memiliki fungsi helper ini secara eksplisit.
func (c *CoopGold) BalanceInUnits(opts *bind.CallOpts, account common.Address) (*big.Int, error) {
	var out []interface{}
	if err := c.contract.Call(opts, &out, "balanceInUnits", account); err != nil {
		return nil, err
	}
	return out[0].(*big.Int), nil
}

// Owner membaca alamat wallet yang saat ini menjadi owner kontrak.
// Owner adalah satu-satunya yang bisa memanggil mint() dan burnFrom().
func (c *CoopGold) Owner(opts *bind.CallOpts) (common.Address, error) {
	var out []interface{}
	if err := c.contract.Call(opts, &out, "owner"); err != nil {
		return common.Address{}, err
	}
	return out[0].(common.Address), nil
}

// Name membaca nama token dari kontrak.
// Untuk CoopGold selalu mengembalikan "CoopGold".
func (c *CoopGold) Name(opts *bind.CallOpts) (string, error) {
	var out []interface{}
	if err := c.contract.Call(opts, &out, "name"); err != nil {
		return "", err
	}
	return out[0].(string), nil
}

// Symbol membaca simbol/ticker token dari kontrak.
// Untuk CoopGold selalu mengembalikan "CGLD".
func (c *CoopGold) Symbol(opts *bind.CallOpts) (string, error) {
	var out []interface{}
	if err := c.contract.Call(opts, &out, "symbol"); err != nil {
		return "", err
	}
	return out[0].(string), nil
}

// Allowance membaca jumlah token yang diizinkan `spender` untuk dibelanjakan
// atas nama `owner`. Bagian dari ERC-20 standard.
func (c *CoopGold) Allowance(opts *bind.CallOpts, owner common.Address, spender common.Address) (*big.Int, error) {
	var out []interface{}
	if err := c.contract.Call(opts, &out, "allowance", owner, spender); err != nil {
		return nil, err
	}
	return out[0].(*big.Int), nil
}

// =============================================================================
// Event Parsing — untuk verifikasi receipt & indexing off-chain
// =============================================================================

// ParseGoldMinted mendecode satu log entry menjadi struct CoopGoldGoldMinted.
//
// Cara penggunaan — verifikasi mint berhasil di-chain setelah menunggu receipt:
//
//	receipt, _ := ethClient.TransactionReceipt(ctx, chainTx.Hash())
//	for _, vLog := range receipt.Logs {
//	    event, err := coopGold.ParseGoldMinted(*vLog)
//	    if err == nil {
//	        log.Printf("Mint confirmed: to=%s amount=%s txID=%s",
//	            event.To.Hex(), event.Amount.String(), event.GoldTxID.String())
//	    }
//	}
func (c *CoopGold) ParseGoldMinted(log types.Log) (*CoopGoldGoldMinted, error) {
	event := new(CoopGoldGoldMinted)
	if err := c.contract.UnpackLog(event, "GoldMinted", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// ParseGoldBurned mendecode satu log entry menjadi struct CoopGoldGoldBurned.
//
// Cara penggunaan sama dengan ParseGoldMinted — iterate receipt.Logs dan
// panggil fungsi ini untuk setiap log yang mungkin merupakan event GoldBurned.
func (c *CoopGold) ParseGoldBurned(log types.Log) (*CoopGoldGoldBurned, error) {
	event := new(CoopGoldGoldBurned)
	if err := c.contract.UnpackLog(event, "GoldBurned", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}
