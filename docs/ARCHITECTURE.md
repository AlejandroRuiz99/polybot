# Polybot — Arquitectura del Sistema

Bot de **reward farming** para Polymarket. Coloca órdenes maker en ambos lados (YES+NO) de mercados de predicción binarios para capturar rewards de liquidez, y ejecuta merges on-chain para convertir los tokens en USDC.

---

## Estructura de Directorios

```
polybot/
├── cmd/polybot/              # Entrypoint — wiring de dependencias y CLI
├── config/                   # Carga YAML + .env + defaults
├── internal/
│   ├── domain/               # Entidades puras, cero imports externos
│   │   └── strategy/         # Estrategia de análisis (servicio de dominio)
│   ├── ports/                # Interfaces (contratos entre capas)
│   ├── adapters/             # Implementaciones concretas de los ports
│   │   ├── polymarket/       # HTTP clients (CLOB, Gamma, Data API, Trading)
│   │   ├── storage/          # SQLite (scanner, paper, live)
│   │   ├── notify/           # Consola (scanner, paper, live)
│   │   └── onchain/          # Polygon blockchain (merges, approvals, gas)
│   └── application/          # Capa de aplicación (orquestación)
│       ├── scanner/          # Búsqueda, análisis, filtrado y ranking de mercados
│       └── engine/           # Motores de ejecución
│           ├── paper/        # Simulación sin dinero real
│           └── live/         # Trading con dinero real
└── config/config.yaml        # Configuración por defecto
```

---

## Flujo General del Sistema

```
                    ┌─────────────┐
                    │  cmd/polybot │  ← CLI flags + config.yaml
                    │   main.go    │
                    └──────┬──────┘
                           │
              ┌────────────┼────────────┐
              │            │            │
         ─────scan──  ──paper──  ───live───
              │            │            │
              ▼            ▼            ▼
         ┌────────┐  ┌──────────┐  ┌──────────┐
         │Scanner │  │PaperEng  │  │LiveEng   │
         │  .Run()│  │.RunOnce()│  │.RunOnce()│
         └───┬────┘  └────┬─────┘  └────┬─────┘
             │             │             │
    ┌────────┴────────┐    │             │
    │ 1. FetchMarkets │    │             │
    │ 2. FetchBooks   │    │             │
    │ 3. Analyze ×N   │ Scanner.RunOnce()│
    │ 4. Filter       │◄───────────────-─┘
    │ 5. Rank         │
    └─────────────────┘
```

---

## 1. `cmd/polybot/` — Entrypoint

### `main.go` (146 líneas)

Punto de entrada. Parsea CLI flags, carga config, construye dependencias y lanza el modo seleccionado.

| Flag | Descripción |
|------|-------------|
| `--config` | Ruta al config YAML (default: `config/config.yaml`) |
| `--once` | Ejecuta un solo ciclo de scan y sale |
| `--dry-run` | Usa fixtures locales en vez de API real |
| `--verbose` | Log level debug |
| `--table` | Imprime tabla completa con portfolio |
| `--validate` | Imprime cálculo paso a paso de top 3 |
| `--paper` | Modo paper trading (simulación) |
| `--paper-report` | Imprime reporte de paper y sale |
| `--live` | Modo REAL MONEY trading |
| `--live-report` | Imprime reporte live y sale |

**Wiring de dependencias:**
1. `config.Load()` → Config
2. `polymarket.NewClient()` → MarketProvider + BookProvider
3. `storage.NewSQLiteStorage()` → Storage
4. `notify.NewConsole()` → Notifier
5. `strategy.NewRewardFarming()` → Strategy (inyectada al scanner)
6. `scanner.New(cfg, client, client, store, notifier, strat)` → Scanner

**Modos de operación:**
- **default** → `s.Run(ctx)` — loop de scanner continuo
- **paper** → `runPaper()` — scanner + simulación de órdenes
- **live** → `runLive()` — scanner + órdenes reales en CLOB

