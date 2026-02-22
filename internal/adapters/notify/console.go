package notify

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"

	"github.com/alejandrodnm/polybot/internal/domain"
	"github.com/olekukonko/tablewriter"
)

// Console implementa ports.Notifier.
type Console struct {
	out       io.Writer
	orderSize float64
	table     bool
	validate  bool
}

// NewConsole crea un notificador que escribe a stdout.
func NewConsole(orderSize float64, table, validate bool) *Console {
	return &Console{out: os.Stdout, orderSize: orderSize, table: table, validate: validate}
}

// NewConsoleWriter crea un notificador para tests.
func NewConsoleWriter(w io.Writer, table, validate bool) *Console {
	return &Console{out: w, orderSize: 100, table: table, validate: validate}
}

// Notify imprime el output en el modo configurado.
func (c *Console) Notify(_ context.Context, opportunities []domain.Opportunity) error {
	if len(opportunities) == 0 {
		fmt.Fprintf(c.out, "[%s] no opportunities found\n", time.Now().Format("15:04:05"))
		return nil
	}

	if c.table {
		c.printFull(opportunities)
	} else {
		c.printCompact(opportunities)
	}

	if c.validate {
		c.printValidation(opportunities)
	}

	return nil
}

// printCompact imprime lo esencial en 2-3 líneas.
func (c *Console) printCompact(opps []domain.Opportunity) {
	now := time.Now().Format("15:04:05")
	gold, silver, _ := countByCategory(opps)
	arb := countWithArbitrage(opps)

	var sb strings.Builder
	fmt.Fprintf(&sb, "[%s] %d mkts → G:%d S:%d arb:%d", now, len(opps), gold, silver, arb)

	shown := 0
	for _, opp := range opps {
		if shown >= 4 {
			break
		}
		if opp.Category == domain.CategoryBronze || opp.Category == domain.CategoryAvoid {
			break
		}

		name := compactName(opp.Market.Question, 25)
		verdict := opp.Verdict()
		if opp.Arbitrage.HasArbitrage {
			fmt.Fprintf(&sb, " | [G*]%s rwd$%.2f +arb %s",
				name, opp.YourDailyReward, verdict)
		} else {
			fmt.Fprintf(&sb, " | %s %s rwd$%.2f fill$%.2f be%.1f %s",
				opp.Category.Icon(), name,
				opp.YourDailyReward, opp.FillCostUSDC,
				opp.BreakEvenFills, verdict)
		}
		shown++
	}

	fmt.Fprintln(c.out, sb.String())
}

// printFull imprime la tabla honesta con escenarios de P&L.
func (c *Console) printFull(opps []domain.Opportunity) {
	now := time.Now().Format("15:04:05")
	gold, silver, bronze := countByCategory(opps)
	arb := countWithArbitrage(opps)

	fmt.Fprintf(c.out, "\n[%s] %d opportunities — G:%d S:%d B:%d arb:%d\n",
		now, len(opps), gold, silver, bronze, arb)

	c.printTable(opps)
	c.printHonestSummary(opps)
}

// printTable imprime la tabla con métricas honestas.
func (c *Console) printTable(opps []domain.Opportunity) {
	table := tablewriter.NewWriter(c.out)
	table.Header("#", "Cat", "Market", "Rwd/day", "Fill$", "BE fills", "PnL 0f", "PnL 1f", "PnL 3f", "Verdict")

	for i, opp := range opps {
		label := marketLabel(opp.Market)

		beLabel := fmt.Sprintf("%.1f", opp.BreakEvenFills)
		if math.IsInf(opp.BreakEvenFills, 1) {
			beLabel = "INF"
		}

		table.Append(
			fmt.Sprintf("%d", i+1),
			opp.Category.Icon(),
			label,
			fmt.Sprintf("$%.4f", opp.YourDailyReward),
			fmt.Sprintf("$%.2f", opp.FillCostUSDC),
			beLabel,
			fmt.Sprintf("$%.4f", opp.PnLNoFills),
			fmt.Sprintf("$%.4f", opp.PnL1Fill),
			fmt.Sprintf("$%.4f", opp.PnL3Fills),
			opp.Verdict(),
		)
	}

	table.Render()

	fmt.Fprintln(c.out, "  Rwd/day = tu reward bruto | Fill$ = coste por fill event")
	fmt.Fprintln(c.out, "  BE fills = fills/día antes de perder | PnL 0f/1f/3f = escenarios")
	fmt.Fprintln(c.out, "  Verdict: FILLS=PROFIT > SAFE(>10be) > OK(>3be) > RISKY(>1be) > AVOID")
}

