package ports

import (
	"context"

	"github.com/alejandrodnm/polybot/internal/domain"
)

// Notifier presenta las oportunidades encontradas al usuario.
type Notifier interface {
	// Notify muestra las oportunidades ordenadas por score.
	// En la implementación de consola, imprime una tabla formateada.
	Notify(ctx context.Context, opportunities []domain.Opportunity) error
}
