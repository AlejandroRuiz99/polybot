package polymarket

// trading.go — Real order execution via Polymarket CLOB API.
//
// Implements ports.OrderExecutor using AuthClient for L1/L2 auth.
// All maker orders are placed as GTC (good-till-cancelled) limit bids.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/alejandrodnm/polybot/internal/domain"
)

// clobOrderRequest is the JSON body sent to POST /order.
type clobOrderRequest struct {
	Order     clobOrderBody `json:"order"`
	Owner     string        `json:"owner"`
	OrderType string        `json:"orderType"`
}

type clobOrderBody struct {
	Salt          json.Number `json:"salt"`
	Maker         string      `json:"maker"`
	Signer        string      `json:"signer"`
	Taker         string      `json:"taker"`
	TokenID       string      `json:"tokenId"`
	MakerAmount   string      `json:"makerAmount"`
	TakerAmount   string      `json:"takerAmount"`
	Expiration    string      `json:"expiration"`
	Nonce         string      `json:"nonce"`
	FeeRateBps    string      `json:"feeRateBps"`
	Side          string      `json:"side"`
	SignatureType int         `json:"signatureType"`
	Signature     string      `json:"signature"`
}

type clobOrderResponse struct {
	ErrorMsg       string `json:"errorMsg"`
	OrderID        string `json:"orderID"`
	TakingAmount   string `json:"takingAmount"`
	MakingAmount   string `json:"makingAmount"`
	Status         string `json:"status"`
	Success        bool   `json:"success"`
}

type clobOpenOrder struct {
	ID           string `json:"id"`
	AssetID      string `json:"asset_id"`
	Market       string `json:"market"`
	Side         string `json:"side"`
	OriginalSize string `json:"original_size"`
	SizeMatched  string `json:"size_matched"`
	Price        string `json:"price"`
	Status       string `json:"status"`
	CreatedAt    string `json:"created_at"`
	Outcome      string `json:"outcome"`
}

type clobOrdersResponse struct {
	Data       []clobOpenOrder `json:"data"`
	NextCursor string          `json:"next_cursor"`
}

type clobBalanceResponse struct {
	Balance string `json:"balance"`
}

type clobNegRiskResponse struct {
	NegRisk bool `json:"neg_risk"`
}

const (
	usdcEAddress = "0x2791Bca1f2de4661ED88A30C99A7a9449Aa84174"
	ctfAddress   = "0x4D97DCd97eC945f40cF65F87097ACe5EA0476045"
)

var (
	balanceOfABI    abi.ABI
	balanceOfERC1155 abi.ABI
)

func init() {
	var err error
	balanceOfABI, err = abi.JSON(strings.NewReader(`[{
		"name":"balanceOf","type":"function",
		"inputs":[{"name":"account","type":"address"}],
		"outputs":[{"name":"","type":"uint256"}]
	}]`))
	if err != nil {
		panic("balanceOf abi: " + err.Error())
	}
	balanceOfERC1155, err = abi.JSON(strings.NewReader(`[{
		"name":"balanceOf","type":"function",
		"inputs":[{"name":"account","type":"address"},{"name":"id","type":"uint256"}],
		"outputs":[{"name":"","type":"uint256"}]
	}]`))
	if err != nil {
		panic("balanceOf erc1155 abi: " + err.Error())
	}
}

// TradingClient implements ports.OrderExecutor.
type TradingClient struct {
	auth      *AuthClient
	rpcClient *ethclient.Client
}

// NewTradingClient creates a TradingClient. rpcURL is used for on-chain balance checks.
func NewTradingClient(auth *AuthClient, rpcURL string) (*TradingClient, error) {
	rpc, err := ethclient.Dial(rpcURL)
	if err != nil {
		return nil, fmt.Errorf("trading: dial rpc: %w", err)
	}
	return &TradingClient{auth: auth, rpcClient: rpc}, nil
}

