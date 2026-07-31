# 📋 Analisis Backend Flow — Koperasi Digital

> **Tanggal Analisis:** 31 Juli 2026  
> **Scope:** User Register → Login → Transaksi (Simpanan, Pembiayaan, Emas Digital)  
> **Teknologi:** Go (Gin), PostgreSQL, Redis, Blockchain (Polygon EVM)

---

## 1. Arsitektur Umum

```
HTTP Client
    │
    ▼
[main.go] → setupRouter()
    │  ┌─────────────────────────────────────────────────────────────┐
    │  │ CORS → gin.Recovery() → gin.Logger() → RequireAuth (JWT)   │
    │  └─────────────────────────────────────────────────────────────┘
    │
    ├── Handler Layer  (titik masuk HTTP, validasi input)
    ├── Service Layer  (logika bisnis, orkestrasi)
    ├── Repository Layer (query database, cache Redis)
    └── Database (PostgreSQL + Redis)
              │
              └── GoldWorker (background goroutine, event-driven via BLPop)
                      │
                      └── Blockchain (Polygon EVM → Smart Contract CoopGold)
```

### Dependency Injection Chain (per domain)
```
DB/Redis
  └─ Repository
        └─ Service
              └─ Handler ── Router (main.go)
```

---

## 2. Flow: User Register

### 2.1 Endpoint
```
POST /api/v1/register
Body: { "nama_lengkap": "...", "email": "...", "password": "..." }
```

### 2.2 Alur Langkah
```
Client → UserHandler.Register()
            │
            ├─ [1] ShouldBindJSON — validasi:
            │       nama_lengkap: required, min=3
            │       email: required, format email valid
            │       password: required, min=8
            │
            ├─ [2] UserService.Register()
            │       │
            │       ├─ bcrypt.GenerateFromPassword(password, cost=12)
            │       │
            │       ├─ UserRepository.Insert()
            │       │       │
            │       │       └─ INSERT INTO users (nama_lengkap, email, password_hash)
            │       │          RETURNING id, role, wallet_address, status, created_at, updated_at
            │       │          (catch: pq unique violation → ErrEmailAlreadyExists)
            │       │
            │       └─ generateToken(user) → JWT (HS256, TTL dari config)
            │               Claims: user_id, email, iss="koperasi-digital", iat, exp
            │
            └─ Response 201: { token, user (tanpa password_hash) }

Error Mapping:
  400 → ShouldBindJSON gagal
  409 → ErrEmailAlreadyExists (email duplikat)
  500 → error tak terduga
```

### 2.3 Penilaian
| Aspek | Status | Catatan |
|---|---|---|
| Input Validation | ✅ Baik | `binding` tags (required, email, min) |
| Password Hashing | ✅ Baik | bcrypt cost=12 |
| Error Mapping HTTP | ✅ Baik | 400/409/500 |
| Password di response | ✅ Aman | `json:"-"` pada `PasswordHash` |
| User Enumeration | ✅ Aman | 409 response tidak bocorkan detail |

---

## 3. Flow: User Login

### 3.1 Endpoint
```
POST /api/v1/login
Body: { "email": "...", "password": "..." }
```

### 3.2 Alur Langkah
```
Client → UserHandler.Login()
            │
            ├─ [1] ShouldBindJSON — validasi email + password required
            │
            ├─ [2] UserService.Login()
            │       │
            │       ├─ UserRepository.FindByEmail(email)
            │       │       └─ SELECT ... FROM users WHERE email = $1
            │       │          (sql.ErrNoRows → ErrUserNotFound → ErrInvalidCredentials)
            │       │
            │       ├─ bcrypt.CompareHashAndPassword() → jika gagal: ErrInvalidCredentials
            │       │
            │       └─ generateToken(user) → JWT
            │
            └─ Response 200: { token, user }

Error Mapping:
  400 → validasi input gagal
  401 → email tidak ada ATAU password salah (sama sengaja — anti user-enumeration)
  500 → error tak terduga
```

### 3.3 Penilaian
| Aspek | Status | Catatan |
|---|---|---|
| Anti User Enumeration | ✅ Aman | Email-tidak-ada dan password-salah dikembalikan error yang sama |
| Algorithm Confusion Attack | ✅ Aman | Middleware cek `*jwt.SigningMethodHMAC` |
| Token Content | ✅ Minimal | Hanya user_id + email di claims |

