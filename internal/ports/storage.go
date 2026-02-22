package ports

import (
	"context"
	"time"

	"github.com/alejandrodnm/polybot/internal/domain"
)

// Storage persiste los resultados de cada ciclo de escaneo.
type Storage interface {
	// SaveScan persiste las oportunidades encontradas en un ciclo.
	SaveScan(ctx context.Context, opportunities []domain.Opportunity) error

	// GetHistory devuelve las oportunidades registradas en el rango de tiempo dado.
	GetHistory(ctx context.Context, from, to time.Time) ([]domain.Opportunity, error)

	// Close cierra la conexión a la base de datos limpiamente.
	Close() error
}