### `paper.go` (113 líneas)

Bootstrap del paper engine. Inicializa schema, crea `papereng.New()` y ejecuta un loop cada 60s llamando `pe.RunOnce()`. Al salir imprime un reporte con `PrintPaperReport()`.

### `live.go` (187 líneas)

Bootstrap del live engine. Validaciones previas:
- Requiere `POLY_PRIVATE_KEY` env var
- 5 segundos de espera para abortar
- Crea `AuthClient` con autenticación L1/L2
- Crea `TradingClient` y `MergeClient`
- Verifica approvals on-chain
- Valida balance CLOB suficiente
- Restaura estado del circuit breaker

Loop cada 60s llamando `le.RunOnce()`. Se puede detener con Ctrl+C o creando un archivo `STOP_LIVE`.

### `logger.go` (31 líneas)

Configura `slog` con nivel (debug/info/warn/error) y formato (text/json).

---

## 2. `config/` — Configuración

### `config.go` (181 líneas)

Carga config YAML → aplica overrides de `.env` → aplica defaults.

**Structs principales:**

| Struct | Campos clave |
|--------|-------------|
| `Config` | Scanner, Paper, Live, API, Storage, Log |
| `ScannerConfig` | `interval_seconds`, `order_size_usdc`, `fee_rate_default`, filtros, workers |
| `PaperConfig` | `max_markets` (10), `initial_capital` (1000) |
| `LiveConfig` | `order_size` (5), `max_markets` (5), `initial_capital` (20), `max_exposure` (50), `min_merge_profit` (0.05), `polygon_rpc`, filtros |
| `APIConfig` | `clob_base`, `gamma_base` |
| `StorageConfig` | `dsn` (ruta SQLite) |
| `LogConfig` | `level`, `format` |

---

## 3. `internal/domain/` — Entidades de Dominio

Capa pura sin imports externos. Contiene las entidades de negocio y la lógica de cálculo.

### `market.go` (99 líneas)

```
Market
├── ConditionID, QuestionID, Question, Slug
├── EndDate, Volume24h, MakerBaseFee
├── Tokens [2]Token  {TokenID, Outcome, Price}
├── Rewards RewardConfig  {DailyRate, MinSize, MaxSpread}
├── Active, Closed
│
├── HasRewards() bool
├── HoursToResolution() float64
├── EffectiveFeeRate(default) float64
├── YesToken() / NoToken() Token
└── TruncateQuestion(q, cid, maxLen) string
```

### `orderbook.go` (122 líneas)

```
OrderBook
├── TokenID
├── Bids []BookEntry  (mayor → menor precio)
├── Asks []BookEntry  (menor → mayor precio)
│
├── BestBid() / BestAsk() / Midpoint() / Spread()
├── DepthWithin(maxSpread) float64          ← volumen total en tokens
├── DepthWithinUSDC(maxSpread) float64      ← volumen en USDC (size × price)
└── BidDepthWithinUSDC(maxSpread) float64   ← solo bids (competencia real)
```

### `opportunity.go` (84 líneas)

Resultado del análisis de un mercado. Contiene:
- **Spread y calificación**: `SpreadTotal`, `QualifiesReward`
- **Arbitraje**: `ArbitrageResult`
- **Tu reward**: `Competition`, `YourShare`, `SpreadScore`, `YourDailyReward`
- **Costes de fill**: `FillCostPerPair`, `FillCostUSDC`, `BreakEvenFills`
- **P&L escenarios**: `PnLNoFills`, `PnL1Fill`, `PnL3Fills`
- **Ranking**: `CombinedScore` (= PnL1Fill), `Category` (Gold/Silver/Bronze/Avoid)

Métodos: `Verdict()` → FILLS=PROFIT / SAFE / OK / RISKY / AVOID

### `arbitrage.go` (167 líneas)