---

## 4. Middleware: Autentikasi & Otorisasi

### 4.1 RequireAuth (JWT)
```
Authorization: Bearer <token>
    │
    ├─ [1] Cek header Authorization ada
    ├─ [2] Validasi format "Bearer <token>"
    ├─ [3] ParseWithClaims + keyFunc (cek algoritma HMAC)
    ├─ [4] Cek token.Valid + expiry (library jwt/v5 otomatis)
    └─ [5] Set context: user_id (int64), email (string)
```

### 4.2 RequireRole (RBAC)
```
Setelah RequireAuth:
    │
    ├─ [1] Ambil user_id dari context
    ├─ [2] SELECT role FROM users WHERE id = $1 (query DB per request)
    ├─ [3] Cek role ada dalam allowedRoles
    └─ [4] Set context: role (string)

Role levels: anggota | pengurus | admin | super_admin
Admin route: /api/v1/admin/* (pengurus, admin, super_admin)
```

| Aspek | Status | Catatan |
|---|---|---|
| Token freshness | ✅ Baik | Query DB per request, perubahan role langsung berlaku |
| Algorithm confusion | ✅ Dicegah | Cek `*jwt.SigningMethodHMAC` di keyFunc |
| RBAC | ✅ Ada | Tapi hanya 1 route admin (review financing) |

---

## 5. Flow: Modul Simpanan

### 5.1 Buka Rekening
```
POST /api/v1/savings/accounts (protected)
    │
    ├─ Validasi: savings_product_id (required, gt=0)
    ├─ SavingService.OpenAccount()
    │       ├─ FindProductByID → ErrSavingsProductNotFound jika tidak ada
    │       └─ CreateAccount (balance=0, status='active')
    └─ Response 201: SavingsAccount
```

### 5.2 Lihat Rekening
```
GET /api/v1/savings/accounts (protected)
    └─ GetAccountByUserID() → [] (empty jika belum ada, tidak error)
```

### 5.3 Setoran Dana
```
POST /api/v1/savings/deposit (protected)
Body: { "account_id": N, "amount": M, "reference_id": "..." }
    │
    ├─ [1] FindAccountByID + cek userID === account.UserID (ownership)
    ├─ [2] FindProductByID → cek amount >= product.MinDeposit
    ├─ [3] SavingRepository.Deposit() — ATOMIK:
    │       ├─ SELECT ... FOR UPDATE (kunci baris + cek status = 'active')
    │       ├─ UPDATE savings_accounts SET balance += amount
    │       └─ INSERT savings_transactions (type='deposit', reference_id)
    └─ Response 200: { "message": "setoran berhasil" }

Error Mapping:
  404 → rekening tidak ada / bukan milik user (sengaja sama, anti-enumeration)
  422 → rekening tidak aktif / nominal di bawah minimum
```

---

## 6. Flow: Modul Pembiayaan (Murabahah)

### 6.1 Pengajuan Pembiayaan
```
POST /api/v1/financing/apply (protected)
Body: { "principal_amount": N, "duration_months": M }
    │
    ├─ Hitung: margin_amount = principal × 10%
    ├─ Hitung: total_payable = principal + margin
    ├─ Generate: financing_number = "FIN-MRB-{UnixNano}"
    ├─ Insert ke DB status='pending'
    └─ Response 201: Financing

Constraint: fixed rate 10% murabahah.
```

### 6.2 Review Admin
```
PUT /api/v1/admin/financing/:id/review (admin only)
Body: { "action": "approve" | "reject" }
    │
    ├─ Validasi: pengajuan harus berstatus 'pending'
    ├─ approve:
    │   ├─ generateInstallments() — murabahah flat:
    │   │   ├─ per_angsuran = math.Round(total/n × 10000) / 10000
    │   │   ├─ angsuran terakhir = sisa (mencegah selisih pembulatan)
    │   │   └─ due_date = approval_date + N bulan
    │   └─ ApproveWithInstallments() — ATOMIK: update status + insert installments
    ├─ reject:
    │   └─ UpdateStatus('rejected') 
    └─ Response 200: Financing terbaru (re-query dari DB)
```

