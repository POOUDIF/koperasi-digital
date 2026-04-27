-- =============================================================================
-- Migrasi 001: Tabel Dasar Users
--
-- Ini adalah tabel fondasi seluruh sistem koperasi.
-- Semua modul lain (savings, financing, gold) mereferensikan tabel ini via FK.
--
-- CATATAN URUTAN MIGRASI:
--   001 → tabel users (file ini)
--   002 → tabel savings (savings_products, savings_accounts, savings_transactions)
--   003 → tabel financing (financing, financing_installments)
--   004 → tambah kolom role + reviewed_by ke tabel di atas
--   005 → tambah kolom paid_at ke financing_installments
--   006 → tabel gold (gold_prices, gold_transactions)
--   007 → tambah wallet_address + status ke tabel users (file ini)
-- =============================================================================

-- Enum role: menentukan hak akses dalam sistem RBAC.
--   anggota    : hak akses standar — simpan, ajukan pembiayaan, beli emas.
--   pengurus   : semua hak anggota + approve/reject pembiayaan.
--   admin      : semua hak pengurus + manajemen data master.
--   super_admin: akses penuh tanpa pembatasan.
CREATE TYPE user_role AS ENUM ('anggota', 'pengurus', 'admin', 'super_admin');

CREATE TABLE IF NOT EXISTS users (
    id            BIGSERIAL    PRIMARY KEY,

    -- Nama lengkap sesuai KTP — dipakai untuk dokumen resmi koperasi.
    nama_lengkap  VARCHAR(150) NOT NULL,

    -- Email dipakai sebagai username login dan notifikasi.
    -- UNIQUE constraint: satu email = satu akun.
    email         VARCHAR(255) NOT NULL UNIQUE,

    -- Hash bcrypt dari password user.
    -- TIDAK PERNAH simpan password plain text.
    -- Tag json:"-" di Go struct memastikan field ini tidak bocor ke API response.
    password_hash VARCHAR(255) NOT NULL,

    -- Role default 'anggota' — ditetapkan saat registrasi.
    -- Upgrade ke pengurus/admin/super_admin dilakukan oleh super_admin.
    role          user_role    NOT NULL DEFAULT 'anggota',

    created_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

-- Index untuk login: query FindByEmail berjalan O(log n) bukan O(n).
CREATE INDEX IF NOT EXISTS idx_users_email ON users(email);

-- Trigger untuk auto-update kolom updated_at setiap kali baris diubah.
-- Tanpa ini, updated_at harus di-set manual di setiap UPDATE query.
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE TRIGGER users_updated_at
    BEFORE UPDATE ON users
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();