Análisis de arbitraje a múltiples profundidades del orderbook:

```
ArbitrageResult
├── BestAskYES, BestAskNO       ← nivel superficial
├── SumBestAsk, FeesTotal
├── ArbitrageGap                 ← 1.0 - sum - fees (>0 = true arb)
├── HasArbitrage
├── MaxFillable                  ← min(depthYES, depthNO)
└── AtDepth []DepthLevel         ← análisis a $50, $100, $200, $500

CalculateArbitrage(yesBook, noBook, feeRate) → ArbitrageResult
VolumeWeightedPrice(asks, maxUSDC) → avgPrice
```

### `scoring.go` (142 líneas)

Funciones de cálculo de métricas:

| Función | Qué calcula |
|---------|------------|
| `EstimateYourDailyReward()` | `dailyRate × (orderSize / (orderSize+competition)) × spreadScore²` |
| `FillCostPerEvent()` | `(yesP + noP)(1+fee) - 1.0` → coste por fill pair |
| `FillCostUSDC()` | Coste en USDC por fill completo |
| `BreakEvenFills()` | `reward / fillCost` → fills/día antes de perder |
| `EstimateNetProfit()` | `reward - fillCost × fillsPerDay` |
| `ComputeCombinedScore()` | reward + arb bonus (solo si true arb) |
| `Categorize()` | Gold (gap > -2%) / Silver (gap > -5%) / Bronze / Avoid |

### `paper.go` (130 líneas)

Entidades para paper trading:
- `VirtualOrder` — orden simulada con queue, reward, pair tracking
- `PaperFill` — fill simulado
- `PaperPosition` — estado de posición (YES+NO linked by pairID)
- `PaperDailySummary` / `PaperStats` — métricas agregadas

### `live.go` (204 líneas)

Entidades para live trading:
- `LiveOrder` — orden real con CLOBOrderID, NegRisk, CompetitionAt
- `LiveFill` — fill real detectado
- `MergeResult` — resultado de merge on-chain (TxHash, GasCost, SpreadProfit)
- `LivePosition` — posición real con reward accrual
- `CircuitBreaker` — protección contra pérdidas consecutivas
- `LiveDailySummary` / `LiveStats` — métricas agregadas
- `PlaceOrderRequest` / `PlacedOrder` — DTOs de ejecución

### `trade.go` (14 líneas)

`Trade` — trade histórico de la API (ID, TokenID, Side, Price, Size, Timestamp).

### `strategy/strategy.go` (15 líneas)

Interface de estrategia:
```go
type Strategy interface {
    Analyze(ctx, market, yesBook, noBook) (Opportunity, error)
}
```

### `strategy/reward_farming.go` (117 líneas)

Implementación de reward farming. Calcula todas las métricas de una `Opportunity`:
1. Spread total
2. Arbitraje a múltiples profundidades
3. Competencia en el book
4. Tu share del reward pool
5. Fill cost per pair
6. Break-even fills
7. P&L bajo 3 escenarios (0, 1, 3 fills/día)
8. Categorización (Gold/Silver/Bronze/Avoid)

---

## 4. `internal/ports/` — Interfaces

| Archivo | Interface | Métodos clave | Usado por |
|---------|-----------|---------------|-----------|
| `market_provider.go` | `MarketProvider` | `FetchSamplingMarkets()` | Scanner |
| `book_provider.go` | `BookProvider` | `FetchOrderBooks()` | Scanner, Engines |
| `trade_provider.go` | `TradeProvider` | `FetchTrades()` | Paper Engine |
| `notifier.go` | `Notifier` | `Notify(opps)` | Scanner |
| `storage.go` | `Storage` | `SaveScan()`, `GetHistory()`, `Close()` | Scanner |
| `paper_storage.go` | `PaperStorage` | SaveOrder, MarkFilled, MarkMerged, GetOpen, Stats... | Paper Engine |
| `live_storage.go` | `LiveStorage` | SaveOrder, UpdateFill, SaveMerge, CircuitBreaker, Stats... | Live Engine |
| `executor.go` | `OrderExecutor` | `PlaceOrder()`, `CancelOrder()`, `GetBalance()`, `TokenBalance()` | Live Engine |
| `executor.go` | `MergeExecutor` | `MergePositions()`, `EstimateGasCostUSD()`, `EnsureApprovals()` | Live Engine |

