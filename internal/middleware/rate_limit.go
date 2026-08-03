package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// IPRateLimiter membungkus struktur data untuk menyimpan rate limiter per IP address.
type IPRateLimiter struct {
	ips map[string]*rate.Limiter
	mu  *sync.RWMutex
	r   rate.Limit
	b   int
}

// NewIPRateLimiter membuat rate limiter di mana:
// 'r' adalah seberapa banyak request per detik yang diizinkan (misal: 3 = 3 permintaan per detik).
func NewIPRateLimiter(r rate.Limit, b int) *IPRateLimiter {
	return &IPRateLimiter{
		ips: make(map[string]*rate.Limiter),
		mu:  &sync.RWMutex{},
		r:   r,
		b:   b,
	}
}

// GetLimiter mengambil limiter untuk IP tertentu, memutasi map jika IP tersebut belum memiliki limiter = O(1).
func (i *IPRateLimiter) GetLimiter(ip string) *rate.Limiter {
	i.mu.RLock()
	limiter, exists := i.ips[ip]
	i.mu.RUnlock()

	if !exists {
		i.mu.Lock()
		// Double check pattern, mencegah race condition di antara RUnlock dan Lock
		limiter, exists = i.ips[ip]
		if !exists {
			limiter = rate.NewLimiter(i.r, i.b)
			i.ips[ip] = limiter
		}
		i.mu.Unlock()
	}

	return limiter
}

// Global variable untuk limit default public routes.
// 3 requests per second dengan max burst 5 requests.
var publicLimiter = NewIPRateLimiter(rate.Every(time.Second/3), 5)

// RateLimit merupakan middleware Gin untuk membatasi aktivitas dari suatu IP.
// Ini digunakan untuk rute ter-ekspos (login, register) untuk mengurangi serangan brute-force.
func RateLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		clientIP := c.ClientIP()
		limiter := publicLimiter.GetLimiter(clientIP)

		// Jika Allow() false, berarti IP ini sudah lewat batas burst atau request-per-sec
		if !limiter.Allow() {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "terlalu banyak permintaan, silakan coba lagi beberapa saat kemudian",
			})
			return
		}

		c.Next()
	}
}
