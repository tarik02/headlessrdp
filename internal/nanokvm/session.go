package nanokvm

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"

	"headlessdesk/internal/desktop"
	"headlessdesk/internal/inputcode"
)

type Config struct {
	Host               string
	Port               uint16
	Username           string
	Password           string
	InsecureSkipVerify bool
	Timeout            time.Duration
}

type Session struct {
	baseURL    *url.URL
	wsURL      *url.URL
	origin     string
	username   string
	password   string
	httpClient *http.Client
	timeout    time.Duration

	done      chan struct{}
	doneOnce  sync.Once
	closeOnce sync.Once

	mu           sync.RWMutex
	wsMu         sync.Mutex
	status       desktop.Status
	runErr       error
	token        string
	ws           *websocket.Conn
	keyModifiers byte
	keyUsages    []byte
	buttons      byte
}

const (
	defaultPort        uint16 = 80
	defaultTimeout            = 10 * time.Second
	cryptoJSPassphrase        = "nanokvm-sipeed-2024"
	maxJPEGBytes              = 32 * 1024 * 1024
)

func StartSession(cfg Config) (desktop.Session, error) {
	session, err := newSession(cfg)
	if err != nil {
		return nil, err
	}
	if err := session.login(); err != nil {
		return nil, err
	}
	session.setHIDMode()
	if err := session.connectWebSocket(); err != nil {
		return nil, err
	}
	if _, err := session.Screenshot(); err != nil {
		_ = session.Close()
		return nil, err
	}
	go session.readLoop()
	go session.heartbeatLoop()
	return session, nil
}

func newSession(cfg Config) (*Session, error) {
	cfg.Host = strings.TrimSpace(cfg.Host)
	cfg.Username = strings.TrimSpace(cfg.Username)
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.Host == "" {
		return nil, errors.New("nanokvm host is required")
	}
	if cfg.Username == "" {
		return nil, errors.New("nanokvm username is required")
	}
	if cfg.Password == "" {
		return nil, errors.New("nanokvm password is required")
	}

	baseURL, err := parseBaseURL(cfg.Host, cfg.Port)
	if err != nil {
		return nil, err
	}
	wsURL := *baseURL
	if wsURL.Scheme == "https" {
		wsURL.Scheme = "wss"
	} else {
		wsURL.Scheme = "ws"
	}
	wsURL.Path = "/api/ws"

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: cfg.InsecureSkipVerify}

	return &Session{
		baseURL:  baseURL,
		wsURL:    &wsURL,
		origin:   baseURL.String(),
		username: cfg.Username,
		password: cfg.Password,
		httpClient: &http.Client{
			Transport: transport,
		},
		timeout: cfg.Timeout,
		done:    make(chan struct{}),
		status: desktop.Status{
			Protocol:  "nanokvm",
			Connected: true,
			Active:    true,
			State:     "CONNECTING",
		},
	}, nil
}

func parseBaseURL(host string, port uint16) (*url.URL, error) {
	raw := host
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse nanokvm host: %w", err)
	}
	if parsed.Host == "" {
		return nil, errors.New("nanokvm host is required")
	}
	configuredPort := port != 0
	if port == 0 {
		if parsed.Scheme == "https" {
			port = 443
		} else {
			port = defaultPort
		}
	}
	if configuredPort || parsed.Port() == "" {
		parsed.Host = hostWithPort(parsed.Hostname(), port)
	}
	parsed.Path = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed, nil
}

func hostWithPort(host string, port uint16) string {
	return net.JoinHostPort(host, strconv.FormatUint(uint64(port), 10))
}

func (s *Session) login() error {
	encryptedPassword, err := encryptPassword(s.password)
	if err != nil {
		return err
	}
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := s.postJSON("/api/auth/login", map[string]string{
		"username": s.username,
		"password": encryptedPassword,
	}, &resp); err != nil {
		return err
	}
	if resp.Code != 0 {
		return fmt.Errorf("nanokvm login failed: %s", resp.Msg)
	}
	if resp.Data.Token == "" {
		return errors.New("nanokvm login response missing token")
	}

	s.mu.Lock()
	s.token = resp.Data.Token
	s.status.State = "AUTHENTICATED"
	s.mu.Unlock()
	return nil
}

