# Ringkasan Pengembangan Fitur Koperasi Digital

Dokumen ini merangkum seluruh penambahan dan pembaruan fitur yang telah dikembangkan secara berurutan pada arsitektur sistem Koperasi Digital. 

---

## 1. Dashboard Super Admin (Pemantauan Global)
**Tujuan**: Memberikan akses kepada peran `super_admin`, `admin`, dan `pengurus` untuk dapat melihat seluruh data pengguna dan riwayat transaksi koperasi.

* **Daftar Pengguna**: Menambahkan endpoint `GET /api/v1/admin/users` untuk melihat seluruh anggota koperasi.
* **Transaksi Simpanan (Saving)**: Menambahkan endpoint `GET /api/v1/admin/transactions/saving` untuk membaca seluruh riwayat deposit dan penarikan di tabel `savings_transactions`.
* **Transaksi Pembiayaan (Financing)**: Menambahkan endpoint `GET /api/v1/admin/transactions/financing` untuk memantau semua pengajuan pembiayaan dari tabel `financings`.
* **Transaksi Emas (Gold)**: Menambahkan endpoint `GET /api/v1/admin/transactions/gold` untuk melihat riwayat jual/beli emas anggota dari tabel `gold_transactions`.
* Seluruh endpoint ini dilindungi dengan **Role-Based Access Control (RBAC)** ganda menggunakan middleware `RequireAuth` dan `RequireRole`.

---

## 2. Otomatisasi Pembuatan Rekening Wajib
**Tujuan**: Secara otomatis membukakan buku tabungan wajib ("Simpanan Pokok" & "Simpanan Wajib") tepat setelah pengguna selesai melakukan registrasi.

* **Database (Tabel `savings_products`)**: Memanfaatkan flag `is_mandatory = TRUE` untuk menandai produk mana yang bersifat wajib bagi pendaftar baru.
* **Repository**: Menambahkan query `GetMandatoryProducts` di `SavingRepository`.
* **Service**: Mengimplementasikan logika `OpenMandatoryAccounts` pada modul simpanan yang bertugas membuka akun dengan saldo `0` bagi masing-masing produk wajib.
* **Handler/Registrasi**: Meng-inject `SavingService` ke dalam modul `UserHandler` agar saat metode registrasi (`Register`) dipanggil dan berhasil menyimpan akun, sistem akan langsung membuatkan rekening secara *under-the-hood*.

---

## 3. Modul Profil KYC (Know Your Customer)
**Tujuan**: Mewajibkan anggota untuk mengisi kelengkapan data (seperti KTP, penghasilan, dll) sebelum dapat melakukan aktivitas pembiayaan atau meminjam dana.

* **Migrasi Database (`008_create_user_profiles_table.sql`)**: 
  * Membuat tabel `user_profiles` dengan kolom penting seperti `nik`, `phone_number`, `address`, `job_title`, `monthly_income`, dan detail kontak darurat.
* **Penyimpanan (Upsert)**:
  * Menggunakan strategi `ON CONFLICT DO UPDATE` pada basis data PostgreSQL sehingga endpoint simpan KYC berlaku ganda: sebagai `Create` (jika baru) dan `Update` (jika sudah ada).
* **Endpoints**: 
  * `PUT /api/v1/profile/kyc`: Halaman formulir untuk submit kelengkapan data diri.
  * `GET /api/v1/profile/kyc`: Menampilkan data diri KYC anggota yang sedang masuk (login).

---

## 4. Verifikasi Email via Kode OTP (One-Time Password)
**Tujuan**: Memastikan email pengguna valid dengan mengirimkan kode rahasia sebelum mereka bisa mendapatkan hak akses (JWT Token) ke dalam sistem.

* **Migrasi Database (`009_add_email_verification.sql`)**: 
  * Menambahkan kolom `is_email_verified` (Boolean) ke tabel induk `users`.
* **Generasi & Penyimpanan OTP Sementara**:
  * Pada saat registrasi selesai, sistem akan menciptakan 6 digit angka acak (OTP).
  * OTP disimpan ke dalam *Redis Cache* dengan masa berlaku selama **15 menit** dan dengan *key* berformat `otp:email@domain.com`.
* **Email Service (SMTP)**:
  * Membuat dan mendaftarkan `EmailService` internal (menggunakan modul bawaan `net/smtp`).
  * OTP yang sudah di-generate kemudian didistribusikan langsung ke email pendaftar.
* **Perubahan Alur (Authentication Flow)**:
  1. Pengguna memanggil `/register` → OTP dikirim ke email, lalu API membalas "Sukses, cek email".
  2. Pengguna memanggil `/verify-email` → Mengirim email & OTP. Sistem memvalidasi dari Redis, mengubah flag `is_email_verified` di DB, dan **langsung merilis token JWT (Auto-Login)**.
  3. Apabila pengguna nekat memanggil `/login` padahal email belum divalidasi, server otomatis melempar *HTTP 403 Forbidden: Email belum diverifikasi*.

---

## Tindakan Lanjutan untuk Tim Frontend (Penyesuaian UI/UX)
Agar selaras dengan fitur backend di atas, tim frontend diharapkan melakukan beberapa penyesuaian berikut pada antarmuka aplikasi:

1. **Menu Dashboard Super Admin**:
   * Buat halaman/menu baru khusus untuk peran `admin` / `super_admin`.
   * Sediakan sub-menu untuk melihat "Daftar Pengguna", serta riwayat transaksi: "Financing", "Gold", dan "Saving".
   * Gunakan endpoint GET berawalan `/api/v1/admin/...` dan pastikan mengirimkan token JWT milik admin.

2. **Dampak Otomatisasi Rekening Wajib**:
   * Tim frontend tidak perlu lagi membuat tombol khusus "Buka Rekening Simpanan Pokok/Wajib" untuk pengguna baru. Saldo awal (`0`) sudah langsung tersedia setelah pengguna berhasil login.

3. **Formulir Profil Data (KYC)**:
   * Buat halaman baru, misal: `/profile/kyc` atau sebuah modal khusus.
   * Kumpulkan *mandatory fields* (NIK, No. HP, Alamat, Pekerjaan, Pendapatan, dan Kontak Darurat).
   * Submit data tersebut dalam format JSON ke `PUT /api/v1/profile/kyc`.

4. **Alur Pendaftaran (Register) & Layar OTP**:
   * Sesuaikan halaman *Register*. Saat form di-submit dan berhasil, server tidak akan langsung memberikan *Token*. Alihkan pengguna ke layar baru: "Verifikasi Email".
   * Layar Verifikasi Email meminta pengguna memasukkan **6 digit angka OTP**.
   * Hit endpoint `POST /api/v1/verify-email` dengan JSON payload `{ "email": "...", "otp": "..." }`.
   * Apabila sukses, simpan token JWT yang dikembalikan dan masukkan pengguna ke aplikasi (Auto-Login).
   * Beri penanganan *error* saat login jika server membalas status `403 Forbidden` ("email belum diverifikasi"), lalu arahkan kembali pengguna ke layar input OTP.

---
**Catatan Penting**: Mengingat sistem kini tersambung dengan modul pengirim surel (email), lingkungan sistem wajib diberikan variabel `.env` untuk SMTP (`SMTP_HOST`, `SMTP_PORT`, `SMTP_USER`, dll) agar OTP bisa dikirimkan ke kotak masuk pengguna yang asli.
