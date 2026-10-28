package tapo

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"sync"
	"time"
	"unicode/utf8"

	"etapo/internal/models"
)

// Driver implements collector.DeviceDriver for TP-Link Tapo P115 plugs via the KLAP protocol.
type Driver struct {
	mu         sync.Mutex
	ip         string
	username   string
	password   string
	httpClient *http.Client

	// KLAP session keys
	localSeed   []byte
	serverSeed  []byte
	key         []byte
	iv          []byte
	sig         []byte
	seq         int32
	sessionID   string
	isConnected bool
	lastLogin   time.Time
}

// NewDriver creates a new Tapo KLAP protocol driver.
func NewDriver(ip, username, password string) *Driver {
	return &Driver{
		ip:       ip,
		username: username,
		password: password,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// GetInfo retrieves device status (nickname, model, on/off, mac) from the Tapo socket.
func (d *Driver) GetInfo(ctx context.Context) (*models.DeviceInfo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	res, err := d.executeMethod(ctx, "get_device_info", nil)
	if err != nil {
		return nil, fmt.Errorf("tapo get_device_info failed: %w", err)
	}

	info := &models.DeviceInfo{}
	if nickname, ok := res["nickname"].(string); ok {
		info.Nickname = decodeNickname(nickname)
	}
	if model, ok := res["model"].(string); ok {
		info.Model = model
	}
	if mac, ok := res["mac"].(string); ok {
		info.MAC = mac
	}
	if deviceOn, ok := res["device_on"].(bool); ok {
		info.IsOn = deviceOn
	}
	if rssi, ok := res["rssi"].(float64); ok {
		info.RSSI = int(rssi)
	}

	return info, nil
}

// decodeNickname undoes the base64 encoding the device applies to its nickname.
// Values that are not valid base64 UTF-8 are returned unchanged.
func decodeNickname(nickname string) string {
	decoded, err := base64.StdEncoding.DecodeString(nickname)
	if err != nil || !utf8.Valid(decoded) {
		return nickname
	}
	return string(decoded)
}

// GetEnergyUsage retrieves real-time energy telemetry (Watts, Voltage, Current, kWh).
func (d *Driver) GetEnergyUsage(ctx context.Context) (*models.EnergyUsage, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	res, err := d.executeMethod(ctx, "get_energy_usage", nil)
	if err != nil {
		return nil, fmt.Errorf("tapo get_energy_usage failed: %w", err)
	}

	return parseEnergyUsage(res), nil
}

func parseEnergyUsage(res map[string]interface{}) *models.EnergyUsage {
	usage := &models.EnergyUsage{}

	// Tapo API units: current_power in mW, voltage in mV, current in mA, energy in Wh
	if p, ok := res["current_power"].(float64); ok {
		usage.PowerWatts = p / 1000.0
	}

	if v, ok := res["voltage"].(float64); ok {
		usage.VoltageV = v / 1000.0
	} else {
		usage.VoltageV = 230.0 // Default mains estimate if unexposed
	}

	if c, ok := res["current"].(float64); ok {
		usage.CurrentMA = c
	} else if usage.VoltageV > 0 {
		usage.CurrentMA = (usage.PowerWatts / usage.VoltageV) * 1000.0
	}

	if today, ok := res["today_energy"].(float64); ok {
		usage.EnergyTodayKWh = today / 1000.0
	}

	if month, ok := res["month_energy"].(float64); ok {
		usage.EnergyMonthKWh = month / 1000.0
	}

	return usage
}

// SetPower turns the socket relay on or off.
func (d *Driver) SetPower(ctx context.Context, on bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	params := map[string]interface{}{
		"device_on": on,
	}
	_, err := d.executeMethod(ctx, "set_device_info", params)
	return err
}

// executeMethod ensures an active KLAP session and issues a request.
func (d *Driver) executeMethod(ctx context.Context, method string, params map[string]interface{}) (map[string]interface{}, error) {
	if !d.isConnected || time.Since(d.lastLogin) > 5*time.Minute {
		if err := d.login(ctx); err != nil {
			return nil, err
		}
	}

	res, err := d.sendEncrypted(ctx, method, params)
	if err != nil {
		// Reset connection and retry once
		d.isConnected = false
		if retryErr := d.login(ctx); retryErr != nil {
			return nil, fmt.Errorf("re-auth failed after request error: %v (initial: %w)", retryErr, err)
		}
		return d.sendEncrypted(ctx, method, params)
	}

	return res, nil
}

// login performs the 2-step KLAP handshake.
func (d *Driver) login(ctx context.Context) error {
	// Generate random 16-byte local seed
	d.localSeed = make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, d.localSeed); err != nil {
		return err
	}

	authHash := sha256Hash(concat(sha1Hash([]byte(d.username)), sha1Hash([]byte(d.password))))

	// Step 1: Handshake1
	url1 := fmt.Sprintf("http://%s/app/handshake1", d.ip)
	req1, err := http.NewRequestWithContext(ctx, "POST", url1, bytes.NewReader(d.localSeed))
	if err != nil {
		return err
	}
	resp1, err := d.httpClient.Do(req1)
	if err != nil {
		return fmt.Errorf("handshake1 failed: %w", err)
	}
	defer resp1.Body.Close()

	if resp1.StatusCode != http.StatusOK {
		return fmt.Errorf("handshake1 returned HTTP %d", resp1.StatusCode)
	}

	respBody1, err := ioutil.ReadAll(resp1.Body)
	if err != nil || len(respBody1) != 48 {
		return errors.New("invalid handshake1 response")
	}

	remoteSeed := respBody1[:16]
	serverHash := respBody1[16:]
	if !bytes.Equal(sha256Hash(concat(d.localSeed, remoteSeed, authHash)), serverHash) {
		return errors.New("handshake1 credentials mismatch: check Tapo account email and password")
	}
	d.serverSeed = remoteSeed

	// Extract session cookie if present
	for _, cookie := range resp1.Cookies() {
		if cookie.Name == "TP_SESSIONID" {
			d.sessionID = cookie.Value
			break
		}
	}

	// Compute session keys: key, iv, sig, and starting sequence number
	d.key = sha256Hash(concat([]byte("lsk"), d.localSeed, d.serverSeed, authHash))[:16]
	ivSeed := sha256Hash(concat([]byte("iv"), d.localSeed, d.serverSeed, authHash))
	d.iv = ivSeed[:12]
	d.seq = int32(binary.BigEndian.Uint32(ivSeed[28:]))
	d.sig = sha256Hash(concat([]byte("ldk"), d.localSeed, d.serverSeed, authHash))[:28]

	// Step 2: Handshake2
	handshake2Payload := sha256Hash(concat(d.serverSeed, d.localSeed, authHash))

	url2 := fmt.Sprintf("http://%s/app/handshake2", d.ip)
	req2, err := http.NewRequestWithContext(ctx, "POST", url2, bytes.NewReader(handshake2Payload))
	if err != nil {
		return err
	}
	if d.sessionID != "" {
		req2.AddCookie(&http.Cookie{Name: "TP_SESSIONID", Value: d.sessionID})
	}

	resp2, err := d.httpClient.Do(req2)
	if err != nil {
		return fmt.Errorf("handshake2 failed: %w", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		return fmt.Errorf("handshake2 returned HTTP %d", resp2.StatusCode)
	}

	d.isConnected = true
	d.lastLogin = time.Now()
	return nil
}

func (d *Driver) sendEncrypted(ctx context.Context, method string, params map[string]interface{}) (map[string]interface{}, error) {
	d.seq++

	if params == nil {
		params = map[string]interface{}{}
	}

	payload := map[string]interface{}{
		"method":       method,
		"params":       params,
		"request_time": time.Now().Unix(),
		"terminalUUID": "etapo-pi-client",
	}

	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	iv := concat(d.iv, int32ToBytes(d.seq))

	// Pad and Encrypt with AES-CBC
	encrypted, err := encryptAES(jsonBytes, d.key, iv)
	if err != nil {
		return nil, err
	}

	// Build KLAP signature
	reqSig := sha256Hash(concat(d.sig, int32ToBytes(d.seq), encrypted))

	body := concat(reqSig, encrypted)

	url := fmt.Sprintf("http://%s/app/request?seq=%d", d.ip, d.seq)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if d.sessionID != "" {
		req.AddCookie(&http.Cookie{Name: "TP_SESSIONID", Value: d.sessionID})
	}

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("request returned status %d", resp.StatusCode)
	}

	respBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if len(respBytes) < 32 {
		return nil, errors.New("response payload too short")
	}

	// Skip 32-byte signature
	encryptedResp := respBytes[32:]
	decrypted, err := decryptAES(encryptedResp, d.key, iv)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt response: %w", err)
	}

	var jsonRes struct {
		ErrorCode int                    `json:"error_code"`
		Result    map[string]interface{} `json:"result"`
	}
	if err := json.Unmarshal(decrypted, &jsonRes); err != nil {
		return nil, fmt.Errorf("failed to unmarshal tapo response: %w (%s)", err, hex.EncodeToString(decrypted))
	}

	if jsonRes.ErrorCode != 0 {
		return nil, fmt.Errorf("tapo API error code: %d", jsonRes.ErrorCode)
	}

	return jsonRes.Result, nil
}

