package notify

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"time"

	"github.com/alejandrodnm/polybot/internal/domain"
	"github.com/olekukonko/tablewriter"
)

// Console implementa ports.Notifier imprimiendo una tabla formateada en la consola.
type Console struct {
	out       io.Writer
	orderSize float64
	validate  bool
}

// NewConsole crea un notificador que escribe a stdout.
func NewConsole(orderSize float64, validate bool) *Console {
	return &Console{out: os.Stdout, orderSize: orderSize, validate: validate}
}

// NewConsoleWriter crea un notificador que escribe al writer dado (útil en tests).
func NewConsoleWriter(w io.Writer) *Console {
	return &Console{out: w, orderSize: 100}
}

// Notify imprime la tabla de oportunidades y el resumen de portfolio.
func (c *Console) Notify(_ context.Context, opportunities []domain.Opportunity) error {
	now := time.Now().Format("15:04:05")

	if len(opportunities) == 0 {
		fmt.Fprintf(c.out, "\n[%s] No opportunities found\n\n", now)
		return nil
	}

	fmt.Fprintf(c.out, "\n[%s] Top %d Opportunities — ranked by YOUR $/day\n", now, len(opportunities))

	c.printTable(opportunities)
	c.printPortfolio(opportunities)

	if c.validate {
		c.printValidation(opportunities)
	}

	return nil
}

// printTable imprime la tabla principal de oportunidades.
func (c *Console) printTable(opps []domain.Opportunity) {
	table := tablewriter.NewWriter(c.out)
	table.Header("#", "Market", "End", "Spread", "MaxSpr", "Competition", "Your$/day", "APR%", "Arb")

	for i, opp := range opps {
		label := marketLabel(opp.Market)
		endDate := endDateLabel(opp.Market)
		arbLabel := "-"
		if opp.HasArbitrage {
			arbLabel = fmt.Sprintf("***+%.4f", opp.ArbitrageGap)
		}

		apr := opp.APR(c.orderSize)

		table.Append(
			fmt.Sprintf("%d", i+1),
			label,
			endDate,
			fmt.Sprintf("%.4f", opp.SpreadTotal),
			fmt.Sprintf("%.4f", opp.Market.Rewards.MaxSpread),
			fmt.Sprintf("$%.0f", opp.Competition),
			fmt.Sprintf("$%.4f", opp.YourDailyReward),
			fmt.Sprintf("%.1f%%", apr),
			arbLabel,
		)
	}

	table.Render()
}

// printPortfolio imprime la simulación de portfolio con los top 5 mercados (C7).
func (c *Console) printPortfolio(opps []domain.Opportunity) {
	top := opps
	if len(top) > 5 {
		top = opps[:5]
	}

	fmt.Fprintf(c.out, "\n=== PORTFOLIO SIMULATION (order: $%.0f/side per market) ===\n", c.orderSize)

	var totalDaily float64
	for i, opp := range top {
		name := marketLabel(opp.Market)
		fmt.Fprintf(c.out, "  #%d  %-42s → $%.4f/day\n", i+1, name, opp.YourDailyReward)
		totalDaily += opp.YourDailyReward
	}

	capital := c.orderSize * 2 * float64(len(top)) // YES + NO por mercado
	monthly := totalDaily * 30
	apr := 0.0
	if capital > 0 {
		apr = (totalDaily / capital) * 365 * 100
	}

	fmt.Fprintf(c.out, "\n  Capital at risk:        $%.2f (2 orders × $%.0f × %d markets)\n",
		capital, c.orderSize, len(top))
	fmt.Fprintf(c.out, "  Estimated daily income: $%.4f/day\n", totalDaily)
	fmt.Fprintf(c.out, "  Estimated monthly:      $%.2f/month\n", monthly)
	fmt.Fprintf(c.out, "  Estimated APR:          %.1f%%\n\n", apr)
}

