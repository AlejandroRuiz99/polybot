# Polybot — Polymarket Liquidity Rewards Scanner

Scanner que descubre oportunidades de **liquidity reward farming** en Polymarket. Escanea mercados con rewards activos cada 30s, calcula métricas de rentabilidad y muestra las mejores oportunidades en consola.

## Arquitectura

Hexagonal (ports & adapters):
- **Domain**: entidades puras + fórmula de scoring `S=((v-s)/v)²×b`
- **Ports**: interfaces `MarketProvider`, `BookProvider`, `Storage`, `Notifier`
- **Adapters**: Polymarket HTTP client, SQLite, console table
- **Scanner**: orquestador → fetch → analyze → filter → rank → notify

## Quickstart

```bash
cp .env.example .env
make build
make run

# Un solo ciclo (debug)
make run-once

# Con fixtures locales (sin API real)
make run-dry
```

## Comandos

```bash
make build       # compilar
make test        # tests con cobertura
make lint        # golangci-lint
make run         # ejecutar scanner en loop
make run-once    # un ciclo y salir
make run-dry     # dry-run con fixtures
```

## Flags CLI

| Flag | Default | Descripción |
|------|---------|-------------|
| `--config` | `config/config.yaml` | Archivo de configuración |
| `--once` | false | Ejecutar un ciclo y salir |
| `--dry-run` | false | Usar fixtures locales |
| `--verbose` | false | Log level debug |
| `--format` | text | Formato de log (text/json) |

## Rate limits API

- Escaneo cada 30s ≈ 20 req/min (límite real: ~3000 req/min)
- Backoff adaptativo en HTTP 429

## Docs

- [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) — diseño detallado
- [`docs/API_REFERENCE.md`](docs/API_REFERENCE.md) — endpoints Polymarket
- [`docs/DECISIONS.md`](docs/DECISIONS.md) — decisiones técnicas
- [`docs/GLOSSARY.md`](docs/GLOSSARY.md) — términos del dominio
