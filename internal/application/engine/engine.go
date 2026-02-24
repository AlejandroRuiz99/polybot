package engine

import (
	"context"

	"github.com/alejandrodnm/polybot/internal/domain"
)

// ScannerService es la interfaz mínima que los engines necesitan del scanner.
// Desacopla LiveEngine y PaperEngine de *scanner.Scanner concreto.
type ScannerService interface {
	RunOnce(ctx context.Context) ([]domain.Opportunity, error)
}

// QueuePosition devuelve el valor en USDC de los bids al mismo nivel de precio.
// FIFO dentro de un nivel de precio: solo los bids al mismo precio están delante.
func QueuePosition(book domain.OrderBook, bidPrice float64) float64 {
	total := 0.0
	for _, entry := range book.Bids {
		if abs64(entry.Price-bidPrice) < 0.001 {
			total += entry.Size * entry.Price
		}
	}
	return total
}

// TruncateStr trunca un string a maxLen caracteres añadiendo "..." si es necesario.
func TruncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func abs64(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