---

## 5. `internal/adapters/` — Implementaciones

### `polymarket/` — API Clients

| Archivo | Qué hace |
|---------|----------|
| `client.go` (146 líneas) | HTTP client base con rate limiting (token bucket) y retries con backoff exponencial. 3 limiters: books (30/s), gamma (18/s), general (540/s) |
| `types.go` (88 líneas) | DTOs raw de las APIs (CLOB y Gamma). Nunca salen del paquete |
| `mapping.go` | Convierte DTOs raw → `domain.Market`, `domain.OrderBook` |
| `clob.go` | `FetchSamplingMarkets()` — paginación automática con cursor. `FetchOrderBooks()` — batch de 20 tokens en paralelo con goroutines |
| `gamma.go` | `EnrichWithGamma()` — añade question, slug, endDate, volume24h, fee a los mercados |
| `trades.go` (106 líneas) | `FetchTrades()` — trades históricos de la Data API (3 páginas máx, 1000/página) |
| `auth.go` | `AuthClient` — autenticación L1 (EIP-712 signature) + L2 (HMAC-SHA256). Deriva API credentials desde private key |
| `trading.go` | `TradingClient` — implementa `OrderExecutor`. Place/Cancel/GetOpenOrders vía CLOB API autenticada + `TokenBalance()` on-chain ERC-1155 |

### `storage/` — SQLite

| Archivo | Qué hace |
|---------|----------|
| `sqlite.go` (366 líneas) | Storage del scanner. Schema: `cycles` + `opportunities`. Cache en memoria para evitar writes redundantes (~90% reducción). Prune automático (cycles 30d, opps 14d) |
| `paper.go` | Implementa `PaperStorage`. Schema: `paper_orders`, `paper_fills`, `paper_dailies`. CRUD para órdenes virtuales, fills simulados, estadísticas |
| `live.go` | Implementa `LiveStorage`. Schema: `live_orders`, `live_fills`, `merge_results`, `live_dailies`, `circuit_breaker`. CRUD para órdenes reales, merges, circuit breaker |

### `notify/` — Output de Consola

| Archivo | Qué imprime |
|---------|------------|
| `console.go` (361 líneas) | **Scanner**: compact (1 línea), table (tabla + portfolio), validation (cálculo detallado top 3) |
| `console_paper.go` | **Paper**: `PrintPaperStatus()` (1 línea por ciclo), `PrintPaperReport()` (reporte completo con dailies, aggregate, compound rotation, verdict) |
| `console_live.go` (95 líneas) | **Live**: `PrintLiveReport()` (open orders, partial fills, dailies, circuit breaker) |

### `onchain/` — Blockchain

| Archivo | Qué hace |
|---------|----------|
| `merge.go` (~640 líneas) | `MergeClient` — interacción con Polygon. Merge de YES+NO tokens → USDC.e via CTF contract. Gas dinámico, ERC1155 approvals, 3 contracts (CTFExchange, NegRiskCTFExchange, NegRiskAdapter). Estimación de gas en USD via CoinGecko |

---

## 6. `internal/application/` — Capa de Aplicación

### `scanner/` — Búsqueda y Análisis

El pipeline del scanner en cada ciclo:

```
FetchSamplingMarkets() ──► FetchOrderBooks() ──► Analyze ×N ──► Filter ──► Rank
      (CLOB API)           (CLOB /books)      (paralelo)    (criterios)  (score)
```

