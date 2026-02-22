# Decisiones Técnicas

## ADR-001: Arquitectura Hexagonal
**Contexto**: Necesitamos mockear la API de Polymarket en tests sin llamadas reales.
**Decisión**: Ports & adapters. Los ports son interfaces en `internal/ports/`. Los adapters implementan esas interfaces.
**Consecuencia**: El scanner y la lógica de negocio son 100% testeables con mocks.

## ADR-002: `modernc.org/sqlite` en lugar de `go-sqlite3`
**Contexto**: `go-sqlite3` requiere CGo, que en Windows necesita MinGW/GCC.
**Decisión**: `modernc.org/sqlite` — pure Go, sin CGo, compatible en Windows/Linux/Mac sin configuración extra.
**Trade-off**: Ligeramente más lento en benchmarks intensivos, pero irrelevante para este caso de uso.

## ADR-003: `log/slog` en lugar de `zap` o `zerolog`
**Contexto**: Necesitamos logs estructurados, pero sin añadir dependencias.
**Decisión**: `log/slog` de stdlib (Go 1.21+). Soporta JSON y texto con un cambio de handler.
**Consecuencia**: Sin dependencias extra, handler swappable para producción vs debug.

## ADR-004: `getSamplingMarkets()` como endpoint primario
**Contexto**: Polymarket tiene ~20k mercados activos. Filtrar todos para encontrar los de rewards es costoso.
**Decisión**: Usar `/sampling-markets` del CLOB, que devuelve SOLO mercados con rewards activos (~100-200).
**Consecuencia**: Reducimos el ciclo de escaneo a ~5-10 requests en lugar de cientos.

## ADR-005: Batch endpoints para orderbooks
**Contexto**: Un mercado binario tiene 2 tokens. Hacer 1 request por token sería ineficiente.
**Decisión**: Usar `POST /books` con hasta 20 token_ids por request.
**Consecuencia**: ~10 mercados por request → 3-5 requests para 30-50 mercados.

## ADR-006: Frecuencia de escaneo 30s
**Contexto**: El reward sampling de Polymarket es aleatorio cada ~60s.
**Decisión**: Escanear cada 30s para tener visión casi en tiempo real.
**Consecuencia**: ~20 req/min de ~3000 disponibles. Margen enorme para backoff.

## ADR-007: Rate limiter al 60% del límite real
**Contexto**: Los límites de la API pueden variar. Necesitamos margen de seguridad.
**Decisión**: Configurar el token bucket al 60% del límite documentado.
**Consecuencia**: En el peor caso usamos 60% de la cuota, dejando 40% de buffer.