### 6.3 Pembayaran Cicilan
```
POST /api/v1/financing/installments/:id/pay (protected)
Body: { "savings_account_id": N }
    │
    ├─ [1] GetInstallmentByID + cek ownership via financing.user_id
    ├─ [2] Cek installment.Status !== 'paid'
    ├─ [3] FinancingRepository.PayInstallment() — ATOMIK:
    │       ├─ SELECT savings_account FOR UPDATE → ownership + status + saldo
    │       ├─ UPDATE savings_accounts SET balance -= amount_due
    │       ├─ INSERT savings_transactions (type='withdraw')
    │       ├─ UPDATE installments SET status='paid', amount_paid, paid_at
    │       └─ Cek: jika semua cicilan lunas → UPDATE financing status='paid'
    └─ Response 200: { "message": "pembayaran cicilan berhasil" }

Race Condition Guard: Validasi status='unpaid' juga dicek di dalam DB tx
(ErrInstallmentAlreadyPaid dari repository jika terjadi race condition)
```

---

## 7. Flow: Transaksi Emas Digital

### 7.1 Beli Emas
```
POST /api/v1/gold/buy (protected)
Body: { "gram_amount": 0.5, "savings_account_id": N }
    │
    ├─ [1] GoldService.BuyGold():
    │       ├─ GetCurrentPrice() → Redis cache (15 menit) ATAU PostgreSQL
    │       ├─ total_rupiah = math.Round(gram × buy_price × 10000) / 10000
    │       └─ GoldRepository.BuyWithDebit() — ATOMIK (1 DB transaction):
    │               ├─ SELECT savings_account FOR UPDATE (lock + validasi)
    │               ├─ Cek: user_id === account.user_id
    │               ├─ Cek: account.status === 'active'
    │               ├─ Cek: balance >= total_rupiah
    │               ├─ UPDATE savings_accounts SET balance -= total_rupiah
    │               ├─ INSERT gold_transactions (status='pending')
    │               └─ INSERT savings_transactions (type='withdraw', ref='gold_buy_{id}')
    │
    ├─ [2] RPush ke Redis "queue:gold_mint" (post-commit, non-fatal jika gagal)
    │
    └─ Response 201: GoldTransaction
```

### 7.2 Worker Blockchain (Asinkron)
```
GoldWorker.Start() [goroutine, BLPop blocking]
    │
    ├─ BLPop("queue:gold_mint") → txID
    ├─ GoldRepository.FindByID(txID)
    ├─ Guard: jika status !== 'pending' → skip
    ├─ mintOnChain(ctx, txID, gramAmount):
    │       ├─ Konversi gram → big.Int (× 10^4)
    │       ├─ contract.NewCoopGold(address, ethclient)
    │       ├─ coopGold.Mint(auth, recipientAddr, amount, goldTxID)
    │       │   [error → UpdateStatusAndHash('failed', '')]
    │       ├─ UpdateStatusAndHash('processing', txHash)
    │       └─ go awaitReceipt():
    │               ├─ bind.WaitMined() — tunggu konfirmasi blok
    │               ├─ receipt.Status == 1 (success):
    │               │   ├─ validateGoldMintedEvent() — verifikasi event log
    │               │   └─ UpdateTransactionStatus('success')
    │               └─ receipt.Status == 0 (revert):
    │                   └─ RefundFailedTransaction() — ATOMIK:
    │                           ├─ Lookup savings_account via reference_id
    │                           ├─ UPDATE savings_accounts SET balance += total_rupiah
    │                           ├─ INSERT savings_transactions (type='deposit', ref='gold_refund_{id}')
    │                           └─ UPDATE gold_transactions SET status='failed'
    └─ [Graceful Shutdown]: context cancel → BLPop unblock → return
```

---

## 8. Database Schema Overview

