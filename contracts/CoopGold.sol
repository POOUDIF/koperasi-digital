// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

// =============================================================================
//  CoopGold (CGLD) — Token Emas Digital Koperasi Syariah
//  Network target: Polygon Amoy Testnet (Chain ID: 80002)
// =============================================================================
//
//  ARSITEKTUR INTEGRASI (2 Tahap):
//
//  [TAHAP 1 — OFF-CHAIN, sudah diimplementasi]
//    POST /api/v1/gold/buy
//      → potong saldo Wadiah di PostgreSQL
//      → insert gold_transactions (status: 'pending', tx_hash: NULL)
//
//  [TAHAP 2 — ON-CHAIN, contract ini yang menangani]
//    Background Worker Golang
//      → ambil gold_transactions WHERE status = 'pending'
//      → panggil CoopGold.mint(memberWallet, amount)
//      → terima tx_hash dari Polygon
//      → UPDATE gold_transactions SET status = 'success', tx_hash = $1
//
//  DESAIN DECIMAL:
//    Standard ERC-20 pakai 18 decimal (seperti ETH).
//    Untuk emas, presisi 4 decimal sudah lebih dari cukup:
//      1 CGLD          = 1 gram emas
//      0.5 CGLD        = 0.5000 gram (setengah gram)
//      0.0001 CGLD     = 0.0001 gram (batas minimum)
//    Representasi on-chain:
//      1 gram   → 10_000 units (10^4)
//      0.5 gram →  5_000 units
//    Ini KONSISTEN dengan kolom DECIMAL(10,4) di tabel gold_transactions PostgreSQL.
//
// =============================================================================
//
//  PANDUAN DEPLOY VIA REMIX IDE (https://remix.ethereum.org)
//  — Tidak perlu instalasi Hardhat/Foundry di lokal —
//
//  LANGKAH 1: Persiapan MetaMask
//    a. Install ekstensi MetaMask di browser.
//    b. Tambahkan jaringan Polygon Amoy Testnet secara manual:
//         Network Name : Polygon Amoy Testnet
//         RPC URL      : https://rpc-amoy.polygon.technology
//         Chain ID     : 80002
//         Symbol       : POL
//         Explorer     : https://amoy.polygonscan.com
//    c. Dapatkan test POL (gas fee) dari faucet:
//         https://faucet.polygon.technology  (pilih "Amoy", masukkan address wallet)
//       Minimal butuh ~0.1 POL untuk deploy.
//
//  LANGKAH 2: Buka Remix
//    a. Buka https://remix.ethereum.org
//    b. Di panel kiri "File Explorer", klik ikon "+" → buat file baru.
//    c. Beri nama: CoopGold.sol
//    d. Paste seluruh isi file ini ke editor.
//
//  LANGKAH 3: Compile
//    a. Klik tab "Solidity Compiler" (ikon <S> di panel kiri).
//    b. Pilih Compiler version: 0.8.20 (atau versi 0.8.x terbaru yang tersedia).
//    c. Centang "Enable optimization", set Runs = 200.
//       (Optimisasi mengurangi gas cost pada setiap pemanggilan fungsi.)
//    d. Klik tombol "Compile CoopGold.sol".
//    e. Pastikan TIDAK ada error merah. Warning kuning boleh diabaikan.
//
//  LANGKAH 4: Deploy ke Polygon Amoy
//    a. Klik tab "Deploy & Run Transactions" (ikon rocket di panel kiri).
//    b. Di dropdown "Environment", pilih "Injected Provider - MetaMask".
//    c. MetaMask akan meminta izin koneksi → Approve.
//    d. Pastikan MetaMask sudah aktif di jaringan Polygon Amoy (Chain ID: 80002).
//    e. Di dropdown "Contract", pastikan "CoopGold" sudah terpilih.
//    f. Contract ini TIDAK butuh argumen constructor → langsung klik "Deploy".
//    g. MetaMask akan memunculkan popup konfirmasi transaksi → Confirm.
//    h. Tunggu ~2 detik hingga transaksi dikonfirmasi oleh blockchain.
//
//  LANGKAH 5: Simpan Contract Address & ABI
//    Setelah deploy berhasil, dua data ini WAJIB disimpan untuk backend Golang:
//
//    CONTRACT ADDRESS:
//      Di panel kiri Remix, di bawah "Deployed Contracts", akan muncul:
//        "COOGOLD at 0xABC...123"
//      Copy address tersebut (format: 0x + 40 karakter hex).
//      Simpan ke .env backend:
//        GOLD_CONTRACT_ADDRESS=0xABCDEF...
//
//    ABI (Application Binary Interface):
//      ABI adalah "skema" contract — daftar fungsi dan event yang bisa dipanggil.
//      Backend Golang butuh ini untuk tahu cara encode/decode pemanggilan fungsi.
//      Cara mengambil ABI dari Remix:
//        1. Klik tab "Solidity Compiler".
//        2. Scroll ke bawah, klik tombol "ABI" (di bawah nama contract).
//        3. ABI JSON akan ter-copy ke clipboard.
//        4. Simpan ke file: contracts/CoopGold.abi.json
//
//  LANGKAH 6: Verifikasi di PolygonScan (opsional tapi SANGAT dianjurkan)
//    a. Buka https://amoy.polygonscan.com/address/0x<CONTRACT_ADDRESS>
//    b. Klik tab "Contract" → "Verify and Publish"
//    c. Isi:
//         Compiler type     : Solidity (Single file)
//         Compiler version  : v0.8.20 (sama persis dengan yang dipakai saat deploy)
//         License           : MIT
//    d. Paste source code contract ini → Submit.
//    e. Setelah terverifikasi, siapapun bisa membaca dan mengaudit contract kamu.
//       PENTING untuk membangun kepercayaan anggota koperasi (prinsip transparansi syariah).
//
//  LANGKAH 7: Transfer Ownership ke Backend Wallet (WAJIB di production)
//    Contract ini deploy dengan msg.sender sebagai owner.
//    Di production, owner yang memiliki hak mint/burn adalah backend server,
//    bukan wallet development kamu.
//    Cara transfer:
//      Di Remix → Deployed Contracts → transferOwnership → isi address wallet backend
//      ATAU panggil fungsi ini via backend Golang setelah setup selesai.
//
// =============================================================================

