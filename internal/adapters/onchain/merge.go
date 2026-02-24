package onchain

// merge.go — On-chain CTF merge executor for Polymarket.
//
// The CTF (Conditional Token Framework) mergePositions() function converts
// YES+NO token pairs back into USDC.e collateral:
//   100 YES tokens + 100 NO tokens → $100 USDC.e
//
// This file handles:
//   - Dynamic gas estimation
//   - ERC1155 approval checks/setup
//   - Atomic on-chain merge transactions

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/alejandrodnm/polybot/internal/domain"
)

const (
	polygonChainID = int64(137)

	// USDC.e collateral on Polygon
	usdcEAddress = "0x2791Bca1f2de4661ED88A30C99A7a9449Aa84174"

	// CTF contract — holds conditional tokens (ERC1155)
	ctfAddress = "0x4D97DCd97eC945f40cF65F87097ACe5EA0476045"

	// Exchange contracts that need ERC1155 setApprovalForAll
	normalExchange  = "0x4bFb41d5B3570DeFd03C39a9A4D8dE6Bd8B8982E"
	negRiskExchange = "0xC5d563A36AE78145C45a50134d48A1215220f80a"
	negRiskAdapter  = "0xd91E80cF2E7be2e162c6513ceD06f1dD0dA35296"

	// Gas limits (conservative upper bounds)
	mergeGasLimit    = uint64(200_000)
	approvalGasLimit = uint64(80_000)

	// POL price fallback (USD) — used when no oracle available
	polPriceFallbackUSD = 0.12

	// Gas price update interval
	gasPriceUpdateInterval = 5 * time.Minute
)

// Contract ABIs
var (
	ctfABI     abi.ABI
	erc1155ABI abi.ABI
	erc20ABI   abi.ABI
)

func init() {
	var err error

	ctfABI, err = abi.JSON(strings.NewReader(`[
		{
			"name": "mergePositions",
			"type": "function",
			"inputs": [
				{"name": "collateralToken", "type": "address"},
				{"name": "parentCollectionId", "type": "bytes32"},
				{"name": "conditionId", "type": "bytes32"},
				{"name": "partition", "type": "uint256[]"},
				{"name": "amount", "type": "uint256"}
			],
			"outputs": []
		}
	]`))
	if err != nil {
		panic("ctf abi parse: " + err.Error())
	}

	erc1155ABI, err = abi.JSON(strings.NewReader(`[
		{
			"name": "setApprovalForAll",
			"type": "function",
			"inputs": [
				{"name": "operator", "type": "address"},
				{"name": "approved", "type": "bool"}
			],
			"outputs": []
		},
		{
			"name": "isApprovedForAll",
			"type": "function",
			"inputs": [
				{"name": "account", "type": "address"},
				{"name": "operator", "type": "address"}
			],
			"outputs": [{"name": "", "type": "bool"}]
		}
	]`))
	if err != nil {
		panic("erc1155 abi parse: " + err.Error())
	}

	erc20ABI, err = abi.JSON(strings.NewReader(`[
		{
			"name": "approve",
			"type": "function",
			"inputs": [
				{"name": "spender", "type": "address"},
				{"name": "amount", "type": "uint256"}
			],
			"outputs": [{"name": "", "type": "bool"}]
		},
		{
			"name": "allowance",
			"type": "function",
			"inputs": [
				{"name": "owner", "type": "address"},
				{"name": "spender", "type": "address"}
			],
			"outputs": [{"name": "", "type": "uint256"}]
		}
	]`))
	if err != nil {
		panic("erc20 abi parse: " + err.Error())
	}
}

// MergeClient implements ports.MergeExecutor.
type MergeClient struct {
	client     *ethclient.Client
	privateKey []byte
	address    common.Address
	rpcURL     string
	httpClient *http.Client

	mu             sync.RWMutex
	cachedGasWei   *big.Int
	gasUpdatedAt   time.Time
	cachedPOLPrice float64
	polPriceAt     time.Time
}