// printHonestSummary imprime el resumen honesto con rangos de rentabilidad.
func (c *Console) printHonestSummary(opps []domain.Opportunity) {
	golds := filterCat(opps, domain.CategoryGold)
	silvers := filterCat(opps, domain.CategorySilver)

	top := selectTop(golds, silvers, nil, 5)
	if len(top) == 0 {
		fmt.Fprintf(c.out, "\n  ⚠ No hay mercados Gold o Silver rentables\n\n")
		return
	}

	fmt.Fprintf(c.out, "\n=== HONEST PORTFOLIO (top %d, order $%.0f/side) ===\n", len(top), c.orderSize)

	var totRwd, totPnL0, totPnL1, totPnL3 float64
	for _, opp := range top {
		totRwd += opp.YourDailyReward
		totPnL0 += opp.PnLNoFills
		totPnL1 += opp.PnL1Fill
		totPnL3 += opp.PnL3Fills

		name := truncate(opp.Market.Question, 40)
		beLabel := fmt.Sprintf("%.1f fills/day", opp.BreakEvenFills)
		if math.IsInf(opp.BreakEvenFills, 1) {
			beLabel = "fills=profit"
		}
		fmt.Fprintf(c.out, "  %s %-40s rwd:$%.4f  fill:$%.2f  be:%s\n",
			opp.Category.Icon(), name, opp.YourDailyReward, opp.FillCostUSDC, beLabel)
	}

	capital := c.orderSize * 2 * float64(len(top))

	fmt.Fprintf(c.out, "\n  Capital: $%.0f (%d markets × $%.0f × 2 sides)\n",
		capital, len(top), c.orderSize)
	fmt.Fprintf(c.out, "  ─────────────────────────────────────────────\n")
	fmt.Fprintf(c.out, "  Best case  (0 fills/day): $%.4f/day  $%.2f/month  APR %.1f%%\n",
		totPnL0, totPnL0*30, pct(totPnL0, capital))
	fmt.Fprintf(c.out, "  Realistic  (1 fill/day):  $%.4f/day  $%.2f/month  APR %.1f%%\n",
		totPnL1, totPnL1*30, pct(totPnL1, capital))
	fmt.Fprintf(c.out, "  Worst case (3 fills/day): $%.4f/day  $%.2f/month  APR %.1f%%\n",
		totPnL3, totPnL3*30, pct(totPnL3, capital))

	if totPnL1 > 0 {
		fmt.Fprintf(c.out, "\n  VEREDICTO: RENTABLE con 1 fill/day — margen de seguridad: %.1f fills/day\n\n",
			totRwd/maxFloat(sumFillCosts(top), 0.0001))
	} else if totPnL0 > 0 {
		fmt.Fprintf(c.out, "\n  VEREDICTO: MARGINAL — solo rentable si los fills son < 1/día\n\n")
	} else {
		fmt.Fprintf(c.out, "\n  VEREDICTO: NO RENTABLE con la configuración actual\n\n")
	}
}

