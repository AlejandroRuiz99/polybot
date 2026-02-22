# Architecture

## Hexagonal Architecture

```
cmd/scanner/main.go
     │
     ▼
internal/scanner/scanner.go   ← orquestador principal
     │
     ├── ports.MarketProvider  ← implemented by adapters/polymarket
     ├── ports.BookProvider    ← implemented by adapters/polymarket
     ├── ports.Storage         ← implemented by adapters/storage
     └── ports.Notifier        ← implemented by adapters/notify
          │
          ▼
internal/scanner/analyzer.go  ← calcula métricas por mercado
internal/scanner/filter.go    ← filtra oportunidades según config
internal/strategy/            ← lógica de reward farming
internal/domain/              ← entidades puras (sin dependencias)
```

## Flujo de un ciclo de escaneo

```
1. scanner.Run()
2.   → MarketProvider.FetchSamplingMarkets(ctx)   [CLOB /sampling-markets]
3.   → BookProvider.FetchOrderBooks(ctx, tokenIDs) [CLOB POST /books batch]
4.   → analyzer.Analyze(market, book)              [spread, score, competition]
5.   → filter.Apply(opportunities)                 [min score, max spread, etc.]
6.   → strategy.Rank(filtered)                     [ordenar por estimated_score]
7.   → Notifier.Notify(ctx, ranked)                [tabla en consola]
8.   → Storage.SaveScan(ctx, ranked)               [SQLite]
9.   → sleep(scanInterval)
```

## Paquetes y responsabilidades

| Paquete | Responsabilidad |
|---------|----------------|
| `domain` | Entidades, fórmulas puras. Cero imports externos |
| `ports` | Interfaces (contratos). Solo importa `domain` |
| `adapters/polymarket` | HTTP client, DTOs, mapping API→domain |
| `adapters/storage` | SQLite: schema, CRUD |
| `adapters/notify` | Console table output |
| `scanner` | Loop, coordinator, analyzer, filter |
| `strategy` | Reward farming strategy |
| `config` | YAML + .env loading |
| `cmd/scanner` | Wiring de dependencias, flags CLI |
