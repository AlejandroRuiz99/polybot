# Polymarket API Reference

## CLOB API — `https://clob.polymarket.com`

### GET /sampling-markets
Mercados actualmente seleccionados para rewards de liquidez.

**Query params:**
- `next_cursor` — paginación (Base64)
- `limit` — default 100, max 100

**Response:**
```json
{
  "limit": 100,
  "count": 87,
  "next_cursor": "abc123==",
  "data": [
    {
      "condition_id": "0x...",
      "question_id": "0x...",
      "tokens": [
        { "token_id": "123...", "outcome": "Yes", "price": 0.72, "winner": false },
        { "token_id": "456...", "outcome": "No",  "price": 0.28, "winner": false }
      ],
      "rewards": {
        "rates": [{ "asset_address": "0x...", "rewards_daily_rate": 25.5 }],
        "min_size": 10.0,
        "max_spread": 0.04
      },
      "active": true,
      "closed": false
    }
  ]
}
```

### POST /books
Batch orderbooks por token_id.

**Body:**
```json
[
  { "token_id": "123..." },
  { "token_id": "456..." }
]
```

**Response:**
```json
[
  {
    "asset_id": "123...",
    "bids": [{ "price": "0.70", "size": "150.0" }],
    "asks": [{ "price": "0.72", "size": "200.0" }]
  }
]
```

### GET /midpoints
**Query:** `?token_id=123&token_id=456`

**Response:** `{ "123": 0.71, "456": 0.29 }`

### GET /spreads
**Query:** `?token_id=123&token_id=456`

**Response:** `{ "123": 0.02, "456": 0.02 }`

---

## Gamma API — `https://gamma-api.polymarket.com`

### GET /markets
Metadata enriquecida de mercados.

**Query params:**
- `condition_ids` — lista separada por coma
- `limit`, `offset`

**Response:** lista de objetos con `question`, `slug`, `end_date_iso`, `volume`, `liquidity`.

---

## Rate Limits (Feb 2026)

| Endpoint | Límite | Seguro (60%) |
|----------|--------|--------------|
| CLOB general | 9000/10s | 5400/10s |
| POST /books | 500/10s | 300/10s |
| GET /midpoints | 500/10s | 300/10s |
| Gamma /markets | 300/10s | 180/10s |