// NewMergeClient creates a merge executor connected to the given Polygon RPC.
// privateKeyHex is without 0x prefix.
func NewMergeClient(rpcURL, privateKeyHex string) (*MergeClient, error) {
	pkBytes, err := hex.DecodeString(strings.TrimPrefix(privateKeyHex, "0x"))
	if err != nil {
		return nil, fmt.Errorf("merge: decode private key: %w", err)
	}

	privKey, err := crypto.ToECDSA(pkBytes)
	if err != nil {
		return nil, fmt.Errorf("merge: invalid private key: %w", err)
	}

	addr := crypto.PubkeyToAddress(privKey.PublicKey)

	client, err := ethclient.Dial(rpcURL)
	if err != nil {
		return nil, fmt.Errorf("merge: dial rpc %s: %w", rpcURL, err)
	}

	return &MergeClient{
		client:     client,
		privateKey: pkBytes,
		address:    addr,
		rpcURL:     rpcURL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}, nil
}

// EstimateGasCostUSD returns the estimated gas cost in USD for a merge transaction.
func (mc *MergeClient) EstimateGasCostUSD(ctx context.Context) (float64, error) {
	gasPrice, err := mc.getGasPrice(ctx)
	if err != nil {
		return mc.polPriceUSD() * float64(mergeGasLimit) * 100e-9, nil
	}

	gasCostPOL := new(big.Float).SetInt(new(big.Int).Mul(gasPrice, big.NewInt(int64(mergeGasLimit))))
	gasCostPOL.Quo(gasCostPOL, new(big.Float).SetFloat64(1e18))

	gasCostPOLf, _ := gasCostPOL.Float64()
	return gasCostPOLf * mc.polPriceUSD(), nil
}

// polPriceUSD returns the cached POL price, refreshing from CoinGecko if stale.
func (mc *MergeClient) polPriceUSD() float64 {
	mc.mu.RLock()
	price := mc.cachedPOLPrice
	updatedAt := mc.polPriceAt
	mc.mu.RUnlock()

	if price > 0 && time.Since(updatedAt) < 15*time.Minute {
		return price
	}

	fetched, err := mc.fetchPOLPrice()
	if err != nil {
		slog.Warn("merge: failed to fetch POL price, using fallback", "err", err)
		if price > 0 {
			return price
		}
		return polPriceFallbackUSD
	}

	mc.mu.Lock()
	mc.cachedPOLPrice = fetched
	mc.polPriceAt = time.Now()
	mc.mu.Unlock()

	return fetched
}

// fetchPOLPrice queries CoinGecko for the current POL/USD price.
func (mc *MergeClient) fetchPOLPrice() (float64, error) {
	const url = "https://api.coingecko.com/api/v3/simple/price?ids=polygon-ecosystem-token&vs_currencies=usd"

	resp, err := mc.httpClient.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("coingecko status %d: %s", resp.StatusCode, body)
	}

	var data map[string]map[string]float64
	if err := json.Unmarshal(body, &data); err != nil {
		return 0, err
	}

	price, ok := data["polygon-ecosystem-token"]["usd"]
	if !ok || price <= 0 {
		return 0, fmt.Errorf("POL price not found in response")
	}

	slog.Debug("merge: fetched POL price", "usd", price)
	return price, nil
}

