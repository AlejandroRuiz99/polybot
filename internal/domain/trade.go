package domain

import "time"

// Trade representa un trade histórico de la API.
type Trade struct {
	ID        string
	TokenID   string
	Side      string  // "BUY" o "SELL"
	Price     float64
	Size      float64
	Timestamp time.Time
}
