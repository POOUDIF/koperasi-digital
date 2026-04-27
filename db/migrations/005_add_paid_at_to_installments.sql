-- =============================================================================
-- Migrasi: Tambah kolom paid_at ke financing_installments
--
-- Kolom ini mencatat kapan anggota melunasi satu angsuran.
-- NULL selama status masih 'unpaid'; diisi dengan timestamp saat pembayaran
-- berhasil dikonfirmasi dan status berubah menjadi 'paid'.
-- =============================================================================

ALTER TABLE financing_installments
    ADD COLUMN IF NOT EXISTS paid_at TIMESTAMPTZ;

-- Komentar: kolom ini sengaja tidak NOT NULL karena secara default NULL
-- (belum dibayar). Constraint bisnis — jika status='paid' maka paid_at IS NOT NULL
-- — dijaga oleh application layer, bukan DB constraint, untuk fleksibilitas migrasi.