// MergePositions executes an on-chain merge for the given condition.
// amount is in USDC units (e.g., 10.0 = 10 USDC worth of tokens).
// NegRisk markets are skipped for safety — they require the NegRisk adapter
// with a market-specific parentCollectionId that we don't have.
func (mc *MergeClient) MergePositions(ctx context.Context, conditionID string, amount float64, negRisk bool) (domain.MergeResult, error) {
	result := domain.MergeResult{
		ConditionID: conditionID,
		ExecutedAt:  time.Now().UTC(),
	}

	if negRisk {
		result.Error = "NegRisk merges not yet supported — requires NegRisk adapter with parentCollectionId"
		return result, fmt.Errorf("merge: %s", result.Error)
	}

	condBytes, err := hexToBytes32(conditionID)
	if err != nil {
		result.Error = fmt.Sprintf("invalid conditionID: %v", err)
		return result, err
	}

	amountInt := new(big.Int).SetInt64(int64(amount * 1_000_000))
	partition := []*big.Int{big.NewInt(1), big.NewInt(2)}

	callData, err := ctfABI.Pack("mergePositions",
		common.HexToAddress(usdcEAddress),
		[32]byte{},
		condBytes,
		partition,
		amountInt,
	)
	if err != nil {
		result.Error = fmt.Sprintf("pack calldata: %v", err)
		return result, fmt.Errorf("merge: pack: %w", err)
	}

	privKey, err := crypto.ToECDSA(mc.privateKey)
	if err != nil {
		result.Error = "invalid private key"
		return result, fmt.Errorf("merge: private key: %w", err)
	}

	nonce, err := mc.client.PendingNonceAt(ctx, mc.address)
	if err != nil {
		result.Error = fmt.Sprintf("get nonce: %v", err)
		return result, fmt.Errorf("merge: nonce: %w", err)
	}

	gasPrice, err := mc.getGasPrice(ctx)
	if err != nil {
		result.Error = fmt.Sprintf("get gas price: %v", err)
		return result, fmt.Errorf("merge: gas price: %w", err)
	}

	ctfAddr := common.HexToAddress(ctfAddress)

	// Estimate actual gas
	gasEstimate, err := mc.client.EstimateGas(ctx, ethereum.CallMsg{
		From:     mc.address,
		To:       &ctfAddr,
		GasPrice: gasPrice,
		Data:     callData,
	})
	if err != nil {
		// Fall back to conservative limit
		gasEstimate = mergeGasLimit
		slog.Warn("merge: gas estimate failed, using default", "err", err, "limit", mergeGasLimit)
	}
	// Add 20% buffer
	gasEstimate = gasEstimate * 12 / 10

	tx := types.NewTransaction(
		nonce,
		ctfAddr,
		big.NewInt(0), // no ETH value
		gasEstimate,
		gasPrice,
		callData,
	)

	chainID := big.NewInt(polygonChainID)
	signedTx, err := types.SignTx(tx, types.NewEIP155Signer(chainID), privKey)
	if err != nil {
		result.Error = fmt.Sprintf("sign tx: %v", err)
		return result, fmt.Errorf("merge: sign tx: %w", err)
	}

	if err := mc.client.SendTransaction(ctx, signedTx); err != nil {
		result.Error = fmt.Sprintf("send tx: %v", err)
		return result, fmt.Errorf("merge: send tx: %w", err)
	}

	txHash := signedTx.Hash().Hex()
	result.TxHash = txHash
	slog.Info("merge: transaction sent", "condition", conditionID[:12]+"...", "amount", amount, "tx", txHash)

	// Wait for receipt (up to 60s)
	receiptCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	receipt, err := mc.waitForReceipt(receiptCtx, signedTx.Hash())
	if err != nil {
		// TX sent but we couldn't confirm — mark as potentially succeeded
		slog.Warn("merge: could not confirm receipt, tx may still succeed", "tx", txHash, "err", err)
		result.Success = true // optimistic
		result.USDCReceived = amount
		return result, nil
	}

	if receipt.Status != types.ReceiptStatusSuccessful {
		result.Error = "transaction reverted on-chain"
		return result, fmt.Errorf("merge: tx reverted: %s", txHash)
	}

	// Calculate gas cost
	gasUsedPOL := new(big.Float).SetUint64(receipt.GasUsed)
	gasPriceF := new(big.Float).SetInt(gasPrice)
	gasCostWei := new(big.Float).Mul(gasUsedPOL, gasPriceF)
	gasCostPOL, _ := new(big.Float).Quo(gasCostWei, new(big.Float).SetFloat64(1e18)).Float64()
	gasCostUSD := gasCostPOL * mc.polPriceUSD()

	result.Success = true
	result.GasUsedPOL = gasCostPOL
	result.GasCostUSD = gasCostUSD
	result.USDCReceived = amount // 1:1 merge
	result.SpreadProfit = 0      // caller sets this based on cost basis

	slog.Info("merge: confirmed",
		"condition", conditionID[:12]+"...",
		"tx", txHash,
		"gas_usdc", fmt.Sprintf("$%.4f", gasCostUSD),
		"usdc_received", amount,
	)

	return result, nil
}