func encryptPassword(password string) (string, error) {
	salt := make([]byte, 8)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate nanokvm password salt: %w", err)
	}
	key, iv := evpBytesToKey([]byte(cryptoJSPassphrase), salt, 32, aes.BlockSize)
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	padded := pkcs7Pad([]byte(password), aes.BlockSize)
	ciphertext := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ciphertext, padded)

	out := make([]byte, 0, 16+len(ciphertext))
	out = append(out, []byte("Salted__")...)
	out = append(out, salt...)
	out = append(out, ciphertext...)
	return url.QueryEscape(base64.StdEncoding.EncodeToString(out)), nil
}

func evpBytesToKey(password []byte, salt []byte, keyLen int, ivLen int) ([]byte, []byte) {
	var out []byte
	var previous []byte
	for len(out) < keyLen+ivLen {
		h := md5.New()
		h.Write(previous)
		h.Write(password)
		h.Write(salt)
		previous = h.Sum(nil)
		out = append(out, previous...)
	}
	return out[:keyLen], out[keyLen : keyLen+ivLen]
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	padding := blockSize - len(data)%blockSize
	out := make([]byte, len(data)+padding)
	copy(out, data)
	for i := len(data); i < len(out); i++ {
		out[i] = byte(padding)
	}
	return out
}

func (s *Session) setHIDMode() {
	var resp apiResponse
	if err := s.postJSON("/api/hid/mode", map[string]string{"mode": "absolute"}, &resp); err != nil {
		return
	}
}

func (s *Session) connectWebSocket() error {
	config, err := websocket.NewConfig(s.wsURL.String(), s.origin)
	if err != nil {
		return err
	}
	config.Header = http.Header{}
	config.Header.Set("Cookie", "nano-kvm-token="+s.currentToken())
	if s.wsURL.Scheme == "wss" {
		config.TlsConfig = s.httpClient.Transport.(*http.Transport).TLSClientConfig
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	ws, err := config.DialContext(ctx)
	if err != nil {
		return fmt.Errorf("connect nanokvm websocket: %w", err)
	}
	ws.PayloadType = websocket.BinaryFrame

	s.mu.Lock()
	s.ws = ws
	s.status.State = "CONNECTED"
	s.mu.Unlock()
	return nil
}

func (s *Session) Screenshot() (image.Image, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()

	req, err := s.newRequest(ctx, http.MethodGet, "/api/stream/mjpeg", nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		s.setErr(err)
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := fmt.Errorf("nanokvm screenshot HTTP %d", resp.StatusCode)
		s.setErr(err)
		return nil, err
	}

	frame, err := readFirstJPEG(resp.Body)
	if err != nil {
		s.setErr(err)
		return nil, err
	}
	img, err := jpeg.Decode(bytes.NewReader(frame))
	if err != nil {
		s.setErr(err)
		return nil, fmt.Errorf("decode nanokvm screenshot jpeg: %w", err)
	}
	s.setFrameSize(img.Bounds().Dx(), img.Bounds().Dy())
	return img, nil
}

func readFirstJPEG(reader io.Reader) ([]byte, error) {
	buf := make([]byte, 4096)
	var frame []byte
	started := false
	var previous byte
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			for _, b := range buf[:n] {
				if !started {
					if previous == 0xff && b == 0xd8 {
						started = true
						frame = append(frame, 0xff, 0xd8)
					}
					previous = b
					continue
				}
				frame = append(frame, b)
				if len(frame) > maxJPEGBytes {
					return nil, errors.New("nanokvm jpeg frame is too large")
				}
				if previous == 0xff && b == 0xd9 {
					return frame, nil
				}
				previous = b
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, errors.New("nanokvm mjpeg stream ended before first frame")
			}
			return nil, err
		}
	}
}

func (s *Session) SendKey(name inputcode.KeyName, down bool, repeat bool) error {
	modifier, usage, err := hidKey(name.String())
	if err != nil {
		return err
	}
	return s.sendKeyboard(modifier, usage, down)
}

