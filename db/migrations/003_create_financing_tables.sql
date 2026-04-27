-- =============================================================================
-- Migrasi: Modul Pembiayaan Syariah — Akad Murabahah
--
-- Murabahah adalah akad jual-beli di mana koperasi membeli barang yang
-- dibutuhkan anggota, lalu menjualnya kembali dengan harga pokok + margin
-- keuntungan yang disepakati di awal. Harga jual dan margin TIDAK BOLEH
-- berubah setelah akad ditandatangani — prinsip transparansi syariah.
--
-- Dua tabel yang dibuat:
--   1. financing             — master data setiap pengajuan pembiayaan.
--   2. financing_installments— jadwal angsuran yang dihasilkan dari master.
--
-- Semua kolom nominal uang pakai DECIMAL(19,4) untuk menghindari
-- floating-point rounding error yang tidak dapat diterima di sistem keuangan.
-- =============================================================================

-- -----------------------------------------------------------------------------
-- 1. financing — Master pengajuan pembiayaan
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS financing (
    id                BIGSERIAL       PRIMARY KEY,

    -- Nomor unik yang dicetak di dokumen akad, format: FIN-MRB-<timestamp>.
    -- UNIQUE constraint menjamin tidak ada dua pengajuan dengan nomor sama.
    financing_number  VARCHAR(50)     NOT NULL UNIQUE,

    -- Anggota yang mengajukan pembiayaan.
    -- RESTRICT mencegah penghapusan user yang masih punya pembiayaan aktif.
    user_id           BIGINT          NOT NULL
                          REFERENCES users(id) ON DELETE RESTRICT,

    -- Jenis akad syariah. Saat ini hanya 'murabahah'; kolom ini disiapkan
    -- untuk ekspansi ke akad lain (musyarakah, ijarah, dsb.) di masa depan.
    akad              VARCHAR(30)     NOT NULL
                          CHECK (akad IN ('murabahah')),

    -- Harga pokok barang yang dibiayai koperasi (harga beli koperasi).
    principal_amount  DECIMAL(19,4)   NOT NULL
                          CHECK (principal_amount > 0),

    -- Keuntungan koperasi yang disepakati di awal dan TIDAK BOLEH berubah.
    margin_amount     DECIMAL(19,4)   NOT NULL
                          CHECK (margin_amount >= 0),

    -- Harga jual ke anggota = principal_amount + margin_amount.
    -- Di-store agar tidak perlu hitung ulang dan mencegah perubahan margin
    -- di kemudian hari memengaruhi nilai yang sudah disepakati.
    total_payable     DECIMAL(19,4)   NOT NULL
                          CHECK (total_payable > 0),

    -- Tenor dalam bulan (1–360).
    duration_months   INT             NOT NULL
                          CHECK (duration_months BETWEEN 1 AND 360),

    -- Alur status: pending → approved → active → paid
    --   pending  : menunggu persetujuan admin/komite
    --   approved : disetujui, menunggu pencairan
    --   active   : dana sudah cair, angsuran berjalan
    --   paid     : lunas
    --   rejected : ditolak komite (terminal state)
    status            VARCHAR(20)     NOT NULL DEFAULT 'pending'
                          CHECK (status IN ('pending', 'approved', 'active', 'paid', 'rejected')),

    created_at        TIMESTAMPTZ     NOT NULL DEFAULT NOW()
    -- Tidak ada updated_at — perubahan status dicatat via tabel audit terpisah
    -- di iterasi berikutnya untuk menjaga immutability record awal.
);

-- Index untuk dashboard admin: daftar pengajuan berdasarkan status
CREATE INDEX IF NOT EXISTS idx_financing_status
    ON financing(status);

-- Index untuk endpoint "pembiayaan saya": semua pengajuan milik satu user
CREATE INDEX IF NOT EXISTS idx_financing_user_id
    ON financing(user_id);

-- -----------------------------------------------------------------------------
-- 2. financing_installments — Jadwal angsuran per pengajuan
--
-- Dibuat oleh sistem saat admin men-approve pembiayaan (status → active).
-- Setiap baris mewakili satu bulan angsuran dengan tanggal jatuh tempo,
-- nominal yang harus dibayar, dan berapa yang sudah dibayar.
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS financing_installments (
    id                 BIGSERIAL       PRIMARY KEY,

    -- Relasi ke master pembiayaan.
    financing_id       BIGINT          NOT NULL
                           REFERENCES financing(id) ON DELETE RESTRICT,

    -- Urutan angsuran: 1 untuk bulan pertama, dst.
    installment_number INT             NOT NULL
                           CHECK (installment_number >= 1),

    -- Nominal angsuran sesuai jadwal. Untuk murabahah flat:
    -- amount_due = total_payable / duration_months (dibulatkan sesuai ketentuan).
    amount_due         DECIMAL(19,4)   NOT NULL
                           CHECK (amount_due > 0),

    -- Nominal yang sudah dibayarkan anggota. Default 0 saat baris dibuat.
    amount_paid        DECIMAL(19,4)   NOT NULL DEFAULT 0
                           CHECK (amount_paid >= 0),

    -- Tanggal jatuh tempo angsuran (date, bukan timestamp).
    due_date           DATE            NOT NULL,

    -- unpaid: belum dibayar / belum jatuh tempo
    -- paid   : sudah lunas
    status             VARCHAR(20)     NOT NULL DEFAULT 'unpaid'
                           CHECK (status IN ('unpaid', 'paid')),

    -- Satu financing tidak boleh punya dua angsuran dengan nomor sama.
    UNIQUE (financing_id, installment_number)
);

-- Index untuk query "angsuran yang harus dibayar bulan ini"
CREATE INDEX IF NOT EXISTS idx_financing_installments_due_date
    ON financing_installments(due_date);

-- Index untuk mengambil semua angsuran milik satu pengajuan
CREATE INDEX IF NOT EXISTS idx_financing_installments_financing_id
    ON financing_installments(financing_id);
