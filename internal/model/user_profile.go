package model

import "time"

// UserProfile merepresentasikan data KYC (Know Your Customer) dari anggota.
type UserProfile struct {
	UserID                int64     `db:"user_id"                 json:"user_id"`
	NIK                   string    `db:"nik"                     json:"nik"`
	PhoneNumber           string    `db:"phone_number"            json:"phone_number"`
	Address               string    `db:"address"                 json:"address"`
	JobTitle              string    `db:"job_title"               json:"job_title"`
	MonthlyIncome         float64   `db:"monthly_income"          json:"monthly_income"`
	EmergencyContactName  string    `db:"emergency_contact_name"  json:"emergency_contact_name"`
	EmergencyContactPhone string    `db:"emergency_contact_phone" json:"emergency_contact_phone"`
	CreatedAt             time.Time `db:"created_at"              json:"created_at"`
	UpdatedAt             time.Time `db:"updated_at"              json:"updated_at"`
}

// UpdateProfileRequest adalah payload JSON untuk melengkapi/mengupdate data profil.
type UpdateProfileRequest struct {
	NIK                   string  `json:"nik"                     binding:"required,len=16"`
	PhoneNumber           string  `json:"phone_number"            binding:"required,min=10,max=15"`
	Address               string  `json:"address"                 binding:"required"`
	JobTitle              string  `json:"job_title"               binding:"required"`
	MonthlyIncome         float64 `json:"monthly_income"          binding:"required,min=0"`
	EmergencyContactName  string  `json:"emergency_contact_name"  binding:"required"`
	EmergencyContactPhone string  `json:"emergency_contact_phone" binding:"required,min=10,max=15"`
}