| Archivo | Qué hace |
|---------|----------|
| `scanner.go` (241 líneas) | Orquestador principal. `Run()` = loop continuo. `RunOnce()` = 1 ciclo. Emite alertas para Gold nuevos y true arbitrage. Helpers: extractTokenIDs, rankByScore |
| `analyzer.go` (28 líneas) | Delega a `StrategyAnalyzer` (inyectado). Puente entre scanner y strategy |
| `filter.go` (89 líneas) | Filtros configurables: MinReward, MaxSpread, MaxCompetition, MinHoursToResolution, OnlyFillsProfit, RequireQualifies |
| `concurrent.go` (94 líneas) | Worker pool para análisis paralelo. `NumCPU × 2` workers por defecto. Reduce ciclo de ~20s a ~3-5s |

### `engine/engine.go` (41 líneas)

Código compartido entre engines:
- `ScannerService` interface — `RunOnce(ctx) ([]Opportunity, error)`
- `QueuePosition()` — calcula USDC ahead en el book (FIFO)
- `TruncateStr()` — helper de display

### `engine/paper/` — Paper Trading Engine

Simulación completa sin dinero real. Cada ciclo (`RunOnce`):

```
1. Scan mercados (via Scanner.RunOnce)
2. Expirar resolved / near-end
3. Refresh queues de órdenes abiertas
4. Rotar órdenes stale (>4h, spread roto, competencia ×3)
5. Check fills (fetch trades reales, simular queue-aware filling)
6. Merge pares completos (simular CTF merge)
7. Capital allocation (Kelly Criterion)
8. Colocar nuevas órdenes (ranking por compound velocity)
9. Build positions + alertas
10. Save daily summary
```

| Archivo | Qué hace |
|---------|----------|
| `engine.go` (253 líneas) | `RunOnce()` — orquesta los 10 pasos. Config: OrderSize, MaxMarkets, FeeRate, InitialCapital |
| `simulation.go` (414 líneas) | `placeVirtualOrders()` — bid optimization multi-tick. `checkFills()` — simulación queue-aware con trades reales. `expireResolvedAndNearEnd()`, `refreshQueues()` |
| `rotation.go` (600 líneas) | `rotateStaleOrders()` — cancela pares sin fills >4h o con spread roto. `mergeCompletePairs()` — simula merge con gas estimado. `kellyFraction()` — Half-Kelly desde historial. `optimalOrderSize()` — sizing adaptivo por competencia. `buildPositions()` — reward accrual por bloques de 15min |

### `engine/live/` — Live Trading Engine

Trading real en Polymarket CLOB + merges on-chain. Cada ciclo (`RunOnce`):

```
1. PROTECTION: Circuit breaker check
2. DISCOVERY: Get balance + scan markets
3. VERIFICATION: Sync order state + spread history
4. MAINTENANCE: Cancel resolved + rotate stale
5. MERGE: Execute on-chain merges
6. CAPITAL: Kelly allocation + exposure limits
7. PLACEMENT: Gate checks + place order pairs
8. REPORTING: Build positions + alerts + save daily
```

| Archivo | Qué hace |
|---------|----------|
| `engine.go` (251 líneas) | `RunOnce()` — orquesta las 8 fases. Config: OrderSize, MaxMarkets, InitialCapital, MaxExposure, MinMergeProfit. CircuitBreaker integrado. Spread history tracking |
| `orders.go` (403 líneas) | `placeOrderPair()` — optimización de bid por EV (Expected Value). `syncOrderState()` — poll CLOB, detectar fills, reconciliar con on-chain token balance. `spreadStable()` — verifica estabilidad de spread en ventana de 3 scans. NegRisk detection y skip |
| `placement.go` (270 líneas) | `runPlacementPipeline()` — pipeline de filtrado con gate checks (volumen 24h, ask depth, spread%, fill cost, horas, spread stability). `calculateOrderSize()` — respeta balance, exposure, min shares. Stats de skip reasons |
| `capital.go` (249 líneas) | `calculateDeployedCapital()` — suma por estado (open/partial/filled). `getCompoundMetrics()` — P&L desde merge history. `kellyFraction()` — Half-Kelly real. `buildPositions()` — portfolio view con reward accrual. `saveDailySummary()` + `velocityScore()` |
| `merge.go` (125 líneas) | `mergeCompletePairs()` — ejecuta merges on-chain reales. Calcula shares mergeables, estima gas, valida profit mínimo, llama `merger.MergePositions()`, registra en CircuitBreaker |
| `rotation.go` (216 líneas) | `cancelResolvedOrders()` — cancela órdenes de mercados resolved pero **protege pares con fills** (verificación on-chain de token balance). `rotateStaleOrders()` — rota pares sin fills >4h o con competencia spike ×3 |

