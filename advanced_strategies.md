# Advanced Strategies — Ventaja competitiva por inteligencia

Estrategias avanzadas que explotan ineficiencias matemáticas y estructurales
de Polymarket que los bots grandes no cubren.

---

## Estrategia 1: Phantom Spread — Arbitraje temporal predictivo

### Concepto
En un mercado binario, la relación `P(YES) + P(NO) = 1` no se cumple
instantáneamente. Hay un lag entre ambos lados cuando entra información nueva.
Este lag crea ventanas donde `sum < 1` (FILLS=PROFIT) que son predecibles.

### Matemática
```
deviation(t) = bestBid_YES(t) + bestBid_NO(t) - 1.0

Si deviation(t) < 0 → oportunidad de compra (FILLS=PROFIT)
Si deviation(t) > 0 → alguien paga de más
```

La distribución de `deviation(t)` NO es aleatoria. Sigue patrones:
- Después de un fill grande en un lado, el otro tarda N segundos en ajustar
- La desviación es mayor en horarios off-peak (3-5 AM UTC)
- La desviación se amplifica justo antes de resolución (liquidity drain)

### Ventaja
No haces arbitraje reactivo (ver gap → comprar). Haces **arbitraje predictivo**:
predices CUÁNDO se abrirá el gap y tienes tu orden lista antes que nadie.

### Implementación
1. Samplear `deviation(t)` cada 5-10 segundos durante días
2. Modelar la autocorrelación temporal (¿un fill grande predice un gap N segundos después?)
3. Identificar patrones de horario (¿a qué hora UTC hay más desviación?)
4. Pre-posicionar órdenes en los momentos predichos

### Requisitos
- Datos de orderbook de alta frecuencia (la API actual da snapshots, necesitaríamos polling agresivo ~5s)
- Almacenar series temporales de deviation(t) por mercado
- Modelo estadístico simple (media móvil + detección de régimen)

### Riesgo
- La API tiene rate limits que limitan la resolución temporal
- Si otros bots detectan el mismo patrón, la ventana se cierra

### Prioridad: MEDIA
Potente pero necesita infraestructura de datos de alta frecuencia.

---

## Estrategia 2: Geometric Reward Maximization — Portfolio óptimo

### Concepto
Todos los bots optimizan mercado por mercado: "¿cuál es el mejor mercado?"
Pero la función de reward es **cóncava** en cada mercado individual, lo que
significa que el óptimo global NO es meter todo en el mejor mercado, sino
**distribuir capital entre mercados con competencia asimétrica**.

### Matemática
Tu reward en un mercado:
```
R(s, C) = dailyRate × (s / (s + C)) × spreadScore

donde:
  s = tu order size
  C = competencia (USDC de otros en el book)
```

La derivada parcial respecto a `s`:
```
dR/ds = dailyRate × C / (s + C)² × spreadScore
```

Es decreciente → cada dólar extra rinde menos en el mismo mercado.

**El insight clave**: para N mercados, quieres maximizar:
```
max Σᵢ Rᵢ(sᵢ, Cᵢ)
s.t. Σᵢ sᵢ ≤ S_total  (capital total disponible)
     sᵢ ≥ 0
```

Esto es un problema de optimización convexa. La solución analítica
(condiciones KKT) dice:

```
sᵢ* = √(dailyRateᵢ × Cᵢ / λ) - Cᵢ

donde λ es el multiplicador de Lagrange (se ajusta para que Σsᵢ = S_total)
```

### Ejemplo numérico
Capital: $200

**Sin optimizar** (todo al mejor mercado):
```
Mercado A: rate=$10/d, competencia=$5000
→ R = 10 × (200/5200) = $0.38/día
```

**Optimizado** (distribuido):
```
Mercado A: rate=$10/d, competencia=$5000, poner $50
→ R = 10 × (50/5050) = $0.099/día

Mercado B: rate=$5/d, competencia=$200, poner $75
→ R = 5 × (75/275) = $1.36/día

Mercado C: rate=$3/d, competencia=$100, poner $75
→ R = 3 × (75/175) = $1.29/día

Total = $2.75/día vs $0.38/día → 7x mejor
```

### Ventaja
- No requiere velocidad ni infraestructura especial
- Explotable AHORA con los datos que ya tenemos
- Los bots grandes ignoran mercados pequeños (bajo volumen)
- Es matemáticamente óptimo — no depende de timing ni suerte