```
users
  id | nama_lengkap | email | password_hash | role | wallet_address | status | created_at | updated_at

savings_products
  id | name | min_deposit | ...

savings_accounts
  id | user_id | savings_product_id | balance | status | created_at | updated_at

savings_transactions
  id | savings_account_id | type | amount | reference_id | created_at
  (UNIQUE: reference_id — mencegah double-debit/refund)

financing
  id | financing_number | user_id | akad | principal_amount | margin_amount |
  total_payable | duration_months | status | reviewed_by | reviewed_at | created_at

financing_installments
  id | financing_id | installment_number | amount_due | amount_paid |
  due_date | paid_at | status

gold_prices
  id | buy_price_per_gram | sell_price_per_gram | updated_at

gold_transactions
  id | user_id | type | gram_amount | price_per_gram | total_rupiah |
  tx_hash | status | created_at
```

---

## 9. Ringkasan Kekuatan (What's Working Well)

| # | Aspek | Detail |
|---|---|---|
| 1 | **Layered Architecture** | Handler → Service → Repository — clean & testable |
| 2 | **Interface-based DI** | Setiap layer hanya expose interface, bukan struct konkret |
| 3 | **DB Atomicity** | BuyWithDebit, PayInstallment, RefundFailed semua dalam satu DB tx |
| 4 | **Anti Double-Spend** | `SELECT ... FOR UPDATE` pada savings_accounts |
| 5 | **Double-Refund Prevention** | UNIQUE constraint pada `reference_id` di savings_transactions |
| 6 | **Cache-Aside Gold Price** | Redis (TTL 15 menit) + fallback ke PostgreSQL |
| 7 | **Event-Driven Worker** | Redis BLPop — zero polling, zero CPU wasted |
| 8 | **Graceful Shutdown** | ctx cancel → BLPop unblock, HTTP srv.Shutdown(30s) |
| 9 | **Security Basics** | bcrypt cost=12, JWT HS256, password `json:"-"`, anti-enumeration |
| 10 | **CORS Configured** | Explicit origin, AllowCredentials, preflight cached 12h |
| 11 | **Algorithm Confusion Prevented** | keyFunc cek `*jwt.SigningMethodHMAC` |
| 12 | **Race Condition Guard** | PayInstallment cek ulang status di dalam DB tx |

---

## 10. 🚨 GAP Analysis — Masalah & Rekomendasi

### GAP-01: Recipient Wallet Address Hardcoded ke Owner
**Severity: KRITIS 🔴**  
**Lokasi:** `worker/gold_worker.go:253`

```go
// TODO: Ganti dengan wallet_address anggota dari tabel users.
recipientAddr := w.auth.From  // ← TOKEN DIKIRIM KE OWNER, BUKAN KE ANGGOTA!
```

**Dampak:** Token emas seluruh anggota di-mint ke wallet owner kontrak, bukan ke wallet anggota yang beli. Fitur emas tidak berfungsi secara finansial.

**Solusi:**
1. Wajib isi `wallet_address` saat register ATAU saat transaksi emas pertama.
2. Baca `wallet_address` user dari DB di `processTransaction()`.
3. Validasi `wallet_address` bukan nil sebelum mint.

---

### GAP-02: Tidak Ada Validasi `user.status` Saat Login & Transaksi
**Severity: TINGGI 🔴**  
**Lokasi:** `service/user_service.go`, `repository/saving_repository.go`

User dengan status `inactive` atau `banned` bisa login dan melakukan transaksi.  
Kolom `status` ada di tabel `users` (migration 007), tapi tidak pernah dicek di login flow.

**Solusi:**
```go
// Di UserService.Login(), setelah bcrypt berhasil:
if user.Status != "active" {
    return nil, ErrAccountSuspended
}
```
Dan di middleware atau di setiap operasi transaksi, validasi status user.

---

### GAP-03: Tidak Ada Mekanisme Recovery untuk Transaksi `processing` Setelah Restart
**Severity: TINGGI 🔴**  
**Lokasi:** `worker/gold_worker.go`, `main.go`

Saat server restart, transaksi dengan status `processing` (tx sudah dikirim ke chain, tapi `awaitReceipt` belum selesai) akan **tertinggal selamanya** di status `processing`.

`FindPending()` ada di repository tapi **tidak pernah dipanggil saat startup** untuk recovery.

**Solusi:**
```go
// Di main.go saat startup, sebelum Start():
pendingTxs, _ := goldRepo.FindPending(ctx) // recovery pending yang belum diqueue
for _, tx := range pendingTxs {
    redisClient.RPush(ctx, "queue:gold_mint", tx.ID)
}

// Tambahkan juga recovery untuk status 'processing':
// Query gold_transactions WHERE status='processing' → check on-chain status via tx_hash
```