// printValidation imprime el cálculo detallado de los top 3.
func (c *Console) printValidation(opps []domain.Opportunity) {
	top := opps
	if len(top) > 3 {
		top = opps[:3]
	}

	fmt.Fprintln(c.out, "=== VALIDATION — honest step-by-step ===")

	for i, opp := range top {
		m := opp.Market
		slug := m.Slug
		if slug == "" {
			slug = m.ConditionID
		}

		fmt.Fprintf(c.out, "\n--- #%d: %s  [%s] [%s] ---\n",
			i+1, marketLabel(m), opp.Category.String(), opp.Verdict())
		fmt.Fprintf(c.out, "  URL: https://polymarket.com/event/%s\n", slug)
		if !m.EndDate.IsZero() {
			fmt.Fprintf(c.out, "  End: %s (%.0fh left)\n",
				m.EndDate.Format("2006-01-02"), m.HoursToResolution())
		}

		arb := opp.Arbitrage
		fmt.Fprintf(c.out, "\n  1. BOOK STATE:\n")
		fmt.Fprintf(c.out, "     best_bid YES=%.4f  NO=%.4f  (tu precio como maker)\n",
			opp.YesBook.BestBid(), opp.NoBook.BestBid())
		fmt.Fprintf(c.out, "     best_ask YES=%.4f  NO=%.4f\n",
			arb.BestAskYES, arb.BestAskNO)
		fmt.Fprintf(c.out, "     sum(bid)=%.4f  gap=%.4f\n",
			opp.YesBook.BestBid()+opp.NoBook.BestBid(), opp.FillCostPerPair)
		fmt.Fprintf(c.out, "     competition=$%.0f\n", opp.Competition)

		fmt.Fprintf(c.out, "\n  2. REWARD INCOME:\n")
		fmt.Fprintf(c.out, "     pool: $%.2f/day  max_spread: %.4f\n",
			m.Rewards.DailyRate, m.Rewards.MaxSpread)
		fmt.Fprintf(c.out, "     your_share: %.4f%% ($%.0f / $%.0f)\n",
			opp.YourShare*100, c.orderSize, opp.Competition+c.orderSize)
		fmt.Fprintf(c.out, "     spread_score: %.4f\n", opp.SpreadScore)
		fmt.Fprintf(c.out, "     >>> YOUR REWARD: $%.4f/day\n", opp.YourDailyReward)

		fmt.Fprintf(c.out, "\n  3. FILL COST:\n")
		fmt.Fprintf(c.out, "     cost_per_share_pair: $%.4f (bid YES + bid NO + fees - $1.00)\n",
			opp.FillCostPerPair)
		fmt.Fprintf(c.out, "     shares per $%.0f order: ~%.0f\n",
			c.orderSize, c.orderSize/maxFloat((opp.YesBook.BestBid()+opp.NoBook.BestBid())/2, 0.01))
		fmt.Fprintf(c.out, "     >>> COST PER FILL EVENT: $%.4f\n", opp.FillCostUSDC)

		beLabel := fmt.Sprintf("%.1f fills/day", opp.BreakEvenFills)
		if math.IsInf(opp.BreakEvenFills, 1) {
			beLabel = "∞ (fills are profit)"
		}
		fmt.Fprintf(c.out, "     >>> BREAK EVEN: %s\n", beLabel)

		fmt.Fprintf(c.out, "\n  4. P&L SCENARIOS:\n")
		fmt.Fprintf(c.out, "     0 fills/day: $%.4f  (best case — you never get filled)\n", opp.PnLNoFills)
		fmt.Fprintf(c.out, "     1 fill/day:  $%.4f  (conservative — low volume market)\n", opp.PnL1Fill)
		fmt.Fprintf(c.out, "     3 fills/day: $%.4f  (active market — lots of takers)\n", opp.PnL3Fills)

		if len(arb.AtDepth) > 0 {
			fmt.Fprintf(c.out, "\n  5. ARB DEPTH:\n")
			for _, d := range arb.AtDepth {
				mark := "✗"
				if d.Profitable {
					mark = "✓"
				}
				fmt.Fprintf(c.out, "     $%5.0f: YES=%.4f NO=%.4f gap=%.4f %s\n",
					d.DepthUSDC, d.AvgPriceYES, d.AvgPriceNO, d.GapAfterFees, mark)
			}
		}
	}
	fmt.Fprintln(c.out)
}

