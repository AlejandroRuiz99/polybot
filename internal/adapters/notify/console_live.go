package notify

import (
	"fmt"
	"time"

	"github.com/alejandrodnm/polybot/internal/domain"
)

// LiveReportInput agrupa los datos necesarios para imprimir el reporte live.
type LiveReportInput struct {
	Stats        domain.LiveStats
	OpenOrders   []domain.LiveOrder
	PartialPairs []string
	PairOrders   map[string][]domain.LiveOrder // pairID → órdenes
	CircuitBreaker domain.CircuitBreaker
}

// PrintLiveReport imprime el informe completo de live trading.
func (c *Console) PrintLiveReport(in LiveReportInput) {
	fmt.Fprintf(c.out, "\n╔══════════════════════════════════════════════════════════════╗\n")
	fmt.Fprintf(c.out, "║                    LIVE TRADING REPORT                       ║\n")
	fmt.Fprintf(c.out, "╚══════════════════════════════════════════════════════════════╝\n\n")

	stats := in.Stats
	fmt.Fprintf(c.out, "  Period:       %s → %s (%d days)\n",
		stats.StartDate.Format("2006-01-02"),
		stats.EndDate.Format("2006-01-02"),
		stats.DaysRunning)
	fmt.Fprintf(c.out, "  Total Orders: %d | Fills: %d (%.0f%% fill rate)\n",
		stats.TotalOrders, stats.TotalFills, stats.FillRateReal*100)
	fmt.Fprintf(c.out, "  Merges:       %d completed\n", stats.CompletePairs)
	fmt.Fprintf(c.out, "  Merge Profit: $%.4f\n", stats.TotalMergeProfit)
	fmt.Fprintf(c.out, "  Gas Cost:     $%.4f\n", stats.TotalGasCostUSD)
	fmt.Fprintf(c.out, "  Net P&L:      $%.4f (avg $%.4f/day)\n", stats.NetPnL, stats.DailyAvgPnL)
	fmt.Fprintf(c.out, "  Rotations:    %d\n", stats.TotalRotations)

	fmt.Fprintf(c.out, "\n── OPEN ORDERS (%d) ──\n", len(in.OpenOrders))
	if len(in.OpenOrders) > 0 {
		fmt.Fprintf(c.out, "  %-6s %6s %6s %8s %-35s %s\n", "SIDE", "PRICE", "SIZE$", "FILLED$", "MARKET", "AGE")
		for _, o := range in.OpenOrders {
			age := time.Since(o.PlacedAt).Truncate(time.Minute)
			q := domain.TruncateQuestion(o.Question, o.ConditionID, 35)
			fmt.Fprintf(c.out, "  %-6s %6.2f %6.2f %8.2f %-35s %v\n",
				o.Side, o.BidPrice, o.Size, o.FilledSize, q, age)
		}
	} else {
		fmt.Fprintln(c.out, "  (none)")
	}

	fmt.Fprintf(c.out, "\n── PARTIAL FILLS (%d pairs with only one side filled) ──\n", len(in.PartialPairs))
	if len(in.PartialPairs) > 0 {
		for _, pairID := range in.PartialPairs {
			orders := in.PairOrders[pairID]
			for _, o := range orders {
				status := string(o.Status)
				if o.FilledSize > 0 {
					status = fmt.Sprintf("FILLED $%.2f", o.FilledSize)
				}
				q := domain.TruncateQuestion(o.Question, o.ConditionID, 30)
				pfx := pairID
				if len(pfx) > 8 {
					pfx = pfx[:8]
				}
				fmt.Fprintf(c.out, "  [%s] %-4s %5.2f¢ %8s  %s\n", pfx, o.Side, o.BidPrice*100, status, q)
			}
			fmt.Fprintln(c.out)
		}
	} else {
		fmt.Fprintln(c.out, "  (none)")
	}

	fmt.Fprintf(c.out, "\n── SUMMARY ──\n")
	fmt.Fprintf(c.out, "  Open orders:        %d\n", len(in.OpenOrders))
	fmt.Fprintf(c.out, "  Partial fill pairs: %d (RISK: directional exposure)\n", len(in.PartialPairs))
	fmt.Fprintf(c.out, "  Circuit breaker:    ")
	cb := in.CircuitBreaker
	if cb.Triggered {
		fmt.Fprintf(c.out, "TRIGGERED (reason: %s)\n", cb.TriggeredReason)
	} else {
		fmt.Fprintf(c.out, "OK\n")
	}

	if len(stats.Dailies) > 0 {
		fmt.Fprintf(c.out, "\n── DAILY BREAKDOWN ──\n")
		fmt.Fprintf(c.out, "  %-12s %8s %8s %8s %8s\n", "Date", "Orders", "Fills", "Merges", "NetPnL")
		for _, d := range stats.Dailies {
			fmt.Fprintf(c.out, "  %-12s %8d %8d %8d $%7.4f\n",
				d.Date.Format("2006-01-02"),
				d.OrdersPlaced, d.FillsYes+d.FillsNo, d.Merges, d.NetPnL)
		}
	}
	fmt.Fprintln(c.out)
}