---

### GAP-04: Endpoint `GET /gold/price` Mengembalikan Data Harga dengan Tipe `float64` (Presisi Loss Risk)
**Severity: SEDANG 🟡**  
**Lokasi:** `model/gold.go`

```go
BuyPricePerGram  float64  // float64 bisa kehilangan presisi untuk nilai besar
SellPricePerGram float64
```

Harga emas bisa mencapai Rp 1.698.000. Kalkulasi `gramAmount × price` menggunakan `float64` berpotensi akumulasi error pembulatan meski sudah ada `math.Round`.

**Solusi:** Gunakan `int64` (dalam unit rupiah penuh/satuan Rupiah terkecil) atau pakai library decimal seperti `shopspring/decimal` untuk operasi keuangan.

---

### GAP-05: `financing_number` Berpotensi Duplikat (Race Condition Tipis)
**Severity: SEDANG 🟡**  
**Lokasi:** `service/financing_service.go:109`

```go
FinancingNumber: fmt.Sprintf("FIN-MRB-%d", time.Now().UnixNano()),
```

Jika dua goroutine masuk pada nanosecond yang sama (sangat jarang tapi mungkin di multi-core load tinggi), `financing_number` bisa collision. Ada `ErrDuplicateFinancingNumber` di repository, tapi tidak ada **retry logic** — error ini mencapai user sebagai 500.

**Solusi:**
```go
// Tambahkan retry loop (max 3x) dengan backoff kecil:
for attempt := 0; attempt < 3; attempt++ {
    financing.FinancingNumber = fmt.Sprintf("FIN-MRB-%d-%d", time.Now().UnixNano(), attempt)
    saved, err = s.financingRepo.CreateFinancing(ctx, financing)
    if !errors.Is(err, repository.ErrDuplicateFinancingNumber) {
        break
    }
}
```

---

### GAP-06: Tidak Ada Rate Limiting
**Severity: SEDANG 🟡**  
**Lokasi:** `main.go` (router setup)

Endpoint login dan register tidak memiliki rate limiting. Penyerang bisa melakukan brute-force password atau spam register.

**Solusi:** Tambahkan middleware rate limiter (contoh: `golang.org/x/time/rate` atau `github.com/ulule/limiter`) per IP pada endpoint publik.

---

### GAP-07: Tidak Ada Logging Terstruktur (Structured Logging)
**Severity: SEDANG 🟡**  
**Lokasi:** Seluruh codebase

Saat ini menggunakan `log.Printf` (plain text). Di production sulit melakukan aggregation, filtering, dan alerting.

**Solusi:** Gunakan `slog` (Go 1.21 stdlib) atau `zerolog`/`zap`. Setiap log entry harus punya field: `request_id`, `user_id`, `duration`, `level`.

---

### GAP-08: Tidak Ada Endpoint untuk Sell (Jual) Emas
**Severity: SEDANG 🟡**  
**Lokasi:** `handler/gold_handler.go`, `service/gold_service.go`

Model `GoldTransaction` punya field `type: "buy" | "sell"` dan `GoldPrice` punya `sell_price_per_gram`, tapi **tidak ada endpoint `POST /gold/sell`** yang diimplementasikan.

---

### GAP-09: Tidak Ada Validasi Batas Transaksi Emas (Business Rule)
**Severity: SEDANG 🟡**  
**Lokasi:** `model/gold.go:93`

```go
// Komentar kode sendiri menyebut ini:
// Tidak ada batas maksimum di level validasi Gin —
// batas bisnis (misalnya maks 100 gram/hari) bisa ditambahkan di service layer.
```

Tidak ada batas minimum gram, maksimum per transaksi, atau limit harian.

---

### GAP-10: JWT Token Tidak Bisa di-Revoke (Logout Tidak Invalidate Token)
**Severity: SEDANG 🟡**  
**Lokasi:** Tidak ada endpoint logout

Tidak ada endpoint `POST /logout`. Token JWT akan terus valid sampai expired. Jika token dicuri, tidak ada cara untuk invalidate sebelum expired.