// PrintBacktest imprime los resultados del backtest de trades reales.
func (c *Console) PrintBacktest(results []domain.BacktestResult) {
	if len(results) == 0 {
		fmt.Fprintln(c.out, "\n  No backtest results available.")
		return
	}

	fmt.Fprintf(c.out, "\n╔══════════════════════════════════════════════════════════════════╗\n")
	fmt.Fprintf(c.out, "║  BACKTEST — cross-referencing scanner vs real trades            ║\n")
	fmt.Fprintf(c.out, "╚══════════════════════════════════════════════════════════════════╝\n\n")

	table := tablewriter.NewWriter(c.out)
	table.Header("#", "Market", "Rwd/d", "Trades(Y/N)", "Fills@Bid", "Fills/d", "RealPnL", "Verdict")

	for i, r := range results {
		name := truncate(r.Market.Question, 30)
		if name == "" {
			name = r.Market.ConditionID[:12] + "..."
		}

		fillsLabel := fmt.Sprintf("%d/%d", r.FillsYes, r.FillsNo)
		tradesLabel := fmt.Sprintf("%d/%d", r.TotalTradesYes, r.TotalTradesNo)
		period := fmt.Sprintf("%.0fh", r.Period.Hours())

		table.Append(
			fmt.Sprintf("%d", i+1),
			name,
			fmt.Sprintf("$%.4f", r.Opportunity.YourDailyReward),
			fmt.Sprintf("%s (%s)", tradesLabel, period),
			fillsLabel,
			fmt.Sprintf("%.1f", r.FillsBothPerDay),
			fmt.Sprintf("$%.4f", r.RealPnLDaily),
			r.Verdict,
		)
	}
	table.Render()

	fmt.Fprintln(c.out)
	for i, r := range results {
		name := truncate(r.Market.Question, 50)
		if name == "" {
			name = r.Market.ConditionID[:14]
		}
		fmt.Fprintf(c.out, "  #%d %s\n", i+1, name)
		fmt.Fprintf(c.out, "     Period:     %.0f hours of trade data\n", r.Period.Hours())
		fmt.Fprintf(c.out, "     Sim BIDs:   YES=%.4f  NO=%.4f\n", r.SimBidYes, r.SimBidNo)
		fmt.Fprintf(c.out, "     YES trades: %d total, %d would fill your bid\n",
			r.TotalTradesYes, r.FillsYes)
		fmt.Fprintf(c.out, "     NO trades:  %d total, %d would fill your bid\n",
			r.TotalTradesNo, r.FillsNo)
		fmt.Fprintf(c.out, "     Complete pairs/day: %.1f (min of both sides)\n", r.FillsBothPerDay)
		fmt.Fprintf(c.out, "     Reward/day: $%.4f\n", r.Opportunity.YourDailyReward)
		fmt.Fprintf(c.out, "     Fill cost:  $%.4f per event\n", r.Opportunity.FillCostUSDC)
		fmt.Fprintf(c.out, "     REAL P&L:   $%.4f/day  ($%.2f/month)\n",
			r.RealPnLDaily, r.RealPnLDaily*30)

		icon := "x"
		switch r.Verdict {
		case "PROFITABLE":
			icon = "OK"
		case "MARGINAL":
			icon = "~"
		}
		fmt.Fprintf(c.out, "     VERDICT:    [%s] %s\n\n", icon, r.Verdict)
	}

	// Resumen final
	var totalPnL float64
	profitable := 0
	for _, r := range results {
		totalPnL += r.RealPnLDaily
		if r.Verdict == "PROFITABLE" {
			profitable++
		}
	}

	fmt.Fprintf(c.out, "  ═══════════════════════════════════════════\n")
	fmt.Fprintf(c.out, "  TOTAL P&L (with REAL fill rates): $%.4f/day ($%.2f/month)\n",
		totalPnL, totalPnL*30)
	fmt.Fprintf(c.out, "  Profitable markets: %d/%d\n", profitable, len(results))

	if totalPnL > 0 {
		fmt.Fprintf(c.out, "  >>> STRATEGY VALIDATED: net positive with real trade data\n")
	} else {
		fmt.Fprintf(c.out, "  >>> STRATEGY NOT VALIDATED: net negative with real fill rates\n")
	}
	fmt.Fprintln(c.out)
}

// --- helpers ---

func countByCategory(opps []domain.Opportunity) (gold, silver, bronze int) {
	for _, o := range opps {
		switch o.Category {
		case domain.CategoryGold:
			gold++
		case domain.CategorySilver:
			silver++
		case domain.CategoryBronze:
			bronze++
		}
	}
	return
}

func countWithArbitrage(opps []domain.Opportunity) int {
	n := 0
	for _, o := range opps {
		if o.Arbitrage.HasArbitrage {
			n++
		}
	}
	return n
}

func filterCat(opps []domain.Opportunity, cat domain.OpportunityCategory) []domain.Opportunity {
	var out []domain.Opportunity
	for _, o := range opps {
		if o.Category == cat {
			out = append(out, o)
		}
	}
	return out
}

