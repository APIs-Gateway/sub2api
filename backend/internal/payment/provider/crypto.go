package provider

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/payment"
)

const (
	cryptoFiatCurrency   = "CNY"
	cryptoDefaultMarkup  = 1.002
	cryptoDefaultMinUSDT = 5.0
	cryptoDefaultTimeout = 1200
)

var supportedCryptoNetworks = []string{
	"usdt.trc20",
	"usdt.polygon",
	"usdt.solana",
}

// Crypto implements the built-in crypto payment method backed by BEpusdt.
// The checkout amount remains CNY (the site's payment currency); BEpusdt
// converts it to USDT at the latest synced rate plus the provider's markup.
type Crypto struct {
	instanceID   string
	config       map[string]string
	baseURL      string
	publicBase   string
	callbackBase string
	beHost       string
	username     string
	password     string
	securePath   string
	apiToken     string
	markup       float64
	minUSDT      float64
	timeoutSec   int64
	networks     map[string]struct{}
	client       *http.Client
}

func NewCrypto(instanceID string, config map[string]string) (*Crypto, error) {
	required := []string{"beBase", "publicBase", "callbackBase", "adminUsername", "adminPassword", "adminSecurePath", "apiToken"}
	for _, key := range required {
		if strings.TrimSpace(config[key]) == "" {
			return nil, fmt.Errorf("crypto config missing required key: %s", key)
		}
	}
	baseURL, err := normalizeCryptoBaseURL(config["beBase"])
	if err != nil {
		return nil, fmt.Errorf("crypto beBase: %w", err)
	}
	publicBase, err := normalizeCryptoBaseURL(config["publicBase"])
	if err != nil {
		return nil, fmt.Errorf("crypto publicBase: %w", err)
	}
	callbackBase, err := normalizeCryptoBaseURL(config["callbackBase"])
	if err != nil {
		return nil, fmt.Errorf("crypto callbackBase: %w", err)
	}
	markup := cryptoDefaultMarkup
	if raw := strings.TrimSpace(config["rateMarkup"]); raw != "" {
		markup, err = strconv.ParseFloat(raw, 64)
		if err != nil || markup <= 0 || math.IsNaN(markup) || math.IsInf(markup, 0) {
			return nil, fmt.Errorf("crypto rateMarkup must be a positive number")
		}
	}
	minUSDT := cryptoDefaultMinUSDT
	if raw := strings.TrimSpace(config["minUsdt"]); raw != "" {
		minUSDT, err = strconv.ParseFloat(raw, 64)
		if err != nil || minUSDT <= 0 || math.IsNaN(minUSDT) || math.IsInf(minUSDT, 0) {
			return nil, fmt.Errorf("crypto minUsdt must be a positive number")
		}
	}
	timeoutSec := int64(cryptoDefaultTimeout)
	if raw := strings.TrimSpace(config["timeoutSec"]); raw != "" {
		timeoutSec, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || timeoutSec < 180 {
			return nil, fmt.Errorf("crypto timeoutSec must be at least 180 seconds")
		}
	}
	networks := parseCryptoNetworks(config["networks"])
	if len(networks) == 0 {
		return nil, fmt.Errorf("crypto networks must include at least one supported network")
	}
	beHost := strings.TrimSpace(config["beHost"])
	if beHost == "" {
		parsed, _ := url.Parse(publicBase)
		beHost = parsed.Host
	}

	cfg := make(map[string]string, len(config))
	for key, value := range config {
		cfg[key] = value
	}
	return &Crypto{
		instanceID:   instanceID,
		config:       cfg,
		baseURL:      baseURL,
		publicBase:   publicBase,
		callbackBase: callbackBase,
		beHost:       beHost,
		username:     strings.TrimSpace(config["adminUsername"]),
		password:     config["adminPassword"],
		securePath:   strings.TrimSpace(config["adminSecurePath"]),
		apiToken:     config["apiToken"],
		markup:       markup,
		minUSDT:      minUSDT,
		timeoutSec:   timeoutSec,
		networks:     networks,
		client:       &http.Client{Timeout: 15 * time.Second},
	}, nil
}

func (c *Crypto) Name() string        { return "Crypto Pay" }
func (c *Crypto) ProviderKey() string { return payment.TypeCrypto }
func (c *Crypto) SupportedTypes() []payment.PaymentType {
	return []payment.PaymentType{payment.TypeCrypto}
}