func sha1Hash(data []byte) []byte {
	h := sha1.Sum(data)
	return h[:]
}

func sha256Hash(data []byte) []byte {
	h := sha256.Sum256(data)
	return h[:]
}

func int32ToBytes(n int32) []byte {
	return []byte{
		byte(n >> 24),
		byte(n >> 16),
		byte(n >> 8),
		byte(n),
	}
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func encryptAES(plain []byte, key, iv []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	// PKCS7 padding
	padding := aes.BlockSize - len(plain)%aes.BlockSize
	padtext := bytes.Repeat([]byte{byte(padding)}, padding)
	plain = append(plain, padtext...)

	cipherText := make([]byte, len(plain))
	mode := cipher.NewCBCEncrypter(block, iv)
	mode.CryptBlocks(cipherText, plain)
	return cipherText, nil
}

func decryptAES(cipherText []byte, key, iv []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	if len(cipherText)%aes.BlockSize != 0 {
		return nil, errors.New("cipherText is not a multiple of the block size")
	}

	mode := cipher.NewCBCDecrypter(block, iv)
	plain := make([]byte, len(cipherText))
	mode.CryptBlocks(plain, cipherText)

	// PKCS7 unpadding
	if len(plain) == 0 {
		return nil, errors.New("empty plain text")
	}
	padding := int(plain[len(plain)-1])
	if padding > len(plain) || padding == 0 {
		return plain, nil
	}
	return plain[:len(plain)-padding], nil
}
