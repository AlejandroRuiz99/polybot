package notify

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/alejandrodnm/polybot/internal/domain"
	"github.com/olekukonko/tablewriter"
)

// PaperStatusInput bundles everything PrintPaperStatus needs.
type PaperStatusInput struct {
	Positions        []domain.PaperPosition
	NewOrders        int
	NewFills         int
	Alerts           []string
	Warnings         []string
	CapitalDeployed  float64
	Merges           int
	MergeProfit      float64
	CompoundBalance  float64
	TotalRotations   int
	TotalMergeProfit float64
	InitialCapital   float64
	AvgCycleHours    float64
	KellyFraction    float64
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

	if result.CompoundBalance > 0 || result.TotalRotations > 0 {
		growth := 0.0
		if result.InitialCapital > 0 {
			growth = ((result.InitialCapital + result.TotalMergeProfit) / result.InitialCapital - 1) * 100
		}
		fmt.Fprintf(&sb, " | bal $%.2f (+%.1f%%) | %d rot | K%.0f%%",
			result.CompoundBalance, growth, result.TotalRotations, result.KellyFraction*100)
		if result.Merges > 0 {
			fmt.Fprintf(&sb, " | +%d merge $%.4f", result.Merges, result.MergeProfit)
		}
	}

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
		tbl.Header("Date", "Pos", "Pairs", "Part", "FillY", "FillN", "Reward", "MrgPnL", "Net", "Cap$", "Rot", "Bal$")

		for _, d := range stats.Dailies {
			balLabel := "-"
			if d.CompoundBalance > 0 {
				balLabel = fmt.Sprintf("$%.0f", d.CompoundBalance)
			}
			tbl.Append(
				d.Date.Format("01-02"),
				fmt.Sprintf("%d", d.ActivePositions),
				fmt.Sprintf("%d", d.CompletePairs),
				fmt.Sprintf("%d", d.PartialFills),
				fmt.Sprintf("%d", d.FillsYes),
				fmt.Sprintf("%d", d.FillsNo),
				fmt.Sprintf("$%.4f", d.TotalReward),
				fmt.Sprintf("$%.4f", d.MergeProfit),
				fmt.Sprintf("$%.4f", d.NetPnL),
				fmt.Sprintf("$%.0f", d.CapitalDeployed),
				fmt.Sprintf("%d", d.Rotations),
				balLabel,
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

	fmt.Fprintf(c.out, "\n  --- COMPOUND ROTATION ---\n")
	fmt.Fprintf(c.out, "  Initial capital:       $%.0f\n", stats.InitialCapital)
	fmt.Fprintf(c.out, "  Total rotations:       %d\n", stats.TotalRotations)
	fmt.Fprintf(c.out, "  Total merge profit:    $%.4f\n", stats.TotalMergeProfit)
	if stats.InitialCapital > 0 {
		effectiveCap := stats.InitialCapital + stats.TotalMergeProfit
		growth := (effectiveCap/stats.InitialCapital - 1) * 100
		fmt.Fprintf(c.out, "  Effective capital:     $%.2f (+%.2f%%)\n", effectiveCap, growth)
		fmt.Fprintf(c.out, "  Compound balance:      $%.2f\n", stats.CompoundBalance)
	}
	if stats.TotalRotations > 0 && stats.InitialCapital > 0 {
		profitPerRotation := stats.TotalMergeProfit / float64(stats.TotalRotations)
		fmt.Fprintf(c.out, "  Profit/rotation:       $%.4f\n", profitPerRotation)

		if stats.AvgCycleHours >= 1.0 {
			fmt.Fprintf(c.out, "  Avg cycle time:        %.1f hours\n", stats.AvgCycleHours)
			cyclesPerDay := 24.0 / stats.AvgCycleHours
			fmt.Fprintf(c.out, "  Cycles/day (est):      %.1f\n", cyclesPerDay)
			dailyReturn := profitPerRotation * cyclesPerDay / stats.InitialCapital
			fmt.Fprintf(c.out, "  Est. daily return:     %.3f%%\n", dailyReturn*100)
			if dailyReturn > 0 && dailyReturn < 1.0 {
				monthly := stats.InitialCapital * math.Pow(1+dailyReturn, 30)
				yearly := stats.InitialCapital * math.Pow(1+dailyReturn, 365)
				fmt.Fprintf(c.out, "  Compound 30d:          $%.2f\n", monthly)
				fmt.Fprintf(c.out, "  Compound 365d:         $%.2f\n", yearly)
			}
		} else {
			fmt.Fprintf(c.out, "  Avg cycle time:        <1h (need more data for projections)\n")
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
