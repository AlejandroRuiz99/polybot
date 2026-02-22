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

func makeOpp(question string, yourReward float64, qualifies bool) domain.Opportunity {
	return domain.Opportunity{
		Market: domain.Market{
			ConditionID: "0xtest",
			Question:    question,
			Rewards:     domain.RewardConfig{DailyRate: 25, MaxSpread: 0.04},
		},
		ScannedAt:       time.Now(),
		SpreadTotal:     0.02,
		RewardScore:     yourReward * 50, // legacy score
		YourDailyReward: yourReward,
		Competition:     3000,
		NetProfitEst:    yourReward - 0.2,
		QualifiesReward: qualifies,
	}
}

func TestConsole_Notify_WithOpportunities(t *testing.T) {
	var buf bytes.Buffer
	n := notify.NewConsoleWriter(&buf) // usa orderSize=100 por defecto

	opps := []domain.Opportunity{
		makeOpp("Will Trump win?", 24.0, true),
		makeOpp("Will BTC hit 100k?", 12.5, false),
	}

	err := n.Notify(context.Background(), opps)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "Will Trump win?")
	assert.Contains(t, out, "Will BTC hit 100k?")
	assert.Contains(t, out, "24.00")
	assert.Contains(t, out, "12.50")
}

func TestConsole_Notify_EmptyList(t *testing.T) {
	var buf bytes.Buffer
	n := notify.NewConsoleWriter(&buf)

	err := n.Notify(context.Background(), nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No opportunities found")
}

func TestConsole_Notify_LongQuestionTruncated(t *testing.T) {
	var buf bytes.Buffer
	n := notify.NewConsoleWriter(&buf)

	longQ := strings.Repeat("A", 50)
	opps := []domain.Opportunity{makeOpp(longQ, 10, true)}

	err := n.Notify(context.Background(), opps)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "...")
}