---

## 7. Seguridad y Protecciones (Live)

| Mecanismo | Ubicación | Descripción |
|-----------|-----------|-------------|
| **Circuit Breaker** | `domain/live.go` + `engine/live/engine.go` | 3 pérdidas consecutivas → cooldown 30min. Drawdown > 5% capital → stop total |
| **Spread Stability** | `engine/live/orders.go` | Requiere spread estable en 3 scans consecutivos antes de operar |
| **Gate Checks** | `engine/live/placement.go` | 10+ filtros: volumen 24h, ask depth, spread%, fill cost, horas, NegRisk |
| **Fill Protection** | `engine/live/rotation.go` | No cancela pares con fills — verificación on-chain de token balance |
| **Kelly Criterion** | `engine/live/capital.go` | Half-Kelly desde merge history real. Límite de exposure configurable |
| **NegRisk Skip** | `engine/live/orders.go` | Detecta mercados NegRisk (merge no soportado) y los evita |
| **5s Abort Window** | `cmd/polybot/live.go` | 5 segundos para abortar antes de empezar |
| **STOP_LIVE File** | `cmd/polybot/live.go` | Crear archivo `STOP_LIVE` para shutdown graceful |

---

## 8. Separación Paper vs Live

| Capa | Compartido | Paper | Live |
|------|------------|-------|------|
| **domain** | market, orderbook, opportunity, scoring, arbitrage, trade | `paper.go` | `live.go` |
| **ports** | storage, notifier, book_provider, market_provider | `paper_storage.go` | `live_storage.go`, `executor.go` |
| **adapters/storage** | `sqlite.go` | `paper.go` | `live.go` |
| **adapters/notify** | `console.go` | `console_paper.go` | `console_live.go` |
| **application/engine** | `engine.go` | `paper/` (3 archivos) | `live/` (5 archivos) |
| **application/scanner** | Todo compartido | — | — |

---

## 9. Diagrama de Dependencias

```
cmd/polybot/main.go
    │
    ├── config.Load()
    ├── polymarket.NewClient()         ──► implements MarketProvider, BookProvider, TradeProvider
    ├── storage.NewSQLiteStorage()     ──► implements Storage, PaperStorage, LiveStorage
    ├── notify.NewConsole()            ──► implements Notifier
    ├── strategy.NewRewardFarming()    ──► implements Strategy
    │
    ├── scanner.New(cfg, market, book, store, notifier, strategy)
    │       │
    │       ├── cycle: FetchMarkets → FetchBooks → Analyze(strategy) → Filter → Rank
    │       └── RunOnce() → []Opportunity
    │
    ├── papereng.New(scanner, trades, store, cfg)
    │       └── RunOnce: scan → expire → refresh → rotate → checkFills → merge → place
    │
    └── liveeng.New(scanner, books, executor, merger, store, cfg)
            └── RunOnce: protect → scan → sync → maintain → merge → allocate → place → report
```

**Regla hexagonal**: domain no importa nada externo. Los adapters importan ports + domain. La application importa ports + domain. El cmd conecta todo.
