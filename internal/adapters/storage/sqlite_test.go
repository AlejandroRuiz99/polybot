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

// makeGoldOpp crea una oportunidad Gold con los valores dados.
// Gold = arb + rewards → se debe persistir.
func makeGoldOpp(condID string, combined float64) domain.Opportunity {
	return domain.Opportunity{
		Market: domain.Market{
			ConditionID: condID,
			Question:    "Will X happen?",
			Slug:        "will-x-happen",
			Rewards:     domain.RewardConfig{DailyRate: 25, MaxSpread: 0.04},
		},
		ScannedAt:       time.Now().UTC().Truncate(time.Second),
		SpreadTotal:     0.02,
		CombinedScore:   combined,
		YourDailyReward: combined * 0.7,
		Arbitrage: domain.ArbitrageResult{
			HasArbitrage: true,
			ArbitrageGap: 0.02,
			BestAskYES:   0.48,
			BestAskNO:    0.48,
			SumBestAsk:   0.96,
			MaxFillable:  200,
		},
		Category:        domain.CategoryGold,
		Competition:     5000,
		NetProfitEst:    combined - 0.2,
		QualifiesReward: true,
	}
}

// makeSilverOpp crea una oportunidad Silver (sin arb, rewards seguros).
func makeSilverOpp(condID string, combined float64) domain.Opportunity {
	opp := makeGoldOpp(condID, combined)
	opp.Arbitrage.HasArbitrage = false
	opp.Arbitrage.ArbitrageGap = -0.005
	opp.Category = domain.CategorySilver
	return opp
}

// makeAvoidOpp crea una oportunidad Avoid — NO debe persistirse.
func makeAvoidOpp(condID string) domain.Opportunity {
	opp := makeGoldOpp(condID, 0.01)
	opp.Category = domain.CategoryAvoid
	return opp
}

func TestSQLiteStorage_SaveAndGetHistory(t *testing.T) {
	db, err := storage.NewSQLiteStorage(":memory:")
	require.NoError(t, err)
	defer db.Close()

	opps := []domain.Opportunity{
		makeGoldOpp("0xaaa", 1.69),
		makeSilverOpp("0xbbb", 1.10),
	}

	err = db.SaveScan(context.Background(), opps)
	require.NoError(t, err)

	from := time.Now().UTC().Add(-time.Minute)
	to := time.Now().UTC().Add(time.Minute)
	history, err := db.GetHistory(context.Background(), from, to)
	require.NoError(t, err)
	require.Len(t, history, 2)

	// Ordenados por combined_score desc
	assert.InDelta(t, 1.69, history[0].CombinedScore, 0.01)
	assert.InDelta(t, 1.10, history[1].CombinedScore, 0.01)
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

	history, err := db.GetHistory(context.Background(),
		time.Now().Add(-time.Hour),
		time.Now(),
	)
	require.NoError(t, err)
	assert.Empty(t, history)
}

func TestSQLiteStorage_Upsert_SameMarketTwice(t *testing.T) {
	db, err := storage.NewSQLiteStorage(":memory:")
	require.NoError(t, err)
	defer db.Close()

	ctx := context.Background()

	// Primer ciclo: score 1.0
	err = db.SaveScan(ctx, []domain.Opportunity{makeGoldOpp("0x001", 1.0)})
	require.NoError(t, err)

	// Segundo ciclo: score cambia más de 5% → debe actualizar la fila
	err = db.SaveScan(ctx, []domain.Opportunity{makeGoldOpp("0x001", 1.5)})
	require.NoError(t, err)

	from := time.Now().UTC().Add(-time.Minute)
	to := time.Now().UTC().Add(time.Minute)
	history, err := db.GetHistory(ctx, from, to)
	require.NoError(t, err)

	// Una sola fila (upsert, no duplicado)
	require.Len(t, history, 1)
	assert.InDelta(t, 1.5, history[0].CombinedScore, 0.01, "debe reflejar el último valor")
}

func TestSQLiteStorage_Cache_SkipsUnchanged(t *testing.T) {
	db, err := storage.NewSQLiteStorage(":memory:")
	require.NoError(t, err)
	defer db.Close()

	ctx := context.Background()
	opp := makeGoldOpp("0xstable", 1.0)

	// Primer ciclo: escribe
	require.NoError(t, db.SaveScan(ctx, []domain.Opportunity{opp}))

	// Segundo ciclo: mismo score (sin cambio) → cache impide reescribir
	// El comportamiento observable es que sigue siendo 1 fila con el mismo valor
	require.NoError(t, db.SaveScan(ctx, []domain.Opportunity{opp}))

	from := time.Now().UTC().Add(-time.Minute)
	to := time.Now().UTC().Add(time.Minute)
	history, err := db.GetHistory(ctx, from, to)
	require.NoError(t, err)
	require.Len(t, history, 1)
}

func TestSQLiteStorage_FiltersAvoidAndBronze(t *testing.T) {
	db, err := storage.NewSQLiteStorage(":memory:")
	require.NoError(t, err)
	defer db.Close()

	ctx := context.Background()

	avoid := makeAvoidOpp("0xavoid")
	bronze := makeGoldOpp("0xbronze", 0.5)
	bronze.Category = domain.CategoryBronze
	gold := makeGoldOpp("0xgold", 1.5)

	err = db.SaveScan(ctx, []domain.Opportunity{avoid, bronze, gold})
	require.NoError(t, err)

	from := time.Now().UTC().Add(-time.Minute)
	to := time.Now().UTC().Add(time.Minute)
	history, err := db.GetHistory(ctx, from, to)
	require.NoError(t, err)

	// Solo Gold persiste
	require.Len(t, history, 1)
	assert.Equal(t, "0xgold", history[0].Market.ConditionID)
}

func TestSQLiteStorage_MultipleDifferentMarkets(t *testing.T) {
	db, err := storage.NewSQLiteStorage(":memory:")
	require.NoError(t, err)
	defer db.Close()

	ctx := context.Background()

	err = db.SaveScan(ctx, []domain.Opportunity{
		makeGoldOpp("0x001", 1.5),
		makeSilverOpp("0x002", 1.0),
		makeGoldOpp("0x003", 2.0),
	})
	require.NoError(t, err)

	from := time.Now().UTC().Add(-time.Minute)
	to := time.Now().UTC().Add(time.Minute)
	history, err := db.GetHistory(ctx, from, to)
	require.NoError(t, err)

	require.Len(t, history, 3)
	// Ordenados por combined_score desc
	assert.InDelta(t, 2.0, history[0].CombinedScore, 0.01)
}
