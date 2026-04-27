-- =============================================================================
-- Migrasi 007: Tambah wallet_address dan status ke tabel users
--
-- Dua kolom baru ini melengkapi fitur Web3 dan manajemen akun:
--
--   wallet_address : alamat wallet Polygon (EVM) milik anggota.
--                   Diisi saat anggota menghubungkan wallet mereka di frontend.
--                   NULL = belum menghubungkan wallet.
--                   UNIQUE = satu wallet hanya bisa terhubung ke satu akun.
--
--   status         : status keanggotaan akun.
--                   active   : akun normal, bisa login dan bertransaksi.
--                   inactive : akun belum diaktivasi atau dinonaktifkan sementara.
--                   banned   : akun diblokir permanen oleh admin/pengurus.
--
-- Mengapa ALTER TABLE, bukan ubah migration 001?
-- Migration sudah pernah dijalankan di database development/production.
-- Mengubah file migration lama dan re-run akan menghapus data yang ada.
-- Pola yang benar: selalu tambahkan kolom baru via migration baru.
-- =============================================================================

-- Enum untuk status akun.
-- Dibuat terpisah dari user_role agar bisa dipakai di tabel lain jika diperlukan.
CREATE TYPE user_status AS ENUM ('active', 'inactive', 'banned');

-- Kolom wallet_address:
--   - NULLABLE  : belum semua anggota menghubungkan wallet saat daftar.
--   - UNIQUE    : satu alamat wallet tidak boleh dimiliki dua akun berbeda.
--                 Mencegah anggota "meminjamkan" wallet untuk double-spending token emas.
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS wallet_address VARCHAR(42) UNIQUE,
    ADD COLUMN IF NOT EXISTS status         user_status NOT NULL DEFAULT 'active';

-- Index untuk query admin: tampilkan semua akun banned / inactive dengan cepat.
CREATE INDEX IF NOT EXISTS idx_users_status
    ON users(status)
    WHERE status != 'active';

-- Index untuk lookup wallet_address dari backend saat proses mint/burn token.
-- Partial index (WHERE NOT NULL) lebih efisien — tidak mengindeks baris yang belum punya wallet.
CREATE INDEX IF NOT EXISTS idx_users_wallet_address
    ON users(wallet_address)
    WHERE wallet_address IS NOT NULL;

-- Isi status = 'active' untuk semua user yang sudah ada (retroaktif).
-- DEFAULT 'active' di atas hanya berlaku untuk INSERT baru, bukan baris yang ada.
UPDATE users SET status = 'active' WHERE status IS NULL;
