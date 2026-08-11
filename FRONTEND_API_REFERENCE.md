# 📘 Frontend API Reference — Koperasi Digital

> **Dibuat:** 11 Agustus 2026  
> **Base URL:** `http://localhost:8080/api/v1` (development)  
> **Auth:** JWT Bearer Token  
> **Content-Type:** `application/json`

---

## Daftar Isi

1. [Arsitektur & Middleware](#1-arsitektur--middleware)
2. [Error Response Format](#2-error-response-format)
3. [Auth — Register, Login, Logout, Profile](#3-auth)
4. [Modul Simpanan](#4-modul-simpanan)
5. [Modul Pembiayaan](#5-modul-pembiayaan)
6. [Modul Emas Digital](#6-modul-emas-digital)
7. [Admin Endpoints](#7-admin-endpoints)
8. [Public Endpoints](#8-public-endpoints)
9. [Data Models Lengkap](#9-data-models-lengkap)
10. [Status & Enum Values](#10-status--enum-values)
11. [Penanganan Token di Frontend](#11-penanganan-token-di-frontend)

---

## 1. Arsitektur & Middleware

### Middleware Stack

Semua protected route melewati **dua lapis middleware** secara berurutan:

```
Request
  │
  ├─ [1] RequireAuth         → Validasi JWT Bearer token
  │         ├─ Cek header Authorization ada
  │         ├─ Cek token tidak ada di Redis blocklist (sudah di-logout)
  │         ├─ Parse & verifikasi JWT signature (HMAC)
  │         └─ Inject user_id & email ke request context
  │
  └─ [2] RequireActiveUserDB → Query DB, pastikan akun masih active
              ├─ Jika user 'inactive' atau 'banned': 403 Forbidden
              └─ Jika user tidak ditemukan: 401 Unauthorized
```

> **Implikasi Frontend:** Jika mendapat **403** dari route protected (bukan login), itu berarti akun user telah dinonaktifkan/diblokir oleh admin. Hapus token lokal dan tampilkan pesan untuk menghubungi admin.

### Route Groups

| Group | Prefix | Middleware |
|---|---|---|
| Public | `/api/v1` | Rate limiter (login/register) |
| Protected | `/api/v1` | RequireAuth + RequireActiveUserDB |
| Admin | `/api/v1/admin` | RequireAuth + RequireRole (pengurus/admin/super_admin) |

### CORS

| Setting | Nilai |
|---|---|
| `Allow-Origin` | `FRONTEND_URL` env var (default: `http://localhost:3000`) |
| `Allow-Methods` | GET, POST, PUT, DELETE, OPTIONS |
| `Allow-Headers` | `Authorization`, `Content-Type` |
| `Allow-Credentials` | `true` |
| Preflight cache | 12 jam |

---

## 2. Error Response Format

Semua error menggunakan format JSON yang **konsisten**:

```json
{ "error": "pesan kesalahan yang bisa ditampilkan ke user" }
```

### HTTP Status Codes yang Digunakan

| Code | Arti | Kapan Terjadi |
|---|---|---|
| `200` | OK | GET berhasil, operasi berhasil |
| `201` | Created | Resource baru berhasil dibuat |
| `400` | Bad Request | Body JSON tidak valid / field wajib kosong / format salah |
| `401` | Unauthorized | Token tidak ada / tidak valid / sudah expired / sudah logout |
| `403` | Forbidden | Token valid tapi akun `inactive` atau `banned` |
| `404` | Not Found | Resource tidak ditemukan |
| `409` | Conflict | Data sudah ada (misal: email duplikat, cicilan sudah dibayar) |
| `422` | Unprocessable Entity | Request valid tapi kondisi bisnis tidak terpenuhi (saldo kurang, rekening tidak aktif) |
| `429` | Too Many Requests | Rate limit tercapai (3 req/detik, burst maks 5) |
| `500` | Internal Server Error | Error tak terduga di server |
| `503` | Service Unavailable | Harga emas belum diset admin |

---

## 3. Auth

### POST `/register`

Mendaftarkan anggota baru. **Tidak butuh token.**

**Rate limit:** 3 req/detik per IP, burst maks 5.

**Request Body:**
```json
{
  "nama_lengkap": "Ahmad Rifai Santoso",
  "email": "ahmad@koperasi.id",
  "password": "rahasia123"
}
```

| Field | Validasi |
|---|---|
| `nama_lengkap` | Required, min 3 karakter |
| `email` | Required, format email valid |
| `password` | Required, min 8 karakter |

**Response `201`:**
```json
{
  "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "user": {
    "id": 42,
    "nama_lengkap": "Ahmad Rifai Santoso",
    "email": "ahmad@koperasi.id",
    "role": "anggota",
    "wallet_address": null,
    "status": "active",
    "created_at": "2026-08-11T07:20:00Z",
    "updated_at": "2026-08-11T07:20:00Z"
  }
}
```

**Error Responses:**
| Code | Kondisi |
|---|---|
| `400` | Field wajib kosong / email format salah / password < 8 char |
| `409` | Email sudah terdaftar |
| `500` | Error server |

---

### POST `/login`

Login anggota. **Tidak butuh token.**

**Rate limit:** 3 req/detik per IP, burst maks 5.

**Request Body:**
```json
{
  "email": "ahmad@koperasi.id",
  "password": "rahasia123"
}
```

**Response `200`:**
```json
{
  "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "user": {
    "id": 42,
    "nama_lengkap": "Ahmad Rifai Santoso",
    "email": "ahmad@koperasi.id",
    "role": "anggota",
    "wallet_address": null,
    "status": "active",
    "created_at": "2026-08-11T07:00:00Z",
    "updated_at": "2026-08-11T07:00:00Z"
  }
}
```

**Error Responses:**
| Code | Kondisi |
|---|---|
| `400` | Field kosong / email format salah |
| `401` | Email tidak terdaftar ATAU password salah (sengaja sama, anti-enumeration) |
| `403` | Akun `inactive` atau `banned` — tampilkan pesan "Akun Anda telah dinonaktifkan" |
| `500` | Error server |

---

### POST `/logout` 🔒

Invalidasi token yang sedang digunakan. Token akan masuk blocklist Redis sampai expired.

**Header:** `Authorization: Bearer <token>`

**Request Body:** _(kosong)_

**Response `200`:**
```json
{ "message": "logout berhasil" }
```

> **Frontend:** Setelah menerima `200`, hapus token dari storage lokal (localStorage/cookie) dan redirect ke halaman login.

**Error Responses:**
| Code | Kondisi |
|---|---|
| `401` | Token tidak ada / tidak valid |
| `500` | Error saat memproses logout di Redis |

---

### GET `/profile` 🔒

Mengambil data profil user yang sedang login.

**Header:** `Authorization: Bearer <token>`

**Response `200`:**
```json
{
  "id": 42,
  "nama_lengkap": "Ahmad Rifai Santoso",
  "email": "ahmad@koperasi.id",
  "role": "anggota",
  "wallet_address": null,
  "status": "active",
  "created_at": "2026-08-11T07:00:00Z",
  "updated_at": "2026-08-11T07:00:00Z"
}
```

**Error Responses:**
| Code | Kondisi |
|---|---|
| `401` | Token tidak valid / sudah expired |
| `403` | Akun tidak aktif (dikunci setelah token diterbitkan) |
| `404` | User tidak ditemukan (sangat jarang) |

---

## 4. Modul Simpanan

### POST `/savings/accounts` 🔒

Membuka rekening simpanan baru.

**Request Body:**
```json
{ "savings_product_id": 1 }
```

| Field | Validasi |
|---|---|
| `savings_product_id` | Required, bilangan bulat > 0 |

**Response `201`:**
```json
{
  "id": 7,
  "user_id": 42,
  "savings_product_id": 1,
  "balance": 0,
  "status": "active",
  "created_at": "2026-08-11T07:25:00Z",
  "updated_at": "2026-08-11T07:25:00Z"
}
```

**Error Responses:**
| Code | Kondisi |
|---|---|
| `400` | `savings_product_id` tidak ada / tidak valid |
| `404` | Produk simpanan tidak ditemukan |

---

### GET `/savings/accounts` 🔒

Mengambil semua rekening simpanan milik user yang login.

**Response `200`:**
```json
{
  "accounts": [
    {
      "id": 7,
      "user_id": 42,
      "savings_product_id": 1,
      "balance": 500000,
      "status": "active",
      "created_at": "2026-08-11T07:25:00Z",
      "updated_at": "2026-08-11T08:00:00Z"
    }
  ]
}
```

> **Catatan:** Jika user belum punya rekening, `accounts` adalah array kosong `[]`, bukan error.

---

### POST `/savings/deposit` 🔒

Menyetor dana ke rekening simpanan.

**Request Body:**
```json
{
  "account_id": 7,
  "amount": 500000,
  "reference_id": "TRF-20260811-001"
}
```

| Field | Validasi | Keterangan |
|---|---|---|
| `account_id` | Required, > 0 | ID rekening yang dituju |
| `amount` | Required, > 0 | Nominal setoran dalam Rupiah |
| `reference_id` | Opsional | Nomor referensi transfer/bukti setor |

**Response `200`:**
```json
{ "message": "setoran berhasil" }
```

**Error Responses:**
| Code | Kondisi |
|---|---|
| `400` | Field tidak valid |
| `404` | Rekening tidak ditemukan atau bukan milik user ini |
| `422` | Rekening tidak aktif (frozen/closed) ATAU nominal di bawah minimum produk |

---

## 5. Modul Pembiayaan

### POST `/financing/apply` 🔒

Mengajukan pembiayaan Murabahah baru.

**Request Body:**
```json
{
  "principal_amount": 10000000,
  "duration_months": 12
}
```

| Field | Validasi | Keterangan |
|---|---|---|
| `principal_amount` | Required, > 0 | Nominal pokok yang diajukan (Rupiah) |
| `duration_months` | Required, 1–360 | Tenor dalam bulan |

**Response `201`:**
```json
{
  "id": 3,
  "financing_number": "FIN-MRB-1723366800123456789",
  "user_id": 42,
  "akad": "murabahah",
  "principal_amount": 10000000,
  "margin_amount": 1000000,
  "total_payable": 11000000,
  "duration_months": 12,
  "status": "pending",
  "reviewed_by": null,
  "reviewed_at": null,
  "created_at": "2026-08-11T07:30:00Z"
}
```

> **Catatan Kalkulasi Margin:** Margin = `principal_amount × margin_rate` (saat ini 10%, dikonfigurasi via env `MURABAHAH_MARGIN_RATE`). Nilai ini sudah dihitung dan disimpan oleh backend — frontend cukup menampilkannya.

---

### GET `/financing` 🔒

Mengambil semua pengajuan pembiayaan milik user yang login.

**Response `200`:**
```json
{
  "financings": [
    {
      "id": 3,
      "financing_number": "FIN-MRB-1723366800123456789",
      "user_id": 42,
      "akad": "murabahah",
      "principal_amount": 10000000,
      "margin_amount": 1000000,
      "total_payable": 11000000,
      "duration_months": 12,
      "status": "approved",
      "reviewed_by": 1,
      "reviewed_at": "2026-08-11T08:00:00Z",
      "created_at": "2026-08-11T07:30:00Z"
    }
  ]
}
```

---

### GET `/financing/:id/installments` 🔒

Mengambil jadwal cicilan untuk satu pengajuan pembiayaan.

**URL Param:** `id` — ID pembiayaan (integer)

**Response `200`:**
```json
{
  "installments": [
    {
      "id": 25,
      "financing_id": 3,
      "installment_number": 1,
      "amount_due": 916666.6667,
      "amount_paid": 916666.6667,
      "due_date": "2026-09-11T00:00:00Z",
      "status": "paid",
      "paid_at": "2026-09-10T14:00:00Z"
    },
    {
      "id": 26,
      "financing_id": 3,
      "installment_number": 2,
      "amount_due": 916666.6667,
      "amount_paid": 0,
      "due_date": "2026-10-11T00:00:00Z",
      "status": "unpaid",
      "paid_at": null
    }
  ]
}
```

> **Catatan Angsuran Terakhir:** Angsuran terakhir mungkin sedikit berbeda dari angsuran lainnya karena penyesuaian pembulatan agar total tepat sama dengan `total_payable`.

**Error Responses:**
| Code | Kondisi |
|---|---|
| `400` | `id` bukan integer valid |
| `404` | Pembiayaan tidak ditemukan atau bukan milik user ini |

---

### POST `/financing/installments/:id/pay` 🔒

Membayar satu angsuran cicilan. Dana didebet dari rekening simpanan.

**URL Param:** `id` — ID installment (bukan ID financing)

**Request Body:**
```json
{ "savings_account_id": 7 }
```

| Field | Validasi |
|---|---|
| `savings_account_id` | Required, > 0 |

**Response `200`:**
```json
{ "message": "pembayaran cicilan berhasil" }
```

**Error Responses:**
| Code | Kondisi |
|---|---|
| `400` | `id` tidak valid / field kosong |
| `404` | Cicilan tidak ditemukan ATAU rekening simpanan tidak ditemukan |
| `409` | Cicilan sudah dibayar sebelumnya |
| `422` | Saldo rekening tidak mencukupi ATAU rekening tidak aktif |

---

## 6. Modul Emas Digital

### GET `/gold/price` *(Public)*

Mengambil harga emas terkini. **Tidak butuh token.**

> Data di-cache di Redis selama **15 menit**. Jika Redis tidak punya data, fallback ke PostgreSQL.

**Response `200`:**
```json
{
  "id": 15,
  "buy_price_per_gram": 1698000,
  "sell_price_per_gram": 1685000,
  "updated_at": "2026-08-11T06:00:00Z"
}
```

| Field | Keterangan |
|---|---|
| `buy_price_per_gram` | Harga beli emas (anggota bayar ke koperasi) |
| `sell_price_per_gram` | Harga jual emas (koperasi bayar ke anggota) |

**Error Responses:**
| Code | Kondisi |
|---|---|
| `503` | Admin belum mengisi harga emas. Tampilkan "Harga emas sedang tidak tersedia" |

---

### POST `/gold/buy` 🔒

Membeli emas digital. Saldo rekening simpanan didebet, transaksi on-chain diproses asinkron.

**Request Body:**
```json
{
  "gram_amount": 0.5,
  "savings_account_id": 7
}
```

| Field | Validasi | Keterangan |
|---|---|---|
| `gram_amount` | Required, min 0.0001 | Berat emas yang dibeli (gram) |
| `savings_account_id` | Required, > 0 | Rekening Wadiah yang didebet |

**Response `201`:**
```json
{
  "id": 88,
  "user_id": 42,
  "type": "buy",
  "gram_amount": 0.5,
  "price_per_gram": 1698000,
  "total_rupiah": 849000,
  "tx_hash": null,
  "status": "pending",
  "created_at": "2026-08-11T07:40:00Z"
}
```

> **⚠️ Status Asinkron:** Setelah `201`, status transaksi adalah `pending`. Frontend perlu polling atau mekanisme refresh untuk melihat update status (lihat [alur status](#status-transaksi-emas)).

**Error Responses:**
| Code | Kondisi |
|---|---|
| `400` | Field tidak valid |
| `404` | Rekening simpanan tidak ditemukan |
| `422` | Saldo tidak mencukupi ATAU rekening tidak aktif |
| `503` | Harga emas tidak tersedia |

---

### POST `/gold/sell` 🔒

Menjual emas digital. Dana hasil penjualan dikreditkan ke rekening simpanan.

**Request Body:**
```json
{
  "gram_amount": 0.5,
  "savings_account_id": 7
}
```

| Field | Validasi |
|---|---|
| `gram_amount` | Required, min 0.0001 |
| `savings_account_id` | Required, > 0 |

**Response `201`:**
```json
{
  "id": 89,
  "user_id": 42,
  "type": "sell",
  "gram_amount": 0.5,
  "price_per_gram": 1685000,
  "total_rupiah": 842500,
  "tx_hash": null,
  "status": "pending",
  "created_at": "2026-08-11T07:45:00Z"
}
```

**Error Responses:**
| Code | Kondisi |
|---|---|
| `400` | Melebihi batas transaksi yang diizinkan |
| `404` | Rekening simpanan tidak ditemukan |
| `422` | Rekening tidak aktif |
| `503` | Harga emas tidak tersedia |

---

## 7. Admin Endpoints

### PUT `/admin/financing/:id/review` 🔒 👑

Approve atau reject pengajuan pembiayaan. Hanya untuk role: `pengurus`, `admin`, `super_admin`.

**URL Param:** `id` — ID pengajuan pembiayaan

**Request Body:**
```json
{ "action": "approve" }
```

| Field | Nilai Valid |
|---|---|
| `action` | `"approve"` atau `"reject"` |

**Response `200`** (mengembalikan data financing terbaru):
```json
{
  "id": 3,
  "financing_number": "FIN-MRB-1723366800123456789",
  "user_id": 42,
  "akad": "murabahah",
  "principal_amount": 10000000,
  "margin_amount": 1000000,
  "total_payable": 11000000,
  "duration_months": 12,
  "status": "approved",
  "reviewed_by": 1,
  "reviewed_at": "2026-08-11T08:00:00Z",
  "created_at": "2026-08-11T07:30:00Z"
}
```

> **Setelah `approve`:** Backend secara otomatis membuat jadwal cicilan bulanan (murabahah flat). Frontend bisa langsung memanggil `GET /financing/:id/installments` untuk menampilkannya.

**Error Responses:**
| Code | Kondisi |
|---|---|
| `400` | `id` tidak valid / `action` bukan approve/reject |
| `401` | Token tidak valid |
| `403` | Role tidak mencukupi (anggota biasa) |
| `404` | Pengajuan tidak ditemukan |
| `409` | Pengajuan sudah pernah di-review (bukan `pending` lagi) |

---

## 8. Public Endpoints

### GET `/health`

Health check untuk load balancer / uptime monitor. **Tidak butuh token.**

**Response `200`:**
```json
{
  "status": "ok",
  "database": "ok"
}
```

---

## 9. Data Models Lengkap

### User

```typescript
interface User {
  id: number;
  nama_lengkap: string;
  email: string;
  role: "anggota" | "pengurus" | "admin" | "super_admin";
  wallet_address: string | null;  // null jika belum set
  status: "active" | "inactive" | "banned";
  created_at: string;  // ISO 8601
  updated_at: string;  // ISO 8601
}
```

### SavingsAccount

```typescript
interface SavingsAccount {
  id: number;
  user_id: number;
  savings_product_id: number;
  balance: number;         // dalam Rupiah, selalu >= 0
  status: "active" | "frozen" | "closed";
  created_at: string;
  updated_at: string;
}
```

### Financing

```typescript
interface Financing {
  id: number;
  financing_number: string;   // Format: "FIN-MRB-{unix_nano}"
  user_id: number;
  akad: "murabahah";
  principal_amount: number;
  margin_amount: number;
  total_payable: number;       // = principal + margin
  duration_months: number;
  status: "pending" | "approved" | "rejected" | "active" | "paid";
  reviewed_by: number | null;
  reviewed_at: string | null;
  created_at: string;
}
```

### FinancingInstallment

```typescript
interface FinancingInstallment {
  id: number;
  financing_id: number;
  installment_number: number;  // urutan cicilan, mulai dari 1
  amount_due: number;          // nominal yang harus dibayar
  amount_paid: number;         // nominal yang sudah dibayar
  due_date: string;            // ISO 8601
  status: "unpaid" | "paid";
  paid_at: string | null;      // null jika belum dibayar
}
```

### GoldPrice

```typescript
interface GoldPrice {
  id: number;
  buy_price_per_gram: number;   // harga beli anggota (lebih tinggi)
  sell_price_per_gram: number;  // harga jual anggota (lebih rendah)
  updated_at: string;
}
```

### GoldTransaction

```typescript
interface GoldTransaction {
  id: number;
  user_id: number;
  type: "buy" | "sell";
  gram_amount: number;
  price_per_gram: number;    // harga yang berlaku saat transaksi
  total_rupiah: number;
  tx_hash: string | null;    // diisi setelah konfirmasi blockchain
  status: "pending" | "processing" | "success" | "failed";
  created_at: string;
}
```

---

## 10. Status & Enum Values

### Status Akun User

| Status | Keterangan | Bisa Login? |
|---|---|---|
| `active` | Akun normal | ✅ Ya |
| `inactive` | Dinonaktifkan | ❌ No — 403 |
| `banned` | Diblokir admin | ❌ No — 403 |

### Status Rekening Simpanan

| Status | Keterangan | Bisa Transaksi? |
|---|---|---|
| `active` | Rekening aktif | ✅ Ya |
| `frozen` | Dibekukan sementara | ❌ No — 422 |
| `closed` | Ditutup | ❌ No — 422 |

### Status Transaksi Emas

```
pending → processing → success
               │
               └──────────→ failed (blockchain revert → saldo dikembalikan otomatis)
```

| Status | Keterangan | tx_hash Ada? |
|---|---|---|
| `pending` | Saldo sudah terpotong, menunggu antrian worker | ❌ null |
| `processing` | Transaksi sudah dikirim ke blockchain | ✅ ada |
| `success` | Konfirmasi blok diterima, event terverifikasi | ✅ ada |
| `failed` | Blockchain reverted, **saldo dikembalikan otomatis** | ✅ ada |

> **Frontend:** Saat status `failed`, tidak perlu melakukan refund manual. Backend sudah mengkreditkan kembali saldo secara atomik.

### Status Pembiayaan

```
pending → approved → active → paid
        ↓
      rejected
```

| Status | Keterangan |
|---|---|
| `pending` | Menunggu review admin |
| `approved` | Disetujui, jadwal cicilan sudah dibuat |
| `rejected` | Ditolak admin |
| `active` | Pembiayaan berjalan (ada cicilan yang belum lunas) |
| `paid` | Semua cicilan sudah lunas |

---

## 11. Penanganan Token di Frontend

### Storage Token

Simpan token JWT yang diterima dari `/register` atau `/login`. Gunakan `localStorage` atau `httpOnly cookie` sesuai kebutuhan keamanan aplikasi.

### Cara Pakai Token

Sertakan di **setiap** request ke protected endpoint:

```
Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...
```

### JWT Token TTL

Default: **24 jam** (dapat diubah via env `JWT_TOKEN_TTL_HOURS`).

### Kapan Hapus Token Lokal

| Kondisi | Aksi yang Direkomendasikan |
|---|---|
| Response `401` dari protected endpoint | Hapus token, redirect ke `/login` |
| Response `403` dari protected endpoint | Hapus token, tampilkan "Akun dinonaktifkan, hubungi admin" |
| Setelah `POST /logout` berhasil (`200`) | Hapus token, redirect ke `/login` |

### Flow Logout yang Benar

```
1. POST /logout (kirim token di header)
2. Terima 200 OK
3. Hapus token dari localStorage / cookie
4. Redirect ke /login
```

> **Penting:** Token yang sudah di-logout akan masuk blocklist di Redis. Jika frontend mengirim token lama ke backend, akan mendapat `401` dengan pesan *"sesi telah diakhiri, silakan login kembali"*.

---

## Ringkasan Semua Endpoint

| Method | Endpoint | Auth | Role | Deskripsi |
|---|---|---|---|---|
| `GET` | `/health` | Public | — | Health check |
| `POST` | `/register` | Public | — | Daftar anggota baru |
| `POST` | `/login` | Public | — | Login |
| `GET` | `/gold/price` | Public | — | Harga emas terkini |
| `GET` | `/profile` | 🔒 | semua | Profil user sendiri |
| `POST` | `/logout` | 🔒 | semua | Logout & invalidasi token |
| `POST` | `/savings/accounts` | 🔒 | semua | Buka rekening simpanan |
| `GET` | `/savings/accounts` | 🔒 | semua | Lihat rekening & saldo |
| `POST` | `/savings/deposit` | 🔒 | semua | Setor dana |
| `POST` | `/financing/apply` | 🔒 | semua | Ajukan pembiayaan |
| `GET` | `/financing` | 🔒 | semua | Lihat daftar pembiayaan saya |
| `GET` | `/financing/:id/installments` | 🔒 | semua | Lihat jadwal cicilan |
| `POST` | `/financing/installments/:id/pay` | 🔒 | semua | Bayar cicilan |
| `POST` | `/gold/buy` | 🔒 | semua | Beli emas |
| `POST` | `/gold/sell` | 🔒 | semua | Jual emas |
| `PUT` | `/admin/financing/:id/review` | 🔒 | 👑 Admin | Approve/reject pembiayaan |

---

*Dokumentasi dibuat berdasarkan source code per 11 Agustus 2026.*