func selectTop(golds, silvers, bronzes []domain.Opportunity, n int) []domain.Opportunity {
	var top []domain.Opportunity
	for _, list := range [][]domain.Opportunity{golds, silvers, bronzes} {
		for _, o := range list {
			if len(top) >= n {
				return top
			}
			top = append(top, o)
		}
	}
	return top
}

func marketLabel(m domain.Market) string {
	if m.Question != "" {
		return truncate(m.Question, 38)
	}
	if len(m.ConditionID) > 14 {
		return m.ConditionID[:12] + "..."
	}
	return m.ConditionID
}

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

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func compactName(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	cut := s[:maxLen]
	if idx := strings.LastIndex(cut, " "); idx > maxLen/2 {
		cut = cut[:idx]
	}
	return cut + "…"
}

func pct(daily, capital float64) float64 {
	if capital <= 0 || daily <= 0 {
		return 0
	}
	return (daily / capital) * 365 * 100
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func sumFillCosts(opps []domain.Opportunity) float64 {
	total := 0.0
	for _, o := range opps {
		if o.FillCostUSDC > 0 {
			total += o.FillCostUSDC
		}
	}
	return total
}

// PrintPaperStatus prints a compact status for the current paper cycle.
func (c *Console) PrintPaperStatus(result PaperStatusInput) {
	now := time.Now().Format("15:04:05")

	active, complete, partial := 0, 0, 0
	var rewardAccrued float64
	for _, pos := range result.Positions {
		if pos.YesOrder == nil && pos.NoOrder == nil {
			continue
		}
		if pos.IsResolved {
			continue
		}
		active++
		if pos.IsComplete {
			complete++
		}
		if pos.PartialSince != nil && !pos.IsComplete {
			partial++
		}
		rewardAccrued += pos.RewardAccrued
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "[%s][PAPER] %d pos | %d pairs | %d partial | +%d orders | +%d fills | rwd $%.4f | cap $%.0f",
		now, active, complete, partial, result.NewOrders, result.NewFills,
		rewardAccrued, result.CapitalDeployed)

	for i, alert := range result.Alerts {
		if i >= 2 {
			break
		}
		fmt.Fprintf(&sb, "\n  !! %s", alert)
	}
	for i, warn := range result.Warnings {
		if i >= 2 {
			break
		}
		fmt.Fprintf(&sb, "\n  >> %s", warn)
	}

	fmt.Fprintln(c.out, sb.String())
}

// PaperStatusInput bundles everything PrintPaperStatus needs.
type PaperStatusInput struct {
	Positions       []domain.PaperPosition
	NewOrders       int
	NewFills        int
	Alerts          []string
	Warnings        []string
	CapitalDeployed float64
}

// PrintPaperReport prints a comprehensive paper trading report.
func (c *Console) PrintPaperReport(stats domain.PaperStats) {
	if stats.DaysRunning == 0 {
		fmt.Fprintln(c.out, "\n  No paper trading data yet. Run --paper first for a few days.")
		return
	}

	fmt.Fprintf(c.out, "\n")
	fmt.Fprintf(c.out, "========================================================\n")
	fmt.Fprintf(c.out, "  PAPER TRADING REPORT (maker fee: 0%%)\n")
	fmt.Fprintf(c.out, "  %s to %s (%d days)\n",
		stats.StartDate.Format("2006-01-02"),
		stats.EndDate.Format("2006-01-02"),
		stats.DaysRunning)
	fmt.Fprintf(c.out, "========================================================\n\n")

	if len(stats.Dailies) > 0 {
		tbl := tablewriter.NewWriter(c.out)
		tbl.Header("Date", "Pos", "Pairs", "Part", "FillY", "FillN", "Reward", "FillPnL", "Net", "Cap$")

		for _, d := range stats.Dailies {
			tbl.Append(
				d.Date.Format("01-02"),
				fmt.Sprintf("%d", d.ActivePositions),
				fmt.Sprintf("%d", d.CompletePairs),
				fmt.Sprintf("%d", d.PartialFills),
				fmt.Sprintf("%d", d.FillsYes),
				fmt.Sprintf("%d", d.FillsNo),
				fmt.Sprintf("$%.4f", d.TotalReward),
				fmt.Sprintf("$%.4f", d.TotalFillPnL),
				fmt.Sprintf("$%.4f", d.NetPnL),
				fmt.Sprintf("$%.0f", d.CapitalDeployed),
			)
		}
		tbl.Render()
	}

	fmt.Fprintf(c.out, "\n  --- AGGREGATE ---\n")
	fmt.Fprintf(c.out, "  Markets monitored:     %d\n", stats.MarketsMonitored)
	fmt.Fprintf(c.out, "  Markets resolved:      %d\n", stats.MarketsResolved)
	fmt.Fprintf(c.out, "  Total orders placed:   %d\n", stats.TotalOrders)
	fmt.Fprintf(c.out, "  Total fills:           %d (queue-adjusted)\n", stats.TotalFills)
	fmt.Fprintf(c.out, "  Complete pairs:        %d\n", stats.CompletePairs)
	fmt.Fprintf(c.out, "  Partial fills:         %d\n", stats.PartialFills)
	fmt.Fprintf(c.out, "  Fill rate (real):      %.1f fills/day\n", stats.FillRateReal)
	fmt.Fprintf(c.out, "  Max capital deployed:  $%.0f\n", stats.MaxCapital)

	fmt.Fprintf(c.out, "\n  --- PARTIAL FILL RISK ---\n")
	fmt.Fprintf(c.out, "  Max partial duration:  %.0f min\n", stats.MaxPartialMins)
	if stats.TotalFills > 0 {
		partialPct := float64(stats.PartialFills) / float64(stats.TotalFills) * 100
		fmt.Fprintf(c.out, "  Partial rate:          %.1f%% of all fills\n", partialPct)
	}

	fmt.Fprintf(c.out, "\n  --- P&L (with 0%% maker fee) ---\n")
	fmt.Fprintf(c.out, "  Reward income:         $%.4f\n", stats.TotalReward)
	fmt.Fprintf(c.out, "  Fill PnL:              $%.4f\n", stats.TotalFillPnL)
	fmt.Fprintf(c.out, "  Resolution PnL:        $%.4f\n", stats.ResolutionPnL)
	fmt.Fprintf(c.out, "  Total net PnL:         $%.4f\n", stats.NetPnL)
	fmt.Fprintf(c.out, "  Daily avg PnL:         $%.4f/day\n", stats.DailyAvgPnL)
	if stats.DaysRunning >= 3 {
		monthly := stats.DailyAvgPnL * 30
		fmt.Fprintf(c.out, "  Projected monthly:     $%.2f/month\n", monthly)
		if stats.MaxCapital > 0 {
			apr := (stats.DailyAvgPnL / stats.MaxCapital) * 365 * 100
			fmt.Fprintf(c.out, "  Projected APR:         %.1f%%\n", apr)
		}
	}

	fmt.Fprintf(c.out, "\n  --- VERDICT ---\n")
	if stats.DaysRunning < 3 {
		fmt.Fprintf(c.out, "  Need at least 3 days of data. Currently %d days.\n", stats.DaysRunning)
		fmt.Fprintf(c.out, "  Keep running --paper and check back later.\n")
	} else if stats.NetPnL > 0 && stats.DailyAvgPnL > 0 {
		fmt.Fprintf(c.out, "  POSITIVE: Paper trading is net profitable.\n")
		if stats.PartialFills == 0 || (float64(stats.PartialFills)/float64(stats.TotalFills+1) < 0.3) {
			fmt.Fprintf(c.out, "  Partial fill risk: manageable (%.0f%%).\n",
				float64(stats.PartialFills)/float64(stats.TotalFills+1)*100)
			fmt.Fprintf(c.out, "  Queue-adjusted fills: YES (realistic simulation).\n")
			fmt.Fprintf(c.out, "  Maker fee: 0%% (verified for Polymarket).\n")
			if stats.DaysRunning >= 7 {
				fmt.Fprintf(c.out, "  >>> READY to move to capital minimo ($25/side, 2 markets).\n")
			} else {
				fmt.Fprintf(c.out, "  >>> Promising. Run %d more days for full confidence.\n", 7-stats.DaysRunning)
			}
		} else {
			fmt.Fprintf(c.out, "  WARNING: High partial fill rate. Consider longer observation.\n")
		}
	} else {
		fmt.Fprintf(c.out, "  NEGATIVE: Paper trading is not profitable.\n")
		fmt.Fprintf(c.out, "  Do NOT use real money. Review strategy.\n")
	}

	fmt.Fprintln(c.out)
}
