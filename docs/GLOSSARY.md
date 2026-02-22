# Glosario

## Términos de Polymarket

| Término | Definición |
|---------|------------|
| **Market** | Mercado de predicción binario (YES/NO) |
| **Token** | Contrato ERC-1155 que representa YES o NO de un mercado |
| **token_id** | ID único del token en el CLOB |
| **CLOB** | Central Limit Order Book — motor de matching de Polymarket |
| **Gamma** | API de Polymarket para metadata de mercados (nombre, slug, etc.) |
| **Sampling market** | Mercado actualmente seleccionado para recibir rewards de liquidez |
| **Reward rate** | USDC/día distribuidos entre LPs de un mercado |
| **max_spread** | Spread máximo que califica para recibir rewards |
| **LP** | Liquidity Provider — quien provee órdenes en el book |

## Términos del Scanner

| Término | Definición |
|---------|------------|
| **spread_total** | `best_ask_YES + best_ask_NO - 1.0` (si < 0 hay arbitraje) |
| **midpoint** | Centro del book: `(best_bid + best_ask) / 2` |
| **estimated_score** | `S = ((v-s)/v)² × b` — score estimado de nuestras órdenes |
| **competition** | Profundidad del book dentro del max_spread (cuántos LPs compiten) |
| **net_profit_est** | `reward_estimate - fees - spread_cost` |
| **opportunity** | Mercado que pasa todos los filtros y tiene score positivo |
| **scan cycle** | Una iteración completa del scanner (fetch→analyze→filter→notify) |
| **dry-run** | Modo que usa fixtures locales en lugar de la API real |

## Fórmula de Scoring

```
S = ((v - s) / v)² × b

v = tamaño de nuestras órdenes hipotéticas (USDC)
s = spread_total del mercado
b = reward_rate diario (USDC/día)
```

Un score alto indica: spread pequeño (fácil de calificar) + reward alto (vale la pena).