func (c *Crypto) CreatePayment(ctx context.Context, req payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	network := normalizeCryptoNetwork(req.CryptoNetwork)
	if _, ok := c.networks[network]; !ok {
		return nil, fmt.Errorf("crypto network is not enabled: %s", network)
	}
	amountCNY, err := strconv.ParseFloat(strings.TrimSpace(req.Amount), 64)
	if err != nil || amountCNY <= 0 || math.IsNaN(amountCNY) || math.IsInf(amountCNY, 0) {
		return nil, fmt.Errorf("invalid crypto payment amount")
	}
	rawRate, err := c.currentRate(ctx)
	if err != nil {
		return nil, fmt.Errorf("load USDT/CNY rate: %w", err)
	}
	usdtAmount := amountCNY / (rawRate * c.markup)
	if usdtAmount+1e-9 < c.minUSDT {
		return nil, fmt.Errorf("crypto payment must be at least %.2f USDT", c.minUSDT)
	}

	notifyURL := strings.TrimSpace(req.NotifyURL)
	if notifyURL == "" {
		notifyURL = c.callbackBase + "/api/v1/payment/webhook/crypto"
	}
	payload := map[string]any{
		"order_id":     req.OrderID,
		"amount":       amountCNY,
		"fiat":         cryptoFiatCurrency,
		"trade_type":   network,
		"name":         req.Subject,
		"notify_url":   notifyURL,
		"redirect_url": req.ReturnURL,
		"timeout":      float64(c.timeoutSec),
		"rate":         "~" + strconv.FormatFloat(c.markup, 'f', -1, 64),
	}
	payload["signature"] = signCryptoMap(payload, c.apiToken)
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal crypto payment request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/order/create-transaction", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	if c.beHost != "" {
		request.Host = c.beHost
	}
	response, err := c.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("create crypto payment: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	var envelope struct {
		Code       int    `json:"code"`
		StatusCode int    `json:"status_code"`
		Message    string `json:"message"`
		Msg        string `json:"msg"`
		Data       struct {
			TradeID    string `json:"trade_id"`
			PaymentURL string `json:"payment_url"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("decode crypto payment response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || (envelope.Code != 200 && envelope.StatusCode != 200) || envelope.Data.TradeID == "" {
		message := envelope.Message
		if message == "" {
			message = envelope.Msg
		}
		if message == "" {
			message = "BEpusdt rejected the order"
		}
		return nil, errors.New(message)
	}

	return &payment.CreatePaymentResponse{
		TradeNo:  envelope.Data.TradeID,
		PayURL:   normalizeCryptoPaymentURL(envelope.Data.PaymentURL, c.publicBase, envelope.Data.TradeID),
		Currency: cryptoFiatCurrency,
	}, nil
}

func (c *Crypto) QueryOrder(_ context.Context, tradeNo string) (*payment.QueryOrderResponse, error) {
	return &payment.QueryOrderResponse{
		TradeNo:  tradeNo,
		Status:   payment.ProviderStatusPending,
		Metadata: map[string]string{"currency": cryptoFiatCurrency},
	}, nil
}

func (c *Crypto) VerifyNotification(_ context.Context, rawBody string, _ map[string]string) (*payment.PaymentNotification, error) {
	values, err := decodeCryptoJSONValues([]byte(rawBody))
	if err != nil {
		return nil, fmt.Errorf("decode crypto callback: %w", err)
	}
	provided, _ := values["signature"].(string)
	expected := signCryptoMap(values, c.apiToken)
	if provided == "" || subtle.ConstantTimeCompare([]byte(strings.ToLower(provided)), []byte(expected)) != 1 {
		return nil, fmt.Errorf("crypto callback signature mismatch")
	}
	if cryptoIntValue(values["status"]) != 2 {
		return nil, nil
	}
	orderID, _ := values["order_id"].(string)
	tradeID, _ := values["trade_id"].(string)
	transactionID, _ := values["block_transaction_id"].(string)
	amount, err := cryptoFloatValue(values["amount"])
	actualUSDT, actualErr := cryptoFloatValue(values["actual_amount"])
	if strings.TrimSpace(orderID) == "" || strings.TrimSpace(tradeID) == "" || strings.TrimSpace(transactionID) == "" || err != nil || amount <= 0 || actualErr != nil || actualUSDT < c.minUSDT {
		return nil, fmt.Errorf("crypto callback has invalid paid amount or transaction")
	}
	network, _ := values["trade_type"].(string)
	return &payment.PaymentNotification{
		TradeNo:  tradeID,
		OrderID:  orderID,
		Amount:   amount,
		Status:   payment.NotificationStatusSuccess,
		RawData:  rawBody,
		Metadata: map[string]string{"currency": cryptoFiatCurrency, "network": normalizeCryptoNetwork(network), "transaction_id": transactionID},
	}, nil
}

func (c *Crypto) Refund(context.Context, payment.RefundRequest) (*payment.RefundResponse, error) {
	return nil, fmt.Errorf("crypto payment refunds are not supported")
}

func (c *Crypto) currentRate(ctx context.Context) (float64, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return 0, err
	}
	client := &http.Client{Timeout: 10 * time.Second, Jar: jar}
	secureRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+c.securePath, nil)
	if err != nil {
		return 0, err
	}
	secureResponse, err := client.Do(secureRequest)
	if err != nil {
		return 0, err
	}
	_ = secureResponse.Body.Close()

	loginBody, _ := json.Marshal(map[string]string{"username": c.username, "password": c.password})
	loginRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/auth/login", bytes.NewReader(loginBody))
	if err != nil {
		return 0, err
	}
	loginRequest.Header.Set("Content-Type", "application/json")
	loginResponse, err := client.Do(loginRequest)
	if err != nil {
		return 0, err
	}
	defer func() { _ = loginResponse.Body.Close() }()
	var login struct {
		Code int `json:"code"`
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(loginResponse.Body, 1<<20)).Decode(&login); err != nil {
		return 0, err
	}
	if loginResponse.StatusCode < 200 || loginResponse.StatusCode >= 300 || login.Code != 200 || login.Data.Token == "" {
		return 0, fmt.Errorf("BEpusdt admin login failed")
	}

	rateBody, _ := json.Marshal(map[string]any{"page": 1, "size": 1, "sort": "desc", "fiat": cryptoFiatCurrency, "crypto": "USDT"})
	rateRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/rate/list", bytes.NewReader(rateBody))
	if err != nil {
		return 0, err
	}
	rateRequest.Header.Set("Content-Type", "application/json")
	rateRequest.Header.Set("Authorization", login.Data.Token)
	rateResponse, err := client.Do(rateRequest)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rateResponse.Body.Close() }()
	var rateResult struct {
		Code int `json:"code"`
		Data []struct {
			RawRate float64 `json:"raw_rate"`
			Rate    string  `json:"rate"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(rateResponse.Body, 1<<20)).Decode(&rateResult); err != nil {
		return 0, err
	}
	if rateResponse.StatusCode < 200 || rateResponse.StatusCode >= 300 || rateResult.Code != 200 || len(rateResult.Data) == 0 {
		return 0, fmt.Errorf("USDT/CNY rate unavailable")
	}
	raw := rateResult.Data[0].RawRate
	if raw <= 0 {
		raw, _ = strconv.ParseFloat(rateResult.Data[0].Rate, 64)
	}
	if raw <= 0 || math.IsNaN(raw) || math.IsInf(raw, 0) {
		return 0, fmt.Errorf("invalid USDT/CNY rate")
	}
	return raw, nil
}

func normalizeCryptoBaseURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("must be an absolute http(s) URL")
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func normalizeCryptoPaymentURL(raw, publicBase, tradeID string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err == nil && parsed.Path != "" {
		base, baseErr := url.Parse(publicBase)
		if baseErr == nil {
			parsed.Scheme = base.Scheme
			parsed.Host = base.Host
			parsed.User = nil
			return parsed.String()
		}
	}
	return publicBase + "/pay/checkout/" + url.PathEscape(tradeID)
}

func parseCryptoNetworks(raw string) map[string]struct{} {
	result := make(map[string]struct{})
	if strings.TrimSpace(raw) == "" {
		for _, network := range supportedCryptoNetworks {
			result[network] = struct{}{}
		}
		return result
	}
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' }) {
		if network := normalizeCryptoNetwork(part); network != "" {
			result[network] = struct{}{}
		}
	}
	return result
}

func normalizeCryptoNetwork(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "trc20", "tron", "usdt.trc20":
		return "usdt.trc20"
	case "polygon", "matic", "usdt.polygon":
		return "usdt.polygon"
	case "solana", "sol", "usdt.solana":
		return "usdt.solana"
	default:
		return ""
	}
}

func decodeCryptoJSONValues(raw []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var values map[string]any
	if err := decoder.Decode(&values); err != nil {
		return nil, err
	}
	return values, nil
}

func signCryptoMap(values map[string]any, token string) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		if key != "signature" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		if values[key] == nil {
			continue
		}
		value := fmt.Sprintf("%v", values[key])
		if number, ok := values[key].(json.Number); ok {
			parsed, err := strconv.ParseFloat(number.String(), 64)
			if err != nil {
				continue
			}
			value = fmt.Sprintf("%v", parsed)
		}
		if value != "" {
			parts = append(parts, key+"="+value)
		}
	}
	sum := md5.Sum([]byte(strings.Join(parts, "&") + token))
	return fmt.Sprintf("%x", sum)
}

func cryptoFloatValue(value any) (float64, error) {
	switch typed := value.(type) {
	case json.Number:
		return strconv.ParseFloat(typed.String(), 64)
	case string:
		return strconv.ParseFloat(strings.TrimSpace(typed), 64)
	case float64:
		return typed, nil
	default:
		return 0, fmt.Errorf("invalid number")
	}
}

func cryptoIntValue(value any) int {
	parsed, err := cryptoFloatValue(value)
	if err != nil {
		return 0
	}
	return int(parsed)
}
