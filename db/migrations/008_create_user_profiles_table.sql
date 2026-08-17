-- =============================================================================
-- Migrasi 008: Tabel User Profiles (KYC)
--
-- Menyimpan kelengkapan data diri anggota koperasi untuk keperluan
-- KYC (Know Your Customer) dan analisa risiko kredit/pembiayaan.
-- =============================================================================

CREATE TABLE IF NOT EXISTS user_profiles (
    user_id                 BIGINT          PRIMARY KEY,
    
    -- NIK standar KTP Indonesia (16 digit)
    nik                     VARCHAR(16)     NOT NULL UNIQUE,
    
    phone_number            VARCHAR(20)     NOT NULL,
    address                 TEXT            NOT NULL,
    
    -- Pekerjaan & Penghasilan untuk scoring pembiayaan
    job_title               VARCHAR(100)    NOT NULL,
    monthly_income          DECIMAL(15, 2)  NOT NULL DEFAULT 0,
    
    -- Kontak darurat
    emergency_contact_name  VARCHAR(150)    NOT NULL,
    emergency_contact_phone VARCHAR(20)     NOT NULL,

    created_at              TIMESTAMPTZ     NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ     NOT NULL DEFAULT NOW(),

    -- Foreign Key ke tabel users
    CONSTRAINT fk_user_profiles_user_id
        FOREIGN KEY (user_id)
        REFERENCES users (id)
        ON DELETE CASCADE
);

-- Trigger untuk auto-update kolom updated_at setiap kali baris diubah.
CREATE OR REPLACE TRIGGER user_profiles_updated_at
    BEFORE UPDATE ON user_profiles
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();
