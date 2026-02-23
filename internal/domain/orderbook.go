package domain

import "strconv"

// OrderBook representa el libro de órdenes de un token.
type OrderBook struct {
	TokenID string
	Bids    []BookEntry // ordenados mayor a menor precio
	Asks    []BookEntry // ordenados menor a mayor precio
}

// BookEntry es un nivel de precio en el orderbook.
type BookEntry struct {
	Price float64
	Size  float64
}

// BestBid devuelve el mejor precio de compra (mayor bid).
// Devuelve 0 si el book está vacío.
func (ob OrderBook) BestBid() float64 {
	if len(ob.Bids) == 0 {
		return 0
	}
	return ob.Bids[0].Price
}

// BestAsk devuelve el mejor precio de venta (menor ask).
// Devuelve 0 si el book está vacío.
func (ob OrderBook) BestAsk() float64 {
	if len(ob.Asks) == 0 {
		return 0
	}
	return ob.Asks[0].Price
}

// Midpoint devuelve el punto medio entre best bid y best ask.
func (ob OrderBook) Midpoint() float64 {
	bid := ob.BestBid()
	ask := ob.BestAsk()
	if bid == 0 || ask == 0 {
		return 0
	}
	return (bid + ask) / 2
}

// Spread devuelve el spread del book (ask - bid).
func (ob OrderBook) Spread() float64 {
	bid := ob.BestBid()
	ask := ob.BestAsk()
	if bid == 0 || ask == 0 {
		return 0
	}
	return ask - bid
}

// DepthWithin calcula el volumen total de órdenes (bids + asks) en unidades de token
// dentro de un spread dado respecto al midpoint.
func (ob OrderBook) DepthWithin(maxSpread float64) float64 {
	mid := ob.Midpoint()
	if mid == 0 {
		return 0
	}
	var total float64
	for _, b := range ob.Bids {
		if mid-b.Price <= maxSpread {
			total += b.Size
		}
	}
	for _, a := range ob.Asks {
		if a.Price-mid <= maxSpread {
			total += a.Size
		}
	}
	return total
}

// DepthWithinUSDC calcula el valor en USDC (size × price) de las órdenes
// dentro de un spread dado respecto al midpoint.
// Usar este método para calcular competencia en términos monetarios reales.
func (ob OrderBook) DepthWithinUSDC(maxSpread float64) float64 {
	mid := ob.Midpoint()
	if mid == 0 {
		return 0
	}
	var total float64
	for _, b := range ob.Bids {
		if mid-b.Price <= maxSpread {
			total += b.Size * b.Price
		}
	}
	for _, a := range ob.Asks {
		if a.Price-mid <= maxSpread {
			total += a.Size * a.Price
		}
	}
	return total
}

// BidDepthWithinUSDC calculates the USDC value (size × price) of BID orders only
// within a given spread relative to the midpoint.
// Use this for competition in reward farming: only bids compete for liquidity rewards.
func (ob OrderBook) BidDepthWithinUSDC(maxSpread float64) float64 {
	mid := ob.Midpoint()
	if mid == 0 {
		return 0
	}
	var total float64
	for _, b := range ob.Bids {
		if mid-b.Price <= maxSpread {
			total += b.Size * b.Price
		}
	}
	return total
}

// ParsePrice convierte un string de precio a float64.
// Usado en el mapping de la API.
func ParsePrice(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return v
}
