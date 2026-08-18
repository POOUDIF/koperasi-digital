-- Migration 010: Create Deposit Requests Table
-- Digunakan untuk menampung permohonan setor dana sebelum diverifikasi oleh admin.

CREATE TABLE deposit_requests (
    id SERIAL PRIMARY KEY,
    user_id INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    savings_account_id INT NOT NULL REFERENCES savings_accounts(id) ON DELETE CASCADE,
    amount NUMERIC(15, 2) NOT NULL CHECK (amount > 0),
    payment_method VARCHAR(50) NOT NULL,
    proof_image_url VARCHAR(255),
    status VARCHAR(20) NOT NULL DEFAULT 'pending',
    reference_id VARCHAR(100),
    reviewed_by INT REFERENCES users(id) ON DELETE SET NULL,
    reviewed_at TIMESTAMP,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Index untuk mempercepat query list
CREATE INDEX idx_deposit_requests_user_id ON deposit_requests(user_id);
CREATE INDEX idx_deposit_requests_status ON deposit_requests(status);
