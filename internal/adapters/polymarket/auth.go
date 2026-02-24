package polymarket

// auth.go — Polymarket CLOB authenticated client.
//
// Implements two-level authentication:
//   L1: EIP-712 signature with wallet private key → derive API credentials
//   L2: HMAC-SHA256 signing of every authenticated request

import (
	"context"
	"crypto/ecdsa"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/polymarket/go-order-utils/pkg/builder"
	"github.com/polymarket/go-order-utils/pkg/config"
	gomodel "github.com/polymarket/go-order-utils/pkg/model"
)

const (
	polygonChainID = int64(137)

	// CLOB EIP-712 auth domain
	clobDomainName    = "ClobAuthDomain"
	clobDomainVersion = "1"
	// Message signed for deriving API keys
	clobAuthMessage = "This message attests that I control the given wallet"

	// Taker address — zero address = public order
	zeroAddress = "0x0000000000000000000000000000000000000000"
)

// apiCredentials holds the CLOB API credentials derived from a wallet.
type apiCredentials struct {
	APIKey     string `json:"apiKey"`
	Secret     string `json:"secret"`
	Passphrase string `json:"passphrase"`
}

// AuthClient wraps the base Client with L1/L2 auth capabilities.
type AuthClient struct {
	*Client
	privateKey  *ecdsa.PrivateKey
	address     common.Address
	contracts   *config.Contracts
	orderBuilder builder.ExchangeOrderBuilder
	creds       *apiCredentials
}

// NewAuthClient creates an authenticated trading client.
// privateKeyHex is the Polygon private key without 0x prefix.
func NewAuthClient(clobBase, gammaBase, privateKeyHex string) (*AuthClient, error) {
	key, err := crypto.HexToECDSA(privateKeyHex)
	if err != nil {
		return nil, fmt.Errorf("auth: invalid private key: %w", err)
	}

	contracts, err := config.GetContracts(polygonChainID)
	if err != nil {
		return nil, fmt.Errorf("auth: get contracts: %w", err)
	}

	ob := builder.NewExchangeOrderBuilderImpl(big.NewInt(polygonChainID), nil)

	addr := crypto.PubkeyToAddress(key.PublicKey)

	ac := &AuthClient{
		Client:       NewClient(clobBase, gammaBase),
		privateKey:   key,
		address:      addr,
		contracts:    contracts,
		orderBuilder: ob,
	}

	return ac, nil
}

// Address returns the wallet address.
func (ac *AuthClient) Address() string {
	return ac.address.Hex()
}

// EnsureCreds derives (or re-derives) API credentials via L1 auth.
// Should be called once on startup; credentials are cached.
func (ac *AuthClient) EnsureCreds(ctx context.Context) error {
	if ac.creds != nil {
		return nil
	}

	ts := strconv.FormatInt(time.Now().Unix(), 10)
	sig, err := ac.signClobAuth(ts, "0")
	if err != nil {
		return fmt.Errorf("auth: sign l1: %w", err)
	}

	url := fmt.Sprintf("%s/auth/derive-api-key", ac.clobBase)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("auth: derive-api-key request: %w", err)
	}
	req.Header.Set("POLY_ADDRESS", ac.address.Hex())
	req.Header.Set("POLY_SIGNATURE", sig)
	req.Header.Set("POLY_TIMESTAMP", ts)
	req.Header.Set("POLY_NONCE", "0")

	resp, err := ac.http.Do(req)
	if err != nil {
		return fmt.Errorf("auth: derive-api-key: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("auth: derive-api-key status %d: %s", resp.StatusCode, body)
	}

	var creds apiCredentials
	if err := json.Unmarshal(body, &creds); err != nil {
		return fmt.Errorf("auth: parse creds: %w", err)
	}
	ac.creds = &creds
	return nil
}

// EIP-712 type hashes (computed once).
var (
	eip712DomainTypeHash = crypto.Keccak256Hash([]byte(
		"EIP712Domain(string name,string version,uint256 chainId)",
	))
	clobAuthTypeHash = crypto.Keccak256Hash([]byte(
		"ClobAuth(address address,string timestamp,uint256 nonce,string message)",
	))
)

// clobAuthDomainSeparator computes the EIP-712 domain separator for ClobAuthDomain.
func clobAuthDomainSeparator() common.Hash {
	var buf []byte
	buf = append(buf, eip712DomainTypeHash.Bytes()...)
	buf = append(buf, crypto.Keccak256Hash([]byte(clobDomainName)).Bytes()...)
	buf = append(buf, crypto.Keccak256Hash([]byte(clobDomainVersion)).Bytes()...)
	buf = append(buf, common.LeftPadBytes(big.NewInt(polygonChainID).Bytes(), 32)...)
	return crypto.Keccak256Hash(buf)
}

// signClobAuth signs the ClobAuth EIP-712 typed data for L1 auth.
func (ac *AuthClient) signClobAuth(timestamp, nonce string) (string, error) {
	nonceInt, ok := new(big.Int).SetString(nonce, 10)
	if !ok {
		return "", fmt.Errorf("invalid nonce: %s", nonce)
	}

	var structBuf []byte
	structBuf = append(structBuf, clobAuthTypeHash.Bytes()...)
	structBuf = append(structBuf, common.LeftPadBytes(ac.address.Bytes(), 32)...)
	structBuf = append(structBuf, crypto.Keccak256Hash([]byte(timestamp)).Bytes()...)
	structBuf = append(structBuf, common.LeftPadBytes(nonceInt.Bytes(), 32)...)
	structBuf = append(structBuf, crypto.Keccak256Hash([]byte(clobAuthMessage)).Bytes()...)
	structHash := crypto.Keccak256Hash(structBuf)

	var rawBuf []byte
	rawBuf = append(rawBuf, 0x19, 0x01)
	rawBuf = append(rawBuf, clobAuthDomainSeparator().Bytes()...)
	rawBuf = append(rawBuf, structHash.Bytes()...)
	msgHash := crypto.Keccak256Hash(rawBuf)

	sig, err := crypto.Sign(msgHash.Bytes(), ac.privateKey)
	if err != nil {
		return "", err
	}
	sig[64] += 27
	return "0x" + fmt.Sprintf("%x", sig), nil
}