func (s *Session) SendKeyScancode(scancode inputcode.Scancode, down bool, repeat bool) error {
	return fmt.Errorf("nanokvm does not support scancode input: %d", scancode)
}

func (s *Session) TypeText(text string) error {
	if text == "" {
		return errors.New("text is required")
	}
	var resp apiResponse
	if err := s.postJSON("/api/hid/paste", map[string]string{
		"content": text,
		"langue":  "en",
	}, &resp); err != nil {
		return err
	}
	if resp.Code != 0 {
		return fmt.Errorf("nanokvm paste failed: %s", resp.Msg)
	}
	return nil
}

func (s *Session) MoveMouse(x float64, y float64) error {
	mouseX, mouseY, err := s.absolutePoint(x, y)
	if err != nil {
		return err
	}
	return s.sendMouse(mouseX, mouseY, 0)
}

func (s *Session) SendMouseButton(button inputcode.MouseButtonName, x float64, y float64, down bool) error {
	mask, err := mouseButtonMask(button.String())
	if err != nil {
		return err
	}
	mouseX, mouseY, err := s.absolutePoint(x, y)
	if err != nil {
		return err
	}

	s.wsMu.Lock()
	if down {
		s.buttons |= mask
	} else {
		s.buttons &^= mask
	}
	buttons := s.buttons
	s.wsMu.Unlock()
	return s.sendMouseWithButtons(mouseX, mouseY, 0, buttons)
}

func (s *Session) SendMouseWheel(x float64, y float64, delta int, horizontal bool) error {
	if horizontal {
		return errors.New("nanokvm horizontal wheel input is not supported")
	}
	if delta == 0 {
		return errors.New("wheel delta must be non-zero")
	}
	mouseX, mouseY, err := s.absolutePoint(x, y)
	if err != nil {
		return err
	}
	return s.sendMouse(mouseX, mouseY, clampWheel(delta))
}

func (s *Session) MapInputPoint(outputWidth int, outputHeight int, x float64, y float64) (float64, float64, error) {
	if outputWidth <= 0 || outputHeight <= 0 {
		return 0, 0, errors.New("output dimensions are unavailable")
	}
	status := s.Status()
	if status.Width <= 0 || status.Height <= 0 {
		return 0, 0, errors.New("nanokvm dimensions are unavailable")
	}
	return x * float64(status.Width) / float64(outputWidth), y * float64(status.Height) / float64(outputHeight), nil
}

func (s *Session) sendKeyboard(modifier byte, usage byte, down bool) error {
	s.wsMu.Lock()
	defer s.wsMu.Unlock()

	if modifier != 0 {
		if down {
			s.keyModifiers |= modifier
		} else {
			s.keyModifiers &^= modifier
		}
	} else if down {
		if !containsUsage(s.keyUsages, usage) {
			if len(s.keyUsages) >= 6 {
				return errors.New("nanokvm keyboard report supports at most six simultaneous keys")
			}
			s.keyUsages = append(s.keyUsages, usage)
		}
	} else {
		s.keyUsages = removeUsage(s.keyUsages, usage)
	}

	report := []byte{1, s.keyModifiers, 0, 0, 0, 0, 0, 0, 0}
	copy(report[3:], s.keyUsages)
	return s.writeLocked(report)
}

func containsUsage(usages []byte, usage byte) bool {
	for _, existing := range usages {
		if existing == usage {
			return true
		}
	}
	return false
}

func removeUsage(usages []byte, usage byte) []byte {
	for i, existing := range usages {
		if existing == usage {
			return append(usages[:i], usages[i+1:]...)
		}
	}
	return usages
}

func (s *Session) sendMouse(x uint16, y uint16, wheel int8) error {
	s.wsMu.Lock()
	buttons := s.buttons
	s.wsMu.Unlock()
	return s.sendMouseWithButtons(x, y, wheel, buttons)
}

func (s *Session) sendMouseWithButtons(x uint16, y uint16, wheel int8, buttons byte) error {
	report := []byte{2, buttons, byte(x), byte(x >> 8), byte(y), byte(y >> 8), byte(wheel)}
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	return s.writeLocked(report)
}

