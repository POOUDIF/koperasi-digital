-- =============================================================================
-- Migrasi: Modul Simpanan Syariah
-- Tabel: savings_products, savings_accounts, savings_transactions
--
-- Akad yang didukung:
--   - Wadiah    : Titipan. Bank/koperasi menjaga dana, tidak wajib bagi hasil.
--   - Mudharabah: Bagi hasil. Keuntungan dibagi sesuai nisbah (profit_sharing_ratio).
--
-- Catatan uang: semua kolom nominal menggunakan DECIMAL(19,4) untuk menghindari
-- floating-point rounding error yang bisa terjadi jika memakai FLOAT/DOUBLE.
-- =============================================================================

-- -----------------------------------------------------------------------------
-- 1. savings_products — Produk simpanan yang ditawarkan koperasi
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS savings_products (
    id                   BIGSERIAL       PRIMARY KEY,

    -- Nama produk, contoh: "Simpanan Pokok", "Simpanan Wajib", "Simpanan Sukarela"
    name                 VARCHAR(100)    NOT NULL,

    -- Jenis akad syariah. CHECK constraint mencegah nilai di luar dua pilihan ini.
    akad_type            VARCHAR(20)     NOT NULL
                             CHECK (akad_type IN ('Wadiah', 'Mudharabah')),

    -- Setoran minimum per transaksi. 0 berarti tidak ada batas minimum.
    min_deposit          DECIMAL(19,4)   NOT NULL DEFAULT 0
                             CHECK (min_deposit >= 0),

    -- Rasio bagi hasil untuk anggota (0.00 – 1.00).
    -- Contoh: 0.60 berarti anggota mendapat 60% dari keuntungan pengelolaan.
    -- Untuk akad Wadiah nilai ini biasanya 0.
    profit_sharing_ratio DECIMAL(19,4)   NOT NULL DEFAULT 0
                             CHECK (profit_sharing_ratio >= 0 AND profit_sharing_ratio <= 1),

    -- TRUE jika simpanan ini wajib dimiliki oleh semua anggota (misal: simpanan pokok).
    is_mandatory         BOOLEAN         NOT NULL DEFAULT FALSE,

    created_at           TIMESTAMPTZ     NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ     NOT NULL DEFAULT NOW()
);

-- Seed data produk awal — sesuaikan sebelum go-live
INSERT INTO savings_products (name, akad_type, min_deposit, profit_sharing_ratio, is_mandatory)
VALUES
    ('Simpanan Pokok',   'Wadiah',     50000, 0.00, TRUE),
    ('Simpanan Wajib',   'Wadiah',     10000, 0.00, TRUE),
    ('Simpanan Sukarela','Mudharabah', 10000, 0.60, FALSE)
ON CONFLICT DO NOTHING;

-- -----------------------------------------------------------------------------
-- 2. savings_accounts — Rekening simpanan per anggota
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS savings_accounts (
    id                  BIGSERIAL       PRIMARY KEY,

    -- Relasi ke tabel users. RESTRICT mencegah penghapusan user yang masih punya rekening.
    user_id             BIGINT          NOT NULL
                            REFERENCES users(id) ON DELETE RESTRICT,

    -- Produk yang dipilih anggota saat membuka rekening.
    savings_product_id  BIGINT          NOT NULL
                            REFERENCES savings_products(id) ON DELETE RESTRICT,

    -- Saldo berjalan. Selalu diupdate berbarengan dengan insert ke savings_transactions
    -- dalam satu database transaction — lihat repository.Deposit().
    balance             DECIMAL(19,4)   NOT NULL DEFAULT 0
                            CHECK (balance >= 0),

    -- Status rekening: active = normal, frozen = dibekukan, closed = ditutup.
    status              VARCHAR(20)     NOT NULL DEFAULT 'active'
                            CHECK (status IN ('active', 'frozen', 'closed')),

    created_at          TIMESTAMPTZ     NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ     NOT NULL DEFAULT NOW()
);

-- Index untuk query umum: ambil semua rekening milik satu user
CREATE INDEX IF NOT EXISTS idx_savings_accounts_user_id
    ON savings_accounts(user_id);

-- -----------------------------------------------------------------------------
-- 3. savings_transactions — Buku besar mutasi saldo (APPEND-ONLY)
--
-- PENTING: Tabel ini adalah audit trail keuangan. Setelah row di-commit,
-- JANGAN UPDATE atau DELETE baris apapun. Koreksi dilakukan dengan insert
-- baris pembalik (reversal entry), bukan dengan mengubah data historis.
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS savings_transactions (
    id                  BIGSERIAL       PRIMARY KEY,

    -- Rekening yang mengalami mutasi.
    savings_account_id  BIGINT          NOT NULL
                            REFERENCES savings_accounts(id) ON DELETE RESTRICT,

    -- Jenis mutasi: deposit = uang masuk, withdraw = uang keluar.
    type                VARCHAR(20)     NOT NULL
                            CHECK (type IN ('deposit', 'withdraw')),

    -- Nominal transaksi — selalu positif. Arah uang ditentukan oleh kolom type.
    amount              DECIMAL(19,4)   NOT NULL
                            CHECK (amount > 0),

    -- ID referensi eksternal (nomor bukti setor, nomor transfer, dsb.).
    -- Boleh kosong untuk transaksi tunai tanpa nomor referensi.
    reference_id        VARCHAR(100)    NOT NULL DEFAULT '',

    -- created_at saja; tidak ada updated_at karena baris ini tidak pernah diubah.
    created_at          TIMESTAMPTZ     NOT NULL DEFAULT NOW()
);

-- Index untuk query mutasi berdasarkan rekening (paling sering dipakai)
CREATE INDEX IF NOT EXISTS idx_savings_transactions_account_id
    ON savings_transactions(savings_account_id);

-- Index untuk laporan keuangan berbasis rentang tanggal
CREATE INDEX IF NOT EXISTS idx_savings_transactions_created_at
    ON savings_transactions(created_at);
