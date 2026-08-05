package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	tronpb "github.com/tronprotocol/tron-deployment/internal/tronproto/pb"
)

// NodeClient wraps the subset of java-tron's HTTP API needed by txgen:
//
//	/wallet/createtransaction         — build unsigned TRX transfer
//	/wallet/transferasset             — build unsigned TRC10 transfer
//	/wallet/triggersmartcontract      — build unsigned TRC20 call
//	/wallet/getnowblock               — fetch head block (for stats / health)
//	/wallet/getblockbynum             — fetch block at height (for TPS calc)
//
// All endpoints accept and return JSON.
type NodeClient struct {
	baseURL string
	http    *http.Client
}

func NewNodeClient(baseURL string, timeout time.Duration) *NodeClient {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &NodeClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: timeout},
	}
}

// UnsignedTx is the partial shape we care about from the node's
// createtransaction-family responses. Extra fields are kept verbatim
// in Raw so we can re-serialize the whole tx after attaching a signature.
type UnsignedTx struct {
	TxID       string          `json:"txID"`
	RawDataHex string          `json:"raw_data_hex"`
	Raw        json.RawMessage `json:"-"`
}

// CreateTRXTransfer asks the node to build a TRX transfer transaction.
// Amounts are in SUN (1 TRX = 1_000_000 SUN).
func (c *NodeClient) CreateTRXTransfer(ctx context.Context, fromHex, toHex string, amountSun int64) (*UnsignedTx, error) {
	body := map[string]any{
		"owner_address": fromHex,
		"to_address":    toHex,
		"amount":        amountSun,
	}
	return c.callUnsigned(ctx, "/wallet/createtransaction", body)
}

// CreateTRC10Transfer builds a TRC10 asset transfer.
// assetIDStr is the numeric token id (e.g. "1000001"), passed as a
// string because the HTTP API rejects integer overflow on large ids.
func (c *NodeClient) CreateTRC10Transfer(ctx context.Context, fromHex, toHex, assetIDStr string, amount int64) (*UnsignedTx, error) {
	body := map[string]any{
		"owner_address": fromHex,
		"to_address":    toHex,
		"asset_name":    hex.EncodeToString([]byte(assetIDStr)),
		"amount":        amount,
	}
	return c.callUnsigned(ctx, "/wallet/transferasset", body)
}

