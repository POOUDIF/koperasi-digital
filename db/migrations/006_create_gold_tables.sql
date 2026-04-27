-- =============================================================================
-- Migrasi: Modul Jual Beli Emas Digital (Web3 Integration)
--
-- Dua tabel yang dibuat:
--   1. gold_prices        — riwayat harga emas per gram (harga beli & jual koperasi).
--   2. gold_transactions  — setiap transaksi beli/jual emas oleh anggota.
--
-- Alur Transaksi (tahap awal / off-chain):
--   POST /gold/buy
--     → potong saldo Wadiah (savings_accounts)
--     → insert gold_transactions (status: pending, tx_hash: NULL)
--
-- Alur Blockchain (tahap berikutnya — belum diimplementasi):
--   Worker/job memanggil Smart Contract di Polygon
--     → Smart Contract merespon dengan tx_hash
--     → UPDATE gold_transactions SET status='success', tx_hash=$1
--
-- Semua kolom nominal uang dan berat menggunakan DECIMAL untuk menghindari
-- floating-point rounding error yang tidak dapat diterima di sistem keuangan.
-- =============================================================================

-- -----------------------------------------------------------------------------
-- 1. gold_prices — Harga pasar emas per gram yang ditetapkan koperasi
--
-- Tabel ini diisi secara periodik (manual atau via price-feed oracle) oleh admin.
-- Endpoint GET /gold/price selalu mengambil baris terbaru (ORDER BY updated_at DESC).
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS gold_prices (
    id                  BIGSERIAL       PRIMARY KEY,

    -- Harga beli emas: harga yang dibayar anggota saat membeli emas dari koperasi.
    -- Biasanya lebih tinggi dari sell_price_per_gram (spread = keuntungan koperasi).
    buy_price_per_gram  DECIMAL(19,4)   NOT NULL
                            CHECK (buy_price_per_gram > 0),

    -- Harga jual emas: harga yang diterima anggota saat menjual emas ke koperasi.
    sell_price_per_gram DECIMAL(19,4)   NOT NULL
                            CHECK (sell_price_per_gram > 0),

    -- Timestamp kapan harga ini di-set. Dipakai sebagai basis ORDER BY untuk
    -- menemukan harga terkini.
    updated_at          TIMESTAMPTZ     NOT NULL DEFAULT NOW()
);

-- Index untuk query harga terkini (ORDER BY updated_at DESC LIMIT 1)
CREATE INDEX IF NOT EXISTS idx_gold_prices_updated_at
    ON gold_prices(updated_at DESC);

-- -----------------------------------------------------------------------------
-- 2. gold_transactions — Setiap transaksi jual/beli emas oleh anggota
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS gold_transactions (
    id              BIGSERIAL       PRIMARY KEY,

    -- Anggota yang melakukan transaksi.
    -- RESTRICT mencegah penghapusan user yang masih punya riwayat transaksi emas.
    user_id         BIGINT          NOT NULL
                        REFERENCES users(id) ON DELETE RESTRICT,

    -- Arah transaksi dari sudut pandang anggota:
    --   buy  : anggota membeli emas dari koperasi (saldo dikurangi).
    --   sell : anggota menjual emas ke koperasi (saldo ditambah). [belum diimplementasi]
    type            VARCHAR(10)     NOT NULL
                        CHECK (type IN ('buy', 'sell')),

    -- Berat emas dalam satuan gram, presisi 4 desimal (misal: 0.5000 gram).
    gram_amount     DECIMAL(10,4)   NOT NULL
                        CHECK (gram_amount > 0),

    -- Harga per gram yang berlaku saat transaksi dikunci.
    -- Disimpan di sini agar perubahan harga di gold_prices tidak memengaruhi
    -- nilai transaksi yang sudah terjadi — prinsip immutability harga akad.
    price_per_gram  DECIMAL(19,4)   NOT NULL
                        CHECK (price_per_gram > 0),

    -- Nilai total dalam Rupiah = gram_amount × price_per_gram.
    -- Dipersist agar tidak perlu hitung ulang dan akurat meski ada perubahan harga.
    total_rupiah    DECIMAL(19,4)   NOT NULL
                        CHECK (total_rupiah > 0),

    -- Hash transaksi on-chain (dari Smart Contract Polygon).
    -- NULL selama status masih 'pending' / 'processing' (belum dikirim ke blockchain).
    -- Diisi oleh worker/job setelah transaksi blockchain dikonfirmasi.
    tx_hash         VARCHAR(100),

    -- Alur status:
    --   pending    : saldo sudah dipotong, menunggu antrian blockchain.
    --   processing : transaksi sudah dikirim ke Polygon, menunggu konfirmasi blok.
    --   success    : transaksi dikonfirmasi on-chain, token emas sudah diterbitkan.
    --   failed     : gagal di sisi blockchain; saldo di-refund ke rekening asal.
    status          VARCHAR(20)     NOT NULL DEFAULT 'pending'
                        CHECK (status IN ('pending', 'processing', 'success', 'failed')),

    created_at      TIMESTAMPTZ     NOT NULL DEFAULT NOW()
);

-- Index untuk halaman riwayat transaksi per user
CREATE INDEX IF NOT EXISTS idx_gold_transactions_user_id
    ON gold_transactions(user_id);

-- Index untuk worker/job yang memproses transaksi pending secara batch
CREATE INDEX IF NOT EXISTS idx_gold_transactions_status
    ON gold_transactions(status)
    WHERE status IN ('pending', 'processing');

-- -----------------------------------------------------------------------------
-- Seed: 1 data harga emas hari ini (16 April 2026)
--
-- Referensi: harga emas Antam 16 April 2026.
--   buy_price  : harga anggota membeli emas dari koperasi (harga jual koperasi)
--   sell_price : harga anggota menjual emas ke koperasi (harga beli koperasi)
-- -----------------------------------------------------------------------------
INSERT INTO gold_prices (buy_price_per_gram, sell_price_per_gram, updated_at)
VALUES (1698000.0000, 1672000.0000, NOW());