**Solusi:** Implementasikan token blocklist di Redis (`SET token_hash "revoked" EX ttl`). Middleware cek blocklist sebelum memvalidasi token.

---

### GAP-11: Margin Rate Hardcoded
**Severity: RINGAN 🟢**  
**Lokasi:** `service/financing_service.go:55`

```go
const murabahahMarginRate = 0.10  // 10% hardcoded
```

Jika koperasi ingin mengubah rate, harus recompile dan redeploy.

**Solusi:** Pindahkan ke tabel konfigurasi di database atau environment variable.

---

### GAP-12: Tidak Ada Endpoint Admin untuk Manajemen Harga Emas
**Severity: RINGAN 🟢**  
**Lokasi:** `handler/gold_handler.go`

Admin tidak bisa mengupdate harga emas melalui API. Harga harus dimasukkan langsung ke database.

---

### GAP-13: Tidak Ada Unit Test / Integration Test
**Severity: RINGAN 🟢**  
**Lokasi:** Seluruh codebase

Tidak ada file `_test.go` yang ditemukan. Arsitektur interface-based sudah mendukung testing dengan mock, tapi belum diimplementasikan.

---

### GAP-14: CORS `AllowCredentials + AllowOrigins` Perlu Konfigurasi Hati-Hati di Production
**Severity: RINGAN 🟢**  
**Lokasi:** `main.go:187`

Saat ini origin diambil dari env `FRONTEND_URL`. Jika env tidak diset, origin bisa kosong atau wildcard — perlu validasi saat startup.

---

## 11. Ringkasan GAP Priority

| Priority | GAP | Severity | Impact |
|---|---|---|---|
| 🔴 P0 | GAP-01: Wallet address hardcoded ke owner | Kritis | Token emas salah tujuan |
| 🔴 P0 | GAP-02: Status akun tidak dicek saat login | Tinggi | Akun banned bisa login |
| 🔴 P1 | GAP-03: Tidak ada recovery saat restart | Tinggi | Transaksi stuck 'processing' |
| 🟡 P2 | GAP-04: float64 untuk nilai finansial | Sedang | Presisi kalkulasi |
| 🟡 P2 | GAP-05: financing_number race condition | Sedang | Rare tapi bisa 500 |
| 🟡 P2 | GAP-06: Tidak ada rate limiting | Sedang | Brute force risiko |
| 🟡 P2 | GAP-07: Tidak ada structured logging | Sedang | Ops monitoring sulit |
| 🟡 P2 | GAP-08: Tidak ada endpoint jual emas | Sedang | Fitur tidak lengkap |
| 🟡 P2 | GAP-09: Tidak ada batasan transaksi emas | Sedang | Business rule missing |
| 🟡 P2 | GAP-10: JWT tidak bisa di-revoke | Sedang | Security gap |
| 🟢 P3 | GAP-11: Margin rate hardcoded | Ringan | Fleksibilitas rendah |
| 🟢 P3 | GAP-12: Tidak ada admin endpoint harga emas | Ringan | Ops manual |
| 🟢 P3 | GAP-13: Tidak ada unit test | Ringan | Kualitas jangka panjang |
| 🟢 P3 | GAP-14: CORS validation | Ringan | Edge case production |

---

## 12. Kesimpulan

Backend Koperasi Digital telah memiliki **fondasi arsitektur yang sangat solid** dengan:
- Layered architecture yang bersih (Handler → Service → Repository)
- Atomisitas database yang dijaga dengan baik (DB transactions + FOR UPDATE)
- Security dasar yang benar (bcrypt, JWT, anti-enumeration, algorithm confusion prevention)
- Event-driven worker yang efisien (BLPop, zero polling)
- Graceful shutdown terstruktur

Namun **belum production-ready** karena ada **2 GAP kritis** (P0):
1. Token emas di-mint ke wallet salah (owner, bukan anggota)
2. Akun suspended/banned tetap bisa login dan bertransaksi

Dan beberapa **GAP signifikan** (P1/P2) yang perlu diselesaikan sebelum go-live di lingkungan produksi nyata.

---
*Analisis dilakukan berdasarkan source code per 31 Juli 2026.*
