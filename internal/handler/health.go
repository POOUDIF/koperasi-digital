// Package handler berisi HTTP handler untuk setiap kelompok endpoint.
package handler

import (
	"database/sql"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// HealthHandler mengelompokkan dependency yang dibutuhkan oleh health-check endpoint.
type HealthHandler struct {
	db *sql.DB
}

// NewHealthHandler membuat instance HealthHandler baru.
// Dependency (db) diinject lewat konstruktor, bukan global variable.
func NewHealthHandler(db *sql.DB) *HealthHandler {
	return &HealthHandler{db: db}
}

// healthResponse mendefinisikan struktur JSON yang dikembalikan oleh endpoint.
// Tag `json` memastikan nama field konsisten di semua response.
type healthResponse struct {
	Status    string            `json:"status"`              // "ok" atau "degraded"
	Timestamp string            `json:"timestamp"`           // waktu server saat request diterima
	Services  map[string]string `json:"services"`            // status tiap dependency
	Version   string            `json:"version,omitempty"`   // versi build (opsional)
}

// Check adalah handler untuk GET /api/v1/health.
//
// Endpoint ini melakukan:
//  1. Ping ke database untuk memverifikasi koneksi masih hidup.
//  2. Mengembalikan HTTP 200 jika semua dependency sehat.
//  3. Mengembalikan HTTP 503 jika ada dependency yang bermasalah.
//
// Endpoint ini umumnya dipanggil oleh load-balancer atau orchestrator (Kubernetes liveness probe)
// untuk memutuskan apakah instance ini layak menerima traffic.
func (h *HealthHandler) Check(c *gin.Context) {
	services := make(map[string]string)
	overallStatus := "ok"
	httpStatus := http.StatusOK

	// --- Cek koneksi database ---
	// db.PingContext mengirim perintah ringan ke database. Jika gagal, koneksi putus
	// atau database sedang down.
	if err := h.db.PingContext(c.Request.Context()); err != nil {
		services["database"] = "unreachable: " + err.Error()
		overallStatus = "degraded"
		httpStatus = http.StatusServiceUnavailable
	} else {
		services["database"] = "ok"
	}

	// Tambahkan pengecekan service lain di sini (Redis, external API, dsb.)
	// Contoh:
	// if err := h.redis.Ping(c.Request.Context()); err != nil {
	//     services["redis"] = "unreachable"
	//     overallStatus = "degraded"
	//     httpStatus = http.StatusServiceUnavailable
	// }

	c.JSON(httpStatus, healthResponse{
		Status:    overallStatus,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Services:  services,
	})
}