import "@openzeppelin/contracts/token/ERC20/ERC20.sol";
import "@openzeppelin/contracts/access/Ownable.sol";

/**
 * @title  CoopGold
 * @author Koperasi Digital Syariah
 * @notice Token ERC-20 yang merepresentasikan kepemilikan emas fisik anggota koperasi.
 *         1 CGLD = 1 gram emas. Minimum unit: 0.0001 gram (10^-4).
 *
 * @dev    Hak mint dan burn hanya dimiliki oleh owner (backend Golang/koperasi).
 *         Anggota TIDAK bisa mint token sendiri — setiap CGLD yang beredar harus
 *         di-back oleh emas fisik yang dibeli dan dikustodikan oleh koperasi.
 *         Prinsip ini menjaga rasio 1:1 antara CGLD dan gram emas fisik.
 */
contract CoopGold is ERC20, Ownable {

    // =========================================================================
    //  Events — untuk indexing off-chain dan audit trail on-chain
    // =========================================================================

    /**
     * @notice Dipancarkan setiap kali emas berhasil dibeli dan token di-mint.
     * @param to         Alamat wallet anggota yang menerima token.
     * @param amount     Jumlah token dalam unit terkecil (1 gram = 10_000 units).
     * @param goldTxID   ID transaksi di tabel gold_transactions PostgreSQL.
     *                   Dipakai backend untuk mencocokkan event on-chain dengan
     *                   record off-chain dan mengupdate status menjadi 'success'.
     */
    event GoldMinted(address indexed to, uint256 amount, uint256 indexed goldTxID);

    /**
     * @notice Dipancarkan setiap kali anggota menjual emas dan token di-burn.
     * @param from       Alamat wallet anggota yang tokennya dibakar.
     * @param amount     Jumlah token yang dibakar.
     * @param goldTxID   ID transaksi di tabel gold_transactions PostgreSQL.
     */
    event GoldBurned(address indexed from, uint256 amount, uint256 indexed goldTxID);

    // =========================================================================
    //  Constructor
    // =========================================================================

    /**
     * @dev Deployer otomatis menjadi owner pertama.
     *      Setelah deploy, transfer ownership ke wallet backend production
     *      menggunakan fungsi transferOwnership(address newOwner) dari Ownable.
     */
    constructor()
        ERC20("CoopGold", "CGLD")
        Ownable(msg.sender)
    {}

    // =========================================================================
    //  Override Decimals
    // =========================================================================

    /**
     * @notice Mengembalikan jumlah decimal token: 4 (bukan default 18).
     * @dev    Dengan 4 decimal:
     *           - 1 gram emas   = 1_0000 units (10_000)
     *           - 0.5 gram      = 5_000  units
     *           - 0.0001 gram   = 1      unit  (batas minimum)
     *         Konsisten dengan kolom DECIMAL(10,4) di PostgreSQL dan
     *         presisi 4 angka di belakang koma yang dipakai backend Golang.
     */
    function decimals() public pure override returns (uint8) {
        return 4;
    }

    // =========================================================================
    //  Fungsi Inti — hanya bisa dipanggil oleh Owner (backend Golang/koperasi)
    // =========================================================================

    /**
     * @notice Mint (cetak) token CGLD ke wallet anggota.
     *         Dipanggil backend setelah transaksi beli emas berhasil dikonfirmasi
     *         off-chain (saldo Wadiah sudah dipotong di PostgreSQL).
     *
     * @dev    Pemanggilan: CoopGold.mint(memberWallet, gramAmount * 10_000)
     *         Contoh: beli 0.5 gram → mint(memberWallet, 5_000)
     *
     * @param to        Alamat wallet Polygon milik anggota yang membeli emas.
     * @param amount    Jumlah token dalam unit terkecil (gram × 10_000).
     * @param goldTxID  ID baris di tabel gold_transactions untuk audit trail.
     */
    function mint(address to, uint256 amount, uint256 goldTxID)
        external
        onlyOwner
    {
        require(to != address(0), "CoopGold: mint ke address zero tidak diizinkan");
        require(amount > 0,       "CoopGold: amount harus lebih dari 0");

        _mint(to, amount);

        emit GoldMinted(to, amount, goldTxID);
    }

    /**
     * @notice Burn (hancurkan) token CGLD dari wallet anggota.
     *         Dipanggil backend saat anggota menjual emas kembali ke koperasi.
     *         Token dihancurkan untuk menjaga rasio 1:1 dengan emas fisik.
     *
     * @dev    Fungsi ini TIDAK memerlukan allowance dari anggota.
     *         Owner (koperasi) dipercaya untuk mendestruksi token setelah
     *         koperasi memverifikasi permintaan jual dan menyiapkan pembayaran.
     *         Ini berbeda dari burnFrom standar ERC20Burnable yang butuh approve().
     *
     *         Pemanggilan: CoopGold.burnFrom(memberWallet, gramAmount * 10_000)
     *
     * @param account   Alamat wallet anggota yang menjual emas.
     * @param amount    Jumlah token yang dihancurkan (gram × 10_000).
     * @param goldTxID  ID baris di tabel gold_transactions untuk audit trail.
     */
    function burnFrom(address account, uint256 amount, uint256 goldTxID)
        external
        onlyOwner
    {
        require(account != address(0), "CoopGold: burn dari address zero tidak diizinkan");
        require(amount > 0,            "CoopGold: amount harus lebih dari 0");
        require(
            balanceOf(account) >= amount,
            "CoopGold: saldo token anggota tidak mencukupi"
        );

        _burn(account, amount);

        emit GoldBurned(account, amount, goldTxID);
    }

    // =========================================================================
    //  View Helper — untuk kemudahan membaca nilai dari frontend/backend
    // =========================================================================

    /**
     * @notice Konversi balance token (unit terkecil) ke representasi gram (float).
     * @dev    Fungsi ini hanya untuk kemudahan pembacaan — kalkulasi aktual
     *         sebaiknya dilakukan off-chain di backend untuk menghindari
     *         pembulatan integer on-chain.
     *         Contoh: balanceInUnits(wallet) = 5_000 → artinya 0.5000 gram.
     *
     * @param account   Alamat wallet yang ingin dicek.
     * @return          Saldo dalam unit terkecil (bagi 10_000 untuk dapat gram).
     */
    function balanceInUnits(address account) external view returns (uint256) {
        return balanceOf(account);
    }
}