// PlaceOrder signs and submits a BUY maker limit order to the CLOB.
func (tc *TradingClient) PlaceOrder(ctx context.Context, req domain.PlaceOrderRequest) (domain.PlacedOrder, error) {
	if err := tc.auth.EnsureCreds(ctx); err != nil {
		return domain.PlacedOrder{}, fmt.Errorf("place order: creds: %w", err)
	}

	signed, err := tc.auth.buildSignedOrder(req.TokenID, req.Price, req.Size, req.NegRisk)
	if err != nil {
		return domain.PlacedOrder{}, fmt.Errorf("place order: sign: %w", err)
	}

	sideStr := "BUY"
	body := clobOrderRequest{
		Order: clobOrderBody{
			Salt:          json.Number(signed.Order.Salt.String()),
			Maker:         signed.Order.Maker.Hex(),
			Signer:        signed.Order.Signer.Hex(),
			Taker:         signed.Order.Taker.Hex(),
			TokenID:       req.TokenID,
			MakerAmount:   signed.Order.MakerAmount.String(),
			TakerAmount:   signed.Order.TakerAmount.String(),
			Expiration:    signed.Order.Expiration.String(),
			Nonce:         signed.Order.Nonce.String(),
			FeeRateBps:    signed.Order.FeeRateBps.String(),
			Side:          sideStr,
			SignatureType: int(signed.Order.SignatureType.Int64()),
			Signature:     "0x" + hex.EncodeToString(signed.Signature),
		},
		Owner:     tc.auth.creds.APIKey,
		OrderType: "GTC",
	}

	var resp clobOrderResponse
	if err := tc.auth.doL2(ctx, http.MethodPost, "/order", body, &resp); err != nil {
		return domain.PlacedOrder{}, fmt.Errorf("place order: post: %w", err)
	}

	if !resp.Success || resp.ErrorMsg != "" {
		return domain.PlacedOrder{}, fmt.Errorf("place order: clob error: %s", resp.ErrorMsg)
	}

	takenAmt := parseUSDC(resp.TakingAmount)
	madeAmt := parseUSDC(resp.MakingAmount)

	return domain.PlacedOrder{
		CLOBOrderID: resp.OrderID,
		Status:      resp.Status,
		TakenAmount: takenAmt,
		MadeAmount:  madeAmt,
	}, nil
}

// CancelOrder cancels a single order by its CLOB order ID.
func (tc *TradingClient) CancelOrder(ctx context.Context, clobOrderID string) error {
	if err := tc.auth.EnsureCreds(ctx); err != nil {
		return fmt.Errorf("cancel order: creds: %w", err)
	}

	path := "/order/" + clobOrderID
	if err := tc.auth.doL2(ctx, http.MethodDelete, path, nil, nil); err != nil {
		return fmt.Errorf("cancel order %s: %w", clobOrderID, err)
	}
	return nil
}

// CancelAll cancels all open orders for this wallet.
func (tc *TradingClient) CancelAll(ctx context.Context) error {
	if err := tc.auth.EnsureCreds(ctx); err != nil {
		return fmt.Errorf("cancel all: creds: %w", err)
	}

	if err := tc.auth.doL2(ctx, http.MethodDelete, "/orders", nil, nil); err != nil {
		return fmt.Errorf("cancel all: %w", err)
	}
	return nil
}

// GetOpenOrders returns all currently open orders from the CLOB.
func (tc *TradingClient) GetOpenOrders(ctx context.Context) ([]domain.LiveOrder, error) {
	if err := tc.auth.EnsureCreds(ctx); err != nil {
		return nil, fmt.Errorf("get orders: creds: %w", err)
	}

	var resp clobOrdersResponse
	if err := tc.auth.doL2(ctx, http.MethodGet, "/orders", nil, &resp); err != nil {
		return nil, fmt.Errorf("get orders: %w", err)
	}

	orders := make([]domain.LiveOrder, 0, len(resp.Data))
	for _, o := range resp.Data {
		lo := clobOpenOrderToLiveOrder(o)
		orders = append(orders, lo)
	}
	return orders, nil
}

// GetBalance returns the on-chain USDC.e balance of the funder address.
func (tc *TradingClient) GetBalance(ctx context.Context) (float64, error) {
	callData, err := balanceOfABI.Pack("balanceOf", tc.auth.address)
	if err != nil {
		return 0, fmt.Errorf("get balance: pack: %w", err)
	}

	token := common.HexToAddress(usdcEAddress)
	result, err := tc.rpcClient.CallContract(ctx, ethereum.CallMsg{
		To:   &token,
		Data: callData,
	}, nil)
	if err != nil {
		return 0, fmt.Errorf("get balance: rpc call: %w", err)
	}

	vals, err := balanceOfABI.Unpack("balanceOf", result)
	if err != nil || len(vals) == 0 {
		return 0, fmt.Errorf("get balance: unpack: %w", err)
	}

	raw := vals[0].(*big.Int)
	bal, _ := new(big.Float).Quo(new(big.Float).SetInt(raw), new(big.Float).SetFloat64(1e6)).Float64()
	return bal, nil
}