### Implementación
1. Para cada mercado FILLS=PROFIT, obtener: dailyRate, competition, spreadScore
2. Resolver el problema de optimización (iteración simple o scipy-style)
3. Distribuir capital según solución óptima
4. Re-optimizar cada ciclo (la competencia cambia)

### Requisitos
- Los datos ya los tenemos: dailyRate, competition están en cada Opportunity
- Solver simple: iteración de bisección sobre λ
- Capital total como parámetro de config

### Riesgo
- Los mercados pequeños pueden tener menos liquidez → fills más lentos
- Si el mercado se resuelve rápido, el capital se queda bloqueado
- El modelo asume que la competencia no reacciona a tu entrada

### Prioridad: ALTA
Implementable hoy. Máximo impacto con mínimo riesgo.

---

## Estrategia 3: Grafo de Implicación — Arbitraje lógico-estructural

### Concepto
Polymarket tiene mercados correlacionados que la mayoría trata como
independientes. Pero entre ellos existen **restricciones lógicas** que
deben cumplirse siempre. Cuando no se cumplen, hay un arbitraje
matemáticamente garantizado.

### Matemática
Tipos de restricciones lógicas entre mercados:

**Implicación**: Si A implica B, entonces P(A) ≤ P(B)
```
"Bitcoin > $100k en marzo" → "Bitcoin > $90k en marzo"
Si P(BTC>100k) > P(BTC>90k) → ARBITRAJE
```

**Exclusión mutua**: Si A y B no pueden ser verdad a la vez, P(A) + P(B) ≤ 1
```
"Partido A gana" y "Partido B gana" (mismo cargo)
Si P(A) + P(B) > 1 → ARBITRAJE
```

**Partición**: Si {A₁, A₂, ..., Aₙ} cubren todos los resultados, Σ P(Aᵢ) = 1
```
"¿Quién gana la Euro?" — España, Francia, Alemania, ...
Si Σ P(equipoᵢ) ≠ 1 → ARBITRAJE
```

**Desigualdad condicional**: P(A∩B) ≤ min(P(A), P(B))
```
"Trump gana Y Senado republicano"
No puede ser más probable que "Trump gana" solo
```

### Modelo de grafo
```
Nodos = mercados
Aristas = restricciones lógicas (tipo + dirección)

Para cada ciclo lógico en el grafo:
  Verificar que la restricción se cumple con los precios actuales
  Si no → calcular el trade que explota la inconsistencia
```

### Ventaja
- **No depende de velocidad**: la inconsistencia es estructural, no temporal
- **No depende de cola**: operas en mercados DIFERENTES, no compites en el mismo book
- **Matemáticamente garantizado**: si la restricción lógica se viola, ganas dinero
- **Invisible para otros bots**: requiere entender la SEMÁNTICA de los mercados,
  no solo los números

### Implementación
1. Crawlear todos los mercados activos y sus "questions"
2. Usar NLP (o LLM) para detectar relaciones lógicas entre preguntas
3. Construir el grafo de restricciones
4. En cada ciclo, verificar todas las restricciones contra precios actuales
5. Cuando hay violación: calcular el trade óptimo y ejecutar

### Ejemplo real potencial
```
Mercado 1: "Will BTC hit $120k by June 2026?" → YES @ $0.35
Mercado 2: "Will BTC hit $100k by June 2026?" → YES @ $0.30

Restricción: P(BTC>120k) ≤ P(BTC>100k)  (si llega a 120, ya pasó 100)
Violación: 0.35 > 0.30

Trade: BUY "BTC>100k" YES @ $0.30, SELL "BTC>120k" YES @ $0.35
Ganancia garantizada: $0.05 por share, sin importar el resultado
```

### Requisitos
- Capacidad de parsear y relacionar preguntas de mercados (NLP/LLM)
- Base de datos de restricciones (grafo)
- Acceso a precios en tiempo real de múltiples mercados simultáneamente
- Capacidad de operar en múltiples mercados a la vez

### Riesgo
- Detección de relaciones puede tener falsos positivos
- Liquidez para ejecutar ambos lados del trade
- Si la relación es incorrecta (entendiste mal la pregunta), pierdes

### Prioridad: ALTA (pero compleja)
Es la estrategia más potente de las tres. No depende de competir contra
otros bots en velocidad o capital. Depende de VER lo que otros no ven.

---

## Estrategias tácticas adicionales

### 4. Timing de mercados nuevos
Detectar mercados con < 24h de vida donde la cola está vacía.
Prioridad: ALTA. Simple de implementar.