// l2Headers returns the authenticated headers for L2 API calls.
func (ac *AuthClient) l2Headers(method, path, body string) (map[string]string, error) {
	if ac.creds == nil {
		return nil, fmt.Errorf("auth: credentials not derived yet")
	}

	ts := strconv.FormatInt(time.Now().Unix(), 10)
	msg := ts + strings.ToUpper(method) + path + body

	secretBytes, err := base64.URLEncoding.DecodeString(ac.creds.Secret)
	if err != nil {
		return nil, fmt.Errorf("auth: decode secret: %w", err)
	}

	mac := hmac.New(sha256.New, secretBytes)
	mac.Write([]byte(msg))
	sig := base64.URLEncoding.EncodeToString(mac.Sum(nil))

	return map[string]string{
		"POLY_ADDRESS":    ac.address.Hex(),
		"POLY_SIGNATURE":  sig,
		"POLY_TIMESTAMP":  ts,
		"POLY_API_KEY":    ac.creds.APIKey,
		"POLY_PASSPHRASE": ac.creds.Passphrase,
	}, nil
}

// doL2 executes an authenticated L2 HTTP request with rate limiting.
// HMAC headers are regenerated on every attempt so the timestamp stays fresh.
func (ac *AuthClient) doL2(ctx context.Context, method, path string, reqBody, out any) error {
	var bodyStr string

	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("marshal: %w", err)
		}
		bodyStr = string(b)
	}

	fullURL := ac.clobBase + path

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := ac.clobLimiter.Wait(ctx); err != nil {
			return fmt.Errorf("rate limiter: %w", err)
		}

		headers, err := ac.l2Headers(method, path, bodyStr)
		if err != nil {
			return err
		}

		var bodyReader io.Reader
		if bodyStr != "" {
			bodyReader = strings.NewReader(bodyStr)
		}

		req, err := http.NewRequestWithContext(ctx, method, fullURL, bodyReader)
		if err != nil {
			return fmt.Errorf("new request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := ac.http.Do(req)
		if err != nil {
			if attempt == maxRetries {
				return fmt.Errorf("request failed after %d retries: %w", maxRetries, err)
			}
			ac.sleep(ctx, attempt)
			continue
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusTooManyRequests {
			ac.sleep(ctx, attempt)
			continue
		}
		if resp.StatusCode >= 500 {
			if attempt == maxRetries {
				return fmt.Errorf("server error %d: %s", resp.StatusCode, respBody)
			}
			ac.sleep(ctx, attempt)
			continue
		}
		if resp.StatusCode >= 400 {
			return fmt.Errorf("client error %d: %s", resp.StatusCode, respBody)
		}

		if out != nil {
			if err := json.Unmarshal(respBody, out); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}
		}
		return nil
	}
	return fmt.Errorf("exhausted %d retries", maxRetries)
}

// buildSignedOrder creates an EIP-712 signed order for the given parameters.
// price and size are in USDC units (e.g., 0.80 and 10.0).
// Uses integer arithmetic to avoid floating-point precision errors that the
// CLOB API rejects. The API verifies: makerAmount == price * takerAmount exactly.
func (ac *AuthClient) buildSignedOrder(tokenID string, price, size float64, negRisk bool) (*gomodel.SignedOrder, error) {
	pricePrecision := detectPricePrecision(price)
	priceInt := int64(math.Round(price * float64(pricePrecision)))
	sharesCents := int64(math.Floor(size / price * 100))

	amountFactor := int64(1_000_000) / (100 * pricePrecision)
	makerAmount := sharesCents * priceInt * amountFactor
	takerAmount := sharesCents * 10000

	if makerAmount <= 0 || takerAmount <= 0 {
		return nil, fmt.Errorf("invalid amounts: maker=%d taker=%d (price=%.4f size=%.4f)", makerAmount, takerAmount, price, size)
	}

	var verifyingContract gomodel.VerifyingContract
	if negRisk {
		verifyingContract = gomodel.NegRiskCTFExchange
	} else {
		verifyingContract = gomodel.CTFExchange
	}

	orderData := &gomodel.OrderData{
		Maker:         ac.address.Hex(),
		Taker:         zeroAddress,
		TokenId:       tokenID,
		MakerAmount:   strconv.FormatInt(makerAmount, 10),
		TakerAmount:   strconv.FormatInt(takerAmount, 10),
		FeeRateBps:    "0",
		Nonce:         "0",
		Signer:        ac.address.Hex(),
		Expiration:    "0",
		Side:          gomodel.BUY,
		SignatureType: gomodel.EOA,
	}

	signed, err := ac.orderBuilder.BuildSignedOrder(ac.privateKey, orderData, verifyingContract)
	if err != nil {
		return nil, fmt.Errorf("build signed order: %w", err)
	}
	return signed, nil
}

// detectPricePrecision returns the multiplier matching the market's tick size.
// e.g. price=0.60 → 100 (tick 0.01), price=0.673 → 1000 (tick 0.001).
func detectPricePrecision(price float64) int64 {
	for _, prec := range []int64{100, 1000, 10000} {
		rounded := math.Round(price * float64(prec))
		if math.Abs(rounded/float64(prec)-price) < 1e-10 {
			return prec
		}
	}
	return 100
}