// printValidation imprime el cálculo detallado para los top 3 mercados (C8).
func (c *Console) printValidation(opps []domain.Opportunity) {
	top := opps
	if len(top) > 3 {
		top = opps[:3]
	}

	fmt.Fprintln(c.out, "=== VALIDATION — step-by-step calculation ===")

	for i, opp := range top {
		m := opp.Market
		slug := m.Slug
		if slug == "" {
			slug = m.ConditionID
		}

		fmt.Fprintf(c.out, "\n--- Market #%d: %s ---\n", i+1, marketLabel(m))
		fmt.Fprintf(c.out, "  URL:           https://polymarket.com/event/%s\n", slug)
		fmt.Fprintf(c.out, "  Condition ID:  %s\n", m.ConditionID)

		if !m.EndDate.IsZero() {
			hours := m.HoursToResolution()
			fmt.Fprintf(c.out, "  End date:      %s (%.0f hours left)\n",
				m.EndDate.Format("2006-01-02"), hours)
		}

		fmt.Fprintf(c.out, "\n  Raw API data:\n")
		fmt.Fprintf(c.out, "    max_spread:       %.4f\n", m.Rewards.MaxSpread)
		fmt.Fprintf(c.out, "    daily_rate:       $%.2f\n", m.Rewards.DailyRate)
		fmt.Fprintf(c.out, "    maker_base_fee:   %.4f (%.2f%%)\n",
			m.MakerBaseFee, m.MakerBaseFee*100)

		fmt.Fprintf(c.out, "\n  Order book:\n")
		fmt.Fprintf(c.out, "    best_ask_YES:     %.4f\n", opp.YesAsk)
		fmt.Fprintf(c.out, "    best_ask_NO:      %.4f\n", opp.NoAsk)
		fmt.Fprintf(c.out, "    YES+NO sum:       %.4f\n", opp.YesNoSum)
		fmt.Fprintf(c.out, "    spread_total:     %.4f (YES+NO-1)\n", opp.SpreadTotal)
		fmt.Fprintf(c.out, "    competition:      $%.2f (USDC within max_spread)\n", opp.Competition)

		fmt.Fprintf(c.out, "\n  Reward calculation:\n")
		fmt.Fprintf(c.out, "    your_share:       %.4f%% (%.2f / (%.2f + %.2f))\n",
			opp.YourShare*100, c.orderSize, c.orderSize, opp.Competition)
		fmt.Fprintf(c.out, "    spread_score:     %.4f (((%.4f - %.4f) / %.4f)²)\n",
			opp.SpreadScore,
			m.Rewards.MaxSpread, opp.SpreadTotal, m.Rewards.MaxSpread)
		fmt.Fprintf(c.out, "    raw_reward:       $%.4f (%.2f × %.4f%%)\n",
			m.Rewards.DailyRate*opp.YourShare,
			m.Rewards.DailyRate, opp.YourShare*100)
		fmt.Fprintf(c.out, "    your_daily_rwd:   $%.4f (raw × spread_score)\n", opp.YourDailyReward)

		feeRate := m.EffectiveFeeRate(0.02)
		fees := c.orderSize * feeRate * 2
		fmt.Fprintf(c.out, "    fees:             $%.4f (2 × $%.0f × %.2f%%)\n",
			fees, c.orderSize, feeRate*100)
		fmt.Fprintf(c.out, "    net_daily:        $%.4f\n", opp.NetProfitEst)

		verdict := "RENTABLE"
		verdictIcon := "✓"
		if opp.NetProfitEst <= 0 {
			verdict = "NO rentable con esta orden"
			verdictIcon = "✗"
		}
		fmt.Fprintf(c.out, "\n  VERDICT: %s %s\n", verdictIcon, verdict)

		if opp.HasArbitrage {
			fmt.Fprintf(c.out, "  *** ARBITRAGE: gap = $%.4f (compra YES+NO, vende a $1.00)\n",
				opp.ArbitrageGap*c.orderSize)
		}
	}
	fmt.Fprintln(c.out)
}

// marketLabel devuelve el label corto del mercado para la tabla.
func marketLabel(m domain.Market) string {
	if m.Question != "" {
		if len(m.Question) > 42 {
			return m.Question[:39] + "..."
		}
		return m.Question
	}
	if len(m.ConditionID) > 14 {
		return m.ConditionID[:12] + "..."
	}
	return m.ConditionID
}

// endDateLabel devuelve la fecha de resolución formateada, o "-" si no está disponible.
func endDateLabel(m domain.Market) string {
	if m.EndDate.IsZero() {
		return "-"
	}
	hours := m.HoursToResolution()
	if hours < 48 {
		return fmt.Sprintf("%s (!%.0fh)", m.EndDate.Format("01-02"), math.Round(hours))
	}
	return m.EndDate.Format("2006-01-02")
}