### 5. Horarios off-peak
Colocar/reposicionar órdenes entre 3-5 AM UTC.
Menos competencia = mejor posición en la cola.
Prioridad: MEDIA. Requiere scheduling.

### 6. Rotación dinámica
Salir de mercados donde queueAhead creció >3x desde entrada.
Reubicar capital en mercados menos competidos.
Prioridad: ALTA. Ya tenemos queue tracking.

### 7. Cancelación selectiva de parciales
Si un lado se llena y el otro no en >30min, cancelar el lado abierto
y reponerlo a mejor precio para completar el par rápido.
Prioridad: ALTA para operativa real.

### 8. Tamaño de orden adaptativo
En vez de $100 fijo, calcular el tamaño que maximiza R(s)/s
(reward por dólar invertido) en cada mercado.
Prioridad: MEDIA. Extensión natural de la Estrategia 2.

---

---

## Estrategias de nivel 2 — Minimización de riesgo avanzada

### 9. Kelly Criterion para sizing de posiciones

El Kelly Criterion es la fórmula matemáticamente probada para maximizar
el crecimiento de capital a largo plazo. En vez de poner $100 fijo en
cada mercado, calculas el tamaño óptimo.

```
f* = (p × b - q) / b

donde:
  f* = fracción del capital a apostar
  p  = probabilidad de ganar (estimada por tu modelo)
  q  = 1 - p
  b  = ratio de payout (cuánto ganas por dólar arriesgado)
```

En nuestro caso de reward farming con FILLS=PROFIT:
```
p = probabilidad de que el par se complete (ambos fills)
b = (reward_acumulado + fill_profit) / capital_desplegado
q = probabilidad de quedarte con parcial × pérdida esperada del parcial

f* = fracción óptima de tu capital total para este mercado
```

**Ventaja**: Kelly NUNCA te lleva a la ruina (f* siempre es < 100%).
La mayoría de traders usan "half-Kelly" (f*/2) para ser más conservadores.

**Aplicación práctica**: el paper trading con queue-adjusted fills
te da los datos para calcular p (fill rate real). Después de 7 días,
puedes calcular f* por mercado y comparar con el $100 fijo actual.

### 10. Modelo Avellaneda-Stoikov para gestión de inventario

Los market makers profesionales no usan precios fijos. Ajustan
dinámicamente sus bids según su inventario actual.

```
bid_optimo = midprice - spread/2 - γ × σ² × T × q

donde:
  γ = aversión al riesgo
  σ = volatilidad del mercado
  T = tiempo hasta resolución
  q = inventario actual (positivo = tienes YES, negativo = tienes NO)
```

Traducido a tu caso:
- Si tu YES se llenó pero tu NO no (q > 0): **baja tu bid NO**
  (ofrece mejor precio) para atraer un fill rápido y completar el par
- Si NO se llenó pero YES no (q < 0): **baja tu bid YES**
- Si no tienes inventario (q = 0): precio simétrico

**Por qué importa**: reduce el tiempo de exposición parcial.
En vez de esperar pasivamente a que el otro lado se llene,
ajustas el precio activamente para forzar la completación.

### 11. Hedging cross-market para parciales

Si tienes un fill parcial en el mercado A (solo YES comprado) y tarda
en completarse, puedes HEDGE comprando NO en un mercado B correlacionado.

```
Mercado A: "Trump gana elección 2028" — tienes YES @ $0.40
Mercado B: "Republicano gana 2028" — compras NO @ $0.55

Si Trump pierde:
  - Pierdes $0.40 en A (YES vale $0)
  - Ganas $0.45 en B (NO vale $1, pagaste $0.55)
  - Pérdida neta: $0.40 - $0.45 = +$0.05 (ganancia!)

Si Trump gana:
  - Ganas $0.60 en A (YES vale $1, pagaste $0.40)
  - Pierdes $0.55 en B (NO vale $0)
  - Ganancia neta: $0.60 - $0.55 = +$0.05
```

**Resultado**: ganancia GARANTIZADA si encuentras el par correcto.
Es el grafo de implicación (Estrategia 3) aplicado a defensa.

### 12. Diversificación por no-correlación

Estar en 3 mercados de crypto no es diversificación — si BTC cae,
todos caen juntos. Verdadera diversificación:

```
Correlación ≈ 0 entre:
  - Mercado crypto + mercado político + mercado deportivo
  - Mercados con resolución en fechas diferentes
  - Mercados de geopolítica en regiones diferentes
```

