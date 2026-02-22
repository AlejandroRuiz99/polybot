package storage_test

import (
	"context"
	"testing"
	"time"

	"github.com/alejandrodnm/polybot/internal/adapters/storage"
	"github.com/alejandrodnm/polybot/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeOpportunity(condID string, score float64) domain.Opportunity {
	return domain.Opportunity{
		Market: domain.Market{
			ConditionID: condID,
			Question:    "Will X happen?",
			Slug:        "will-x-happen",
			Rewards:     domain.RewardConfig{DailyRate: 25, MaxSpread: 0.04},
		},
		ScannedAt:       time.Now().UTC().Truncate(time.Second),
		SpreadTotal:     0.02,
		RewardScore:     score,
		Competition:     5000,
		NetProfitEst:    score - 0.2,
		QualifiesReward: true,
	}
}

func TestSQLiteStorage_SaveAndGetHistory(t *testing.T) {
	db, err := storage.NewSQLiteStorage(":memory:")
	require.NoError(t, err)
	defer db.Close()

	opps := []domain.Opportunity{
		makeOpportunity("0xaaa", 24.0),
		makeOpportunity("0xbbb", 12.5),
	}

	err = db.SaveScan(context.Background(), opps)
	require.NoError(t, err)

	from := time.Now().UTC().Add(-time.Minute)
	to := time.Now().UTC().Add(time.Minute)
	history, err := db.GetHistory(context.Background(), from, to)
	require.NoError(t, err)
	require.Len(t, history, 2)

	// Ordenados por score desc
	assert.InDelta(t, 24.0, history[0].RewardScore, 0.001)
	assert.InDelta(t, 12.5, history[1].RewardScore, 0.001)
	assert.Equal(t, "0xaaa", history[0].Market.ConditionID)
}

func TestSQLiteStorage_SaveEmptySlice(t *testing.T) {
	db, err := storage.NewSQLiteStorage(":memory:")
	require.NoError(t, err)
	defer db.Close()

	err = db.SaveScan(context.Background(), nil)
	assert.NoError(t, err)
}

func TestSQLiteStorage_GetHistory_EmptyRange(t *testing.T) {
	db, err := storage.NewSQLiteStorage(":memory:")
	require.NoError(t, err)
	defer db.Close()

	// Sin datos
	history, err := db.GetHistory(context.Background(),
		time.Now().Add(-time.Hour),
		time.Now(),
	)
	require.NoError(t, err)
	assert.Empty(t, history)
}

func TestSQLiteStorage_MultipleSaves(t *testing.T) {
	db, err := storage.NewSQLiteStorage(":memory:")
	require.NoError(t, err)
	defer db.Close()

	ctx := context.Background()

	// Primer ciclo
	err = db.SaveScan(ctx, []domain.Opportunity{makeOpportunity("0x001", 10)})
	require.NoError(t, err)

	// Segundo ciclo
	err = db.SaveScan(ctx, []domain.Opportunity{makeOpportunity("0x001", 15), makeOpportunity("0x002", 5)})
	require.NoError(t, err)

	history, err := db.GetHistory(ctx, time.Now().Add(-time.Minute), time.Now().Add(time.Minute))
	require.NoError(t, err)
	assert.Len(t, history, 3)
}
