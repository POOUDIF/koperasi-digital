-- =============================================================================
-- Migrasi: RBAC + Kolom Review Pembiayaan
--
-- Perubahan:
--   1. Tambah kolom `role` ke tabel `users` untuk RBAC.
--   2. Tambah kolom `reviewed_by` dan `reviewed_at` ke tabel `financing`
--      untuk mencatat siapa admin yang mereview dan kapan.
-- =============================================================================

-- -----------------------------------------------------------------------------
-- 1. Tambah kolom role ke tabel users
--
-- Role yang didukung:
--   anggota    : user biasa, bisa mengajukan simpanan & pembiayaan
--   pengurus   : bisa mereview pembiayaan
--   admin      : bisa mereview pembiayaan + manajemen produk
--   super_admin: akses penuh ke semua fitur administrasi
--
-- DEFAULT 'anggota' memastikan semua user lama otomatis mendapat role terendah
-- tanpa perlu UPDATE manual setelah migrasi dijalankan.
-- -----------------------------------------------------------------------------
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS role VARCHAR(20) NOT NULL DEFAULT 'anggota'
        CHECK (role IN ('anggota', 'pengurus', 'admin', 'super_admin'));

-- -----------------------------------------------------------------------------
-- 2. Tambah kolom audit ke tabel financing
--
-- reviewed_by : foreign key ke user yang melakukan approve/reject.
--               NULL selama status masih 'pending'.
-- reviewed_at : timestamp kapan keputusan dibuat.
--               NULL selama status masih 'pending'.
-- -----------------------------------------------------------------------------
ALTER TABLE financing
    ADD COLUMN IF NOT EXISTS reviewed_by BIGINT
        REFERENCES users(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS reviewed_at TIMESTAMPTZ;

-- Index untuk mencari semua review yang dilakukan oleh admin tertentu
-- (berguna untuk audit trail dan laporan kinerja komite pembiayaan).
CREATE INDEX IF NOT EXISTS idx_financing_reviewed_by
    ON financing(reviewed_by)
    WHERE reviewed_by IS NOT NULL;