// IsNegRisk queries the CLOB to determine if a token uses the NegRisk adapter.
func (tc *TradingClient) IsNegRisk(ctx context.Context, tokenID string) (bool, error) {
	url := fmt.Sprintf("%s/neg-risk?token_id=%s", tc.auth.clobBase, tokenID)

	var resp clobNegRiskResponse
	if err := tc.auth.get(ctx, tc.auth.clobLimiter, url, &resp); err != nil {
		return false, fmt.Errorf("neg-risk check: %w", err)
	}
	return resp.NegRisk, nil
}

// TokenBalance returns the on-chain ERC-1155 balance for a conditional token.
// Returns shares (not micro-units) — e.g. 13.51 means 13.51 shares.
func (tc *TradingClient) TokenBalance(ctx context.Context, tokenID string) (float64, error) {
	tid := new(big.Int)
	if _, ok := tid.SetString(tokenID, 10); !ok {
		tidBytes, err := hex.DecodeString(strings.TrimPrefix(tokenID, "0x"))
		if err != nil {
			return 0, fmt.Errorf("token balance: invalid token ID: %s", tokenID)
		}
		tid.SetBytes(tidBytes)
	}

	callData, err := balanceOfERC1155.Pack("balanceOf", tc.auth.address, tid)
	if err != nil {
		return 0, fmt.Errorf("token balance: pack: %w", err)
	}

	ctf := common.HexToAddress(ctfAddress)
	result, err := tc.rpcClient.CallContract(ctx, ethereum.CallMsg{
		To:   &ctf,
		Data: callData,
	}, nil)
	if err != nil {
		return 0, fmt.Errorf("token balance: call: %w", err)
	}

	vals, err := balanceOfERC1155.Unpack("balanceOf", result)
	if err != nil || len(vals) == 0 {
		return 0, fmt.Errorf("token balance: unpack: %w", err)
	}

	raw := vals[0].(*big.Int)
	shares := new(big.Float).SetInt(raw)
	shares.Quo(shares, big.NewFloat(1e6))
	f, _ := shares.Float64()
	return f, nil
}

// clobOpenOrderToLiveOrder converts a CLOB API order to our domain type.
func clobOpenOrderToLiveOrder(o clobOpenOrder) domain.LiveOrder {
	size := parseUSDCStr(o.OriginalSize)
	filled := parseUSDCStr(o.SizeMatched)
	price := parseFloat(o.Price)

	status := domain.LiveStatusOpen
	upper := strings.ToUpper(o.Status)
	switch {
	case strings.Contains(upper, "MATCHED"):
		status = domain.LiveStatusFilled
	case strings.Contains(upper, "CANCEL") || strings.Contains(upper, "INVALID"):
		status = domain.LiveStatusCancelled
	}

	side := "YES"
	if strings.ToLower(o.Outcome) == "no" {
		side = "NO"
	}

	return domain.LiveOrder{
		CLOBOrderID: o.ID,
		ConditionID: o.Market,
		TokenID:     o.AssetID,
		Side:        side,
		BidPrice:    price,
		Size:        size,
		FilledSize:  filled,
		Status:      status,
		PlacedAt:    parseTimestamp(o.CreatedAt),
	}
}

// parseUSDC converts a micro-USDC string (e.g., "1000000") to USDC float.
func parseUSDC(s string) float64 {
	if s == "" {
		return 0
	}
	n := new(big.Int)
	n.SetString(s, 10)
	f, _ := new(big.Float).SetInt(n).Float64()
	return f / 1_000_000
}

func parseUSDCStr(s string) float64 {
	return parseUSDC(s)
}

func parseFloat(s string) float64 {
	if s == "" {
		return 0
	}
	var f float64
	fmt.Sscanf(s, "%f", &f)
	return f
}

func parseTimestamp(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	// Try parsing as int64 Unix timestamp
	var ts int64
	if _, err := fmt.Sscanf(s, "%d", &ts); err == nil && ts > 0 {
		if ts > 1e12 {
			return time.UnixMilli(ts).UTC()
		}
		return time.Unix(ts, 0).UTC()
	}
	// ISO 8601
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// MarshalJSON for clobOrderBody to support dynamic serialization in tests.
func (b clobOrderBody) MarshalJSON() ([]byte, error) {
	type Alias clobOrderBody
	return json.Marshal(Alias(b))
}
