package notify_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alejandrodnm/polybot/internal/adapters/notify"
	"github.com/alejandrodnm/polybot/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeOpp(question string, yourReward, fillCost float64) domain.Opportunity {
	be := yourReward / fillCost
	return domain.Opportunity{
		Market: domain.Market{
			ConditionID: "0xtest",
			Question:    question,
			Rewards:     domain.RewardConfig{DailyRate: 25, MaxSpread: 0.04},
		},
		ScannedAt:       time.Now(),
		SpreadTotal:     0.02,
		YourDailyReward: yourReward,
		FillCostPerPair: 0.01,
		FillCostUSDC:    fillCost,
		BreakEvenFills:  be,
		PnLNoFills:      yourReward,
		PnL1Fill:        yourReward - fillCost,
		PnL3Fills:       yourReward - fillCost*3,
		CombinedScore:   yourReward - fillCost, // PnL 1fill
		Category:        domain.CategorySilver,
		Competition:     3000,
		QualifiesReward: true,
	}
}

func TestConsole_Compact_OneLine(t *testing.T) {
	var buf bytes.Buffer
	n := notify.NewConsoleWriter(&buf, false, false)

	opps := []domain.Opportunity{
		makeOpp("Will Trump win?", 0.50, 0.10),
		makeOpp("Will BTC hit 100k?", 0.30, 0.08),
	}

	err := n.Notify(context.Background(), opps)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "Will Trump win?")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	assert.Len(t, lines, 1, "compact mode is 1 line")
}

func TestConsole_Table_ShowsScenarios(t *testing.T) {
	var buf bytes.Buffer
	n := notify.NewConsoleWriter(&buf, true, false)

	opps := []domain.Opportunity{makeOpp("Test market", 0.50, 0.10)}

	err := n.Notify(context.Background(), opps)
	require.NoError(t, err)

	out := buf.String()
	// tablewriter uppercases headers and may insert spaces
	assert.Contains(t, out, "RWD")
	assert.Contains(t, out, "FILL")
	assert.Contains(t, out, "VERDICT")
}

func TestConsole_Empty(t *testing.T) {
	var buf bytes.Buffer
	n := notify.NewConsoleWriter(&buf, false, false)
	err := n.Notify(context.Background(), nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "no opportunities found")
}

func TestConsole_Compact_LongNameTruncated(t *testing.T) {
	var buf bytes.Buffer
	n := notify.NewConsoleWriter(&buf, false, false)

	longQ := strings.Repeat("A", 50)
	opps := []domain.Opportunity{makeOpp(longQ, 0.50, 0.10)}
	err := n.Notify(context.Background(), opps)
	require.NoError(t, err)
	assert.NotContains(t, buf.String(), strings.Repeat("A", 50))
}