// EnsureApprovals checks and sets both:
//   - ERC1155 setApprovalForAll on the three exchange contracts (for token transfers)
//   - ERC20 USDC.e approve for both exchange contracts (for BUY collateral)
func (mc *MergeClient) EnsureApprovals(ctx context.Context) error {
	operators := []string{normalExchange, negRiskExchange, negRiskAdapter}

	for _, op := range operators {
		approved, err := mc.isApprovedForAll(ctx, common.HexToAddress(op))
		if err != nil {
			return fmt.Errorf("check ERC1155 approval for %s: %w", op, err)
		}
		if approved {
			slog.Debug("merge: ERC1155 approval already set", "operator", op)
			continue
		}

		slog.Info("merge: setting ERC1155 approval", "operator", op)
		if err := mc.setApprovalForAll(ctx, common.HexToAddress(op)); err != nil {
			return fmt.Errorf("set ERC1155 approval for %s: %w", op, err)
		}
		slog.Info("merge: ERC1155 approval set", "operator", op)
	}

	exchanges := []string{normalExchange, negRiskExchange}
	maxUint256 := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	minAllowance := new(big.Int).Mul(big.NewInt(1_000_000), big.NewInt(1_000_000)) // 1M USDC.e

	for _, ex := range exchanges {
		allowance, err := mc.erc20Allowance(ctx, common.HexToAddress(usdcEAddress), common.HexToAddress(ex))
		if err != nil {
			return fmt.Errorf("check USDC.e allowance for %s: %w", ex, err)
		}
		if allowance.Cmp(minAllowance) >= 0 {
			slog.Debug("merge: USDC.e allowance sufficient", "exchange", ex)
			continue
		}

		slog.Info("merge: setting USDC.e approval", "exchange", ex)
		if err := mc.erc20Approve(ctx, common.HexToAddress(usdcEAddress), common.HexToAddress(ex), maxUint256); err != nil {
			return fmt.Errorf("set USDC.e approval for %s: %w", ex, err)
		}
		slog.Info("merge: USDC.e approval set", "exchange", ex)
	}

	return nil
}

// isApprovedForAll checks ERC1155 approval for an operator on the CTF contract.
func (mc *MergeClient) isApprovedForAll(ctx context.Context, operator common.Address) (bool, error) {
	callData, err := erc1155ABI.Pack("isApprovedForAll", mc.address, operator)
	if err != nil {
		return false, err
	}

	ctfAddr := common.HexToAddress(ctfAddress)
	result, err := mc.client.CallContract(ctx, ethereum.CallMsg{
		To:   &ctfAddr,
		Data: callData,
	}, nil)
	if err != nil {
		return false, err
	}

	vals, err := erc1155ABI.Unpack("isApprovedForAll", result)
	if err != nil || len(vals) == 0 {
		return false, err
	}
	return vals[0].(bool), nil
}

// setApprovalForAll sends a setApprovalForAll transaction on the CTF contract.
func (mc *MergeClient) setApprovalForAll(ctx context.Context, operator common.Address) error {
	callData, err := erc1155ABI.Pack("setApprovalForAll", operator, true)
	if err != nil {
		return err
	}

	privKey, err := crypto.ToECDSA(mc.privateKey)
	if err != nil {
		return err
	}

	nonce, err := mc.client.PendingNonceAt(ctx, mc.address)
	if err != nil {
		return fmt.Errorf("nonce: %w", err)
	}

	gasPrice, err := mc.getGasPrice(ctx)
	if err != nil {
		return fmt.Errorf("gas price: %w", err)
	}

	ctfAddr := common.HexToAddress(ctfAddress)
	tx := types.NewTransaction(nonce, ctfAddr, big.NewInt(0), approvalGasLimit, gasPrice, callData)

	chainID := big.NewInt(polygonChainID)
	signed, err := types.SignTx(tx, types.NewEIP155Signer(chainID), privKey)
	if err != nil {
		return err
	}

	if err := mc.client.SendTransaction(ctx, signed); err != nil {
		return err
	}

	receiptCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	receipt, err := mc.waitForReceipt(receiptCtx, signed.Hash())
	if err != nil {
		return fmt.Errorf("wait receipt: %w", err)
	}
	if receipt.Status != types.ReceiptStatusSuccessful {
		return fmt.Errorf("setApprovalForAll tx reverted")
	}
	return nil
}

