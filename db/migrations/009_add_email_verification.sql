-- =============================================================================
-- Migrasi 009: Verifikasi Email
--
-- Menambahkan kolom is_email_verified untuk menandai apakah user sudah
-- memverifikasi email mereka lewat OTP.
-- =============================================================================

ALTER TABLE users 
ADD COLUMN IF NOT EXISTS is_email_verified BOOLEAN NOT NULL DEFAULT FALSE;

-- Update data existing agar user lama tetap bisa login tanpa harus verifikasi ulang
-- Ini opsional tapi berguna saat development/produksi jika sudah ada data.
UPDATE users SET is_email_verified = TRUE WHERE is_email_verified = FALSE;