**Medible**: calcular correlación de movimientos de precio entre
mercados usando datos históricos de trades.

Si la correlación entre tus 3 mercados es 0.8, tu riesgo real es
como si estuvieras en 1.5 mercados. Si la correlación es 0.1,
tu riesgo es como estar en 2.7 mercados independientes.

```
riesgo_efectivo = √(Σᵢⱼ wᵢ × wⱼ × σᵢ × σⱼ × ρᵢⱼ)

donde ρᵢⱼ es la correlación entre mercados i y j
```

---

## Estrategias de nivel 3 — Creatividad pura

### 13. Dead market arbitrage

Mercados donde el evento YA OCURRIÓ pero el mercado no se resolvió
formalmente. El precio debería ser $0.99/$0.01 pero si está a $0.85/$0.15,
hay dinero gratis esperando resolución.

**Detección**: comparar el "question" del mercado con noticias recientes.
Un LLM puede hacer esto automáticamente.

### 14. Volatility harvesting — el mercado como oscilador

Algunos mercados oscilan: un día YES sube, al siguiente baja, sin
tendencia clara (ej: "Will BTC be above $95k on March 1?").

En estos mercados, puedes colocar bids en AMBOS extremos del rango
de oscilación. Cuando el precio oscila de $0.45 a $0.55 y vuelve:
```
Compras YES a $0.45 (bid bajo)
Vendes YES a $0.55 (ask alto)
Ganancia: $0.10 por share por oscilación
```

**Detección**: calcular la desviación estándar del precio y el ratio
de mean-reversion (Hurst exponent). Si H < 0.5, el mercado revierte
→ volatility harvesting funciona. Si H > 0.5, tiende → no funciona.

### 15. Information flow detection

Los trades grandes (>$500) suelen ser de traders informados.
Los trades pequeños (<$10) suelen ser ruido.

```
trade_signal = Σ(sign(trade) × log(trade_size)) / √(N)

Si trade_signal > threshold → dinero informado entrando
→ el precio va a moverse → posiciónate ANTES
```

Esto es el modelo PIN (Probability of Informed Trading) adaptado
a prediction markets. Te permite detectar cuándo alguien que SABE
algo está operando, antes de que el precio refleje esa información.

### 16. Liquidation cascades near resolution

Cuando un mercado tiene < 48h y el precio se mueve bruscamente,
los makers retiran liquidez (panic). Esto crea un feedback loop:
```
precio se mueve → makers retiran → menos liquidez → precio se mueve más
```

Puedes ANTICIPAR esta cascada y:
1. Retirar tus órdenes ANTES que los demás (menos exposición)
2. O APROVECHAR el spread amplio que deja la retirada masiva

---

## Sobre migrar a Rust

**Respuesta corta: NO.**

El cuello de botella es la API (200ms por request, 60s entre ciclos).
Go procesa el análisis en ~1ms. Rust lo haría en ~0.1ms.
Esos 0.9ms no cambian NADA.

Rust tendría sentido si:
- Polymarket ofreciera un websocket de baja latencia (no lo tiene)
- Compitieras en microsegundos contra HFT (no es el caso)
- El análisis fuera computacionalmente pesado (no lo es)

Go es la elección correcta: buena concurrencia, compilación rápida,
deploy simple, y más que suficiente rendimiento para este caso.

**Lo que SÍ daría ventaja técnica** (sin cambiar de lenguaje):
- WebSocket para orderbook updates en tiempo real (si Polymarket lo ofrece)
- Colocar el bot en un servidor en NYC (cerca de los servidores de Polymarket)
- Múltiples instancias monitoreando diferentes subsets de mercados

---

## Roadmap de implementación sugerido

1. **Ahora**: Estrategia 2 (portfolio óptimo) — máximo impacto, datos disponibles
2. **Semana 1**: Estrategia 4+6 (timing nuevos + rotación) — táctico
3. **Semana 1**: Estrategia 9 (Kelly sizing) — con datos del paper trading
4. **Semana 2**: Estrategia 3 (grafo lógico) — ventaja estructural
5. **Semana 2**: Estrategia 12 (diversificación no correlacionada)
6. **Semana 3**: Estrategia 10 (Avellaneda-Stoikov) — para operativa real
7. **Semana 3**: Estrategia 1 (phantom spread) — si los datos lo permiten
8. **Mes 2**: Estrategia 13+15 (dead markets + information flow) — edge avanzado