// erc20Allowance queries the current ERC20 allowance.
func (mc *MergeClient) erc20Allowance(ctx context.Context, token, spender common.Address) (*big.Int, error) {
	callData, err := erc20ABI.Pack("allowance", mc.address, spender)
	if err != nil {
		return nil, err
	}

	result, err := mc.client.CallContract(ctx, ethereum.CallMsg{
		To:   &token,
		Data: callData,
	}, nil)
	if err != nil {
		return nil, err
	}

	vals, err := erc20ABI.Unpack("allowance", result)
	if err != nil || len(vals) == 0 {
		return big.NewInt(0), err
	}
	return vals[0].(*big.Int), nil
}

// erc20Approve sends an ERC20 approve transaction.
func (mc *MergeClient) erc20Approve(ctx context.Context, token, spender common.Address, amount *big.Int) error {
	callData, err := erc20ABI.Pack("approve", spender, amount)
	if err != nil {
		return err
	}

	privKey, err := crypto.ToECDSA(mc.privateKey)
	if err != nil {
		return err
	}

	nonce, err := mc.client.PendingNonceAt(ctx, mc.address)
	if err != nil {
		return fmt.Errorf("nonce: %w", err)
	}

	gasPrice, err := mc.getGasPrice(ctx)
	if err != nil {
		return fmt.Errorf("gas price: %w", err)
	}

	tx := types.NewTransaction(nonce, token, big.NewInt(0), approvalGasLimit, gasPrice, callData)

	chainID := big.NewInt(polygonChainID)
	signed, err := types.SignTx(tx, types.NewEIP155Signer(chainID), privKey)
	if err != nil {
		return err
	}

	if err := mc.client.SendTransaction(ctx, signed); err != nil {
		return err
	}

	receiptCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	receipt, err := mc.waitForReceipt(receiptCtx, signed.Hash())
	if err != nil {
		return fmt.Errorf("wait receipt: %w", err)
	}
	if receipt.Status != types.ReceiptStatusSuccessful {
		return fmt.Errorf("ERC20 approve tx reverted")
	}
	return nil
}

// getGasPrice returns the current gas price, with caching to avoid excessive RPC calls.
func (mc *MergeClient) getGasPrice(ctx context.Context) (*big.Int, error) {
	mc.mu.RLock()
	cached := mc.cachedGasWei
	updatedAt := mc.gasUpdatedAt
	mc.mu.RUnlock()

	if cached != nil && time.Since(updatedAt) < gasPriceUpdateInterval {
		return cached, nil
	}

	price, err := mc.client.SuggestGasPrice(ctx)
	if err != nil {
		if cached != nil {
			return cached, nil
		}
		return big.NewInt(30_000_000_000), nil // 30 gwei fallback
	}

	// Add 10% buffer for faster inclusion (copy to avoid mutating SuggestGasPrice return)
	buffered := new(big.Int).Mul(price, big.NewInt(11))
	buffered.Div(buffered, big.NewInt(10))
	price = buffered

	mc.mu.Lock()
	mc.cachedGasWei = price
	mc.gasUpdatedAt = time.Now()
	mc.mu.Unlock()

	return price, nil
}

// waitForReceipt polls for a transaction receipt until confirmed or timeout.
func (mc *MergeClient) waitForReceipt(ctx context.Context, txHash common.Hash) (*types.Receipt, error) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			receipt, err := mc.client.TransactionReceipt(ctx, txHash)
			if err != nil {
				continue // not yet mined
			}
			return receipt, nil
		}
	}
}

// hexToBytes32 converts a 0x-prefixed hex string to [32]byte.
func hexToBytes32(s string) ([32]byte, error) {
	s = strings.TrimPrefix(s, "0x")
	if len(s) != 64 {
		return [32]byte{}, fmt.Errorf("expected 64 hex chars, got %d", len(s))
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return [32]byte{}, err
	}
	var arr [32]byte
	copy(arr[:], b)
	return arr, nil
}