func (s *Session) writeLocked(report []byte) error {
	ws := s.currentWebSocket()
	if ws == nil {
		return errors.New("nanokvm websocket is not connected")
	}
	ws.PayloadType = websocket.BinaryFrame
	if _, err := ws.Write(report); err != nil {
		s.setErr(err)
		s.closeDone()
		return err
	}
	return nil
}

func (s *Session) absolutePoint(x float64, y float64) (uint16, uint16, error) {
	roundedX, err := desktop.RoundCoordinate(x)
	if err != nil {
		return 0, 0, err
	}
	roundedY, err := desktop.RoundCoordinate(y)
	if err != nil {
		return 0, 0, err
	}
	status := s.Status()
	if status.Width <= 0 || status.Height <= 0 {
		return 0, 0, errors.New("nanokvm dimensions are unavailable")
	}
	if roundedX < 0 || roundedY < 0 || roundedX >= status.Width || roundedY >= status.Height {
		return 0, 0, fmt.Errorf("pointer position out of range: %d,%d", roundedX, roundedY)
	}
	return uint16((roundedX*32767)/status.Width + 1), uint16((roundedY*32767)/status.Height + 1), nil
}

func clampWheel(delta int) int8 {
	if delta > 127 {
		return 127
	}
	if delta < -127 {
		return -127
	}
	return int8(delta)
}

func (s *Session) readLoop() {
	buf := make([]byte, 1024)
	for {
		ws := s.currentWebSocket()
		if ws == nil {
			return
		}
		if _, err := ws.Read(buf); err != nil {
			select {
			case <-s.done:
				return
			default:
				s.setErr(err)
				s.setDisconnected()
				s.closeDone()
				return
			}
		}
	}
}

func (s *Session) heartbeatLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.wsMu.Lock()
			err := s.writeLocked([]byte{0})
			s.wsMu.Unlock()
			if err != nil {
				return
			}
		case <-s.done:
			return
		}
	}
}

func (s *Session) Status() desktop.Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	status := s.status
	if s.runErr != nil {
		status.Error = s.runErr.Error()
	}
	return status
}

func (s *Session) Err() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.runErr
}

func (s *Session) Done() <-chan struct{} {
	return s.done
}

func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		s.closeDone()
		s.mu.RLock()
		ws := s.ws
		s.mu.RUnlock()
		if ws != nil {
			_ = ws.Close()
		}
		s.setDisconnected()
	})
	return s.Err()
}

func (s *Session) setFrameSize(width int, height int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.Width = width
	s.status.Height = height
	s.status.Ready = true
	s.status.State = "ACTIVE"
	s.status.Error = ""
	s.runErr = nil
}

func (s *Session) setErr(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runErr = err
	s.status.Error = err.Error()
}

func (s *Session) setDisconnected() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.Connected = false
	s.status.Active = false
	if s.runErr == nil {
		s.status.State = "CLOSED"
	} else {
		s.status.State = "ERROR"
	}
}

func (s *Session) closeDone() {
	s.doneOnce.Do(func() {
		close(s.done)
	})
}

func (s *Session) currentToken() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.token
}

func (s *Session) currentWebSocket() *websocket.Conn {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ws
}

func (s *Session) postJSON(path string, body any, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	req, err := s.newRequest(ctx, http.MethodPost, path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("nanokvm %s HTTP %d", path, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode nanokvm %s response: %w", path, err)
	}
	return nil
}

func (s *Session) newRequest(ctx context.Context, method string, path string, body io.Reader) (*http.Request, error) {
	endpoint := *s.baseURL
	endpoint.Path = path
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return nil, err
	}
	if token := s.currentToken(); token != "" {
		req.Header.Set("Cookie", "nano-kvm-token="+token)
	}
	return req, nil
}

type apiResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

func hidKey(name string) (byte, byte, error) {
	switch name {
	case "KEY_LEFTCTRL":
		return 1 << 0, 0, nil
	case "KEY_LEFTSHIFT":
		return 1 << 1, 0, nil
	case "KEY_LEFTALT":
		return 1 << 2, 0, nil
	case "KEY_LEFTMETA":
		return 1 << 3, 0, nil
	case "KEY_RIGHTCTRL":
		return 1 << 4, 0, nil
	case "KEY_RIGHTSHIFT":
		return 1 << 5, 0, nil
	case "KEY_RIGHTALT":
		return 1 << 6, 0, nil
	case "KEY_RIGHTMETA":
		return 1 << 7, 0, nil
	}
	if usage, ok := hidUsages[name]; ok {
		return 0, usage, nil
	}
	return 0, 0, fmt.Errorf("unsupported nanokvm key: %s", name)
}