// CreateTRC20Transfer builds a `transfer(address,uint256)` call to a
// TRC20 contract. amount is the raw token amount (account for decimals).
func (c *NodeClient) CreateTRC20Transfer(ctx context.Context, fromHex, contractHex, toHex string, amount int64, feeLimit int64) (*UnsignedTx, error) {
	// transfer(address,uint256) selector = 0xa9059cbb
	// recipient: 32-byte left-padded (drop 0x41 prefix → 20 bytes → pad to 32)
	if len(toHex) != AddressHexLen {
		return nil, fmt.Errorf("to address must be %d hex chars", AddressHexLen)
	}
	recipient20, err := hex.DecodeString(toHex[2:]) // strip 0x41
	if err != nil {
		return nil, err
	}
	param := make([]byte, 64)
	copy(param[12:32], recipient20)
	amtBytes := bigEndianI64(amount)
	copy(param[64-len(amtBytes):], amtBytes)

	body := map[string]any{
		"owner_address":     fromHex,
		"contract_address":  contractHex,
		"function_selector": "transfer(address,uint256)",
		"parameter":         hex.EncodeToString(param),
		"fee_limit":         feeLimit,
		"call_value":        0,
	}
	// triggersmartcontract returns the tx wrapped under "transaction";
	// the selector is auto-computed server-side from function_selector.
	respBytes, err := c.post(ctx, "/wallet/triggersmartcontract", body)
	if err != nil {
		return nil, err
	}
	var wrapper struct {
		Transaction json.RawMessage `json:"transaction"`
		Result      struct {
			Result  bool   `json:"result"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"result"`
	}
	if err := json.Unmarshal(respBytes, &wrapper); err != nil {
		return nil, fmt.Errorf("decode triggersmartcontract: %w", err)
	}
	if !wrapper.Result.Result && len(wrapper.Transaction) == 0 {
		return nil, fmt.Errorf("triggersmartcontract: %s %s", wrapper.Result.Code, wrapper.Result.Message)
	}
	return parseUnsigned(wrapper.Transaction)
}

// Block is the minimal block shape used for TPS statistics.
type Block struct {
	BlockID     string `json:"blockID"`
	BlockHeader struct {
		RawData struct {
			Number    int64 `json:"number"`
			Timestamp int64 `json:"timestamp"`
		} `json:"raw_data"`
	} `json:"block_header"`
	Transactions []json.RawMessage `json:"transactions"`
}

// GetBlockByNum returns the block at the given height, or nil if absent.
func (c *NodeClient) GetBlockByNum(ctx context.Context, num int64) (*Block, error) {
	respBytes, err := c.post(ctx, "/wallet/getblockbynum", map[string]any{"num": num})
	if err != nil {
		return nil, err
	}
	// Empty block returns `{}` — guard before unmarshaling.
	if bytes.Equal(bytes.TrimSpace(respBytes), []byte("{}")) {
		return nil, nil
	}
	var b Block
	if err := json.Unmarshal(respBytes, &b); err != nil {
		return nil, err
	}
	if b.BlockHeader.RawData.Number == 0 && b.BlockID == "" {
		return nil, nil
	}
	return &b, nil
}

// GetNowBlock returns the current head block.
func (c *NodeClient) GetNowBlock(ctx context.Context) (*Block, error) {
	respBytes, err := c.post(ctx, "/wallet/getnowblock", nil)
	if err != nil {
		return nil, err
	}
	var b Block
	if err := json.Unmarshal(respBytes, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

// --- internals ---------------------------------------------------------

func (c *NodeClient) callUnsigned(ctx context.Context, path string, body map[string]any) (*UnsignedTx, error) {
	respBytes, err := c.post(ctx, path, body)
	if err != nil {
		return nil, err
	}
	return parseUnsigned(respBytes)
}

func parseUnsigned(respBytes []byte) (*UnsignedTx, error) {
	// java-tron sometimes returns {"Error":"..."} on bad input.
	var probe struct {
		Error string `json:"Error"`
	}
	if err := json.Unmarshal(respBytes, &probe); err == nil && probe.Error != "" {
		return nil, errors.New(probe.Error)
	}
	var u UnsignedTx
	if err := json.Unmarshal(respBytes, &u); err != nil {
		return nil, err
	}
	if u.TxID == "" || u.RawDataHex == "" {
		return nil, errors.New("response missing txID/raw_data_hex (check sender balance / address validity)")
	}
	u.Raw = append([]byte{}, respBytes...)
	return &u, nil
}

// ExtendExpiration rewrites raw_data.expiration before signing, then refreshes
// raw_data_hex and txID so signatures cover the updated transaction body.
func (u *UnsignedTx) ExtendExpiration(expirationMillis int64) error {
	if expirationMillis <= 0 {
		return nil
	}
	rawBytes, err := hex.DecodeString(u.RawDataHex)
	if err != nil {
		return fmt.Errorf("decode raw_data_hex: %w", err)
	}
	var raw tronpb.TransactionRaw
	if err := proto.Unmarshal(rawBytes, &raw); err != nil {
		return fmt.Errorf("decode raw_data proto: %w", err)
	}
	if raw.Timestamp <= 0 {
		return errors.New("raw_data timestamp is missing")
	}
	raw.Expiration = raw.Timestamp + expirationMillis
	updatedRaw, err := proto.Marshal(&raw)
	if err != nil {
		return fmt.Errorf("encode raw_data proto: %w", err)
	}
	sum := sha256.Sum256(updatedRaw)
	rawHex := hex.EncodeToString(updatedRaw)
	txID := hex.EncodeToString(sum[:])

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(u.Raw, &obj); err != nil {
		return fmt.Errorf("decode unsigned tx json: %w", err)
	}
	rawHexJSON, err := json.Marshal(rawHex)
	if err != nil {
		return err
	}
	txIDJSON, err := json.Marshal(txID)
	if err != nil {
		return err
	}
	var rawObj map[string]json.RawMessage
	if err := json.Unmarshal(obj["raw_data"], &rawObj); err != nil {
		return fmt.Errorf("decode raw_data json: %w", err)
	}
	expirationJSON, err := json.Marshal(raw.Expiration)
	if err != nil {
		return err
	}
	rawObj["expiration"] = expirationJSON
	rawObjJSON, err := json.Marshal(rawObj)
	if err != nil {
		return fmt.Errorf("encode raw_data json: %w", err)
	}
	obj["raw_data"] = rawObjJSON
	obj["raw_data_hex"] = rawHexJSON
	obj["txID"] = txIDJSON
	updatedTx, err := json.Marshal(obj)
	if err != nil {
		return fmt.Errorf("encode unsigned tx json: %w", err)
	}

	u.RawDataHex = rawHex
	u.TxID = txID
	u.Raw = updatedTx
	return nil
}

func (c *NodeClient) post(ctx context.Context, path string, body any) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("%s: HTTP %d: %s", path, resp.StatusCode, string(respBytes))
	}
	return respBytes, nil
}

// bigEndianI64 returns the minimal big-endian byte representation of n
// (no leading zeros). Used to build TRC20 uint256 parameters.
func bigEndianI64(n int64) []byte {
	if n == 0 {
		return []byte{0}
	}
	var out []byte
	for n > 0 {
		out = append([]byte{byte(n & 0xff)}, out...)
		n >>= 8
	}
	return out
}

// AttachSignature merges a signature hex into the unsigned tx JSON,
// returning a JSON object suitable for /wallet/broadcasttransaction.
//
// We keep all of the node's original fields (raw_data, raw_data_hex,
// txID, visible flag) and add a "signature":["<hex>"] field.
func AttachSignature(unsigned []byte, sigHex string) ([]byte, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(unsigned, &obj); err != nil {
		return nil, err
	}
	sigJSON, err := json.Marshal([]string{sigHex})
	if err != nil {
		return nil, err
	}
	obj["signature"] = sigJSON
	return json.Marshal(obj)
}

// AttachPQSignature attaches a post-quantum pq_auth_sig entry to the
// unsigned tx JSON, returning a JSON object suitable for
// /wallet/broadcasttransaction.
//
// scheme is the PQScheme enum name (e.g. "ML_DSA_44"); pubKeyHex and sigHex
// are lowercase hex. java-tron accepts ECDSA `signature` and `pq_auth_sig`
// side by side, but for a PQ-only sender we attach only pq_auth_sig.
func AttachPQSignature(unsigned []byte, scheme, pubKeyHex, sigHex string) ([]byte, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(unsigned, &obj); err != nil {
		return nil, err
	}
	entry := map[string]string{
		"scheme":     scheme,
		"public_key": pubKeyHex,
		"signature":  sigHex,
	}
	arr, err := json.Marshal([]map[string]string{entry})
	if err != nil {
		return nil, err
	}
	obj["pq_auth_sig"] = arr
	return json.Marshal(obj)
}