func mouseButtonMask(name string) (byte, error) {
	switch name {
	case "BTN_LEFT":
		return 1, nil
	case "BTN_RIGHT":
		return 2, nil
	case "BTN_MIDDLE":
		return 4, nil
	case "BTN_SIDE", "BTN_BACK":
		return 8, nil
	case "BTN_EXTRA", "BTN_FORWARD":
		return 16, nil
	default:
		return 0, fmt.Errorf("unsupported nanokvm mouse button: %s", name)
	}
}

var hidUsages = map[string]byte{
	"KEY_A":          0x04,
	"KEY_B":          0x05,
	"KEY_C":          0x06,
	"KEY_D":          0x07,
	"KEY_E":          0x08,
	"KEY_F":          0x09,
	"KEY_G":          0x0a,
	"KEY_H":          0x0b,
	"KEY_I":          0x0c,
	"KEY_J":          0x0d,
	"KEY_K":          0x0e,
	"KEY_L":          0x0f,
	"KEY_M":          0x10,
	"KEY_N":          0x11,
	"KEY_O":          0x12,
	"KEY_P":          0x13,
	"KEY_Q":          0x14,
	"KEY_R":          0x15,
	"KEY_S":          0x16,
	"KEY_T":          0x17,
	"KEY_U":          0x18,
	"KEY_V":          0x19,
	"KEY_W":          0x1a,
	"KEY_X":          0x1b,
	"KEY_Y":          0x1c,
	"KEY_Z":          0x1d,
	"KEY_1":          0x1e,
	"KEY_2":          0x1f,
	"KEY_3":          0x20,
	"KEY_4":          0x21,
	"KEY_5":          0x22,
	"KEY_6":          0x23,
	"KEY_7":          0x24,
	"KEY_8":          0x25,
	"KEY_9":          0x26,
	"KEY_0":          0x27,
	"KEY_ENTER":      0x28,
	"KEY_ESC":        0x29,
	"KEY_BACKSPACE":  0x2a,
	"KEY_TAB":        0x2b,
	"KEY_SPACE":      0x2c,
	"KEY_MINUS":      0x2d,
	"KEY_EQUAL":      0x2e,
	"KEY_LEFTBRACE":  0x2f,
	"KEY_RIGHTBRACE": 0x30,
	"KEY_BACKSLASH":  0x31,
	"KEY_SEMICOLON":  0x33,
	"KEY_APOSTROPHE": 0x34,
	"KEY_GRAVE":      0x35,
	"KEY_COMMA":      0x36,
	"KEY_DOT":        0x37,
	"KEY_SLASH":      0x38,
	"KEY_CAPSLOCK":   0x39,
	"KEY_F1":         0x3a,
	"KEY_F2":         0x3b,
	"KEY_F3":         0x3c,
	"KEY_F4":         0x3d,
	"KEY_F5":         0x3e,
	"KEY_F6":         0x3f,
	"KEY_F7":         0x40,
	"KEY_F8":         0x41,
	"KEY_F9":         0x42,
	"KEY_F10":        0x43,
	"KEY_F11":        0x44,
	"KEY_F12":        0x45,
	"KEY_SYSRQ":      0x46,
	"KEY_SCROLLLOCK": 0x47,
	"KEY_PAUSE":      0x48,
	"KEY_INSERT":     0x49,
	"KEY_HOME":       0x4a,
	"KEY_PAGEUP":     0x4b,
	"KEY_DELETE":     0x4c,
	"KEY_END":        0x4d,
	"KEY_PAGEDOWN":   0x4e,
	"KEY_RIGHT":      0x4f,
	"KEY_LEFT":       0x50,
	"KEY_DOWN":       0x51,
	"KEY_UP":         0x52,
}
