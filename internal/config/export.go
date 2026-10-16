package config

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"
)

// Export file constants.
const (
	ExportFormat        = "sockswebui-config"
	ExportFormatVersion = 1
	ExportModeEncrypted = "encrypted"
	ExportModeNoSecrets = "no-secrets"

	ExportKDFAlg    = "argon2id"
	ExportKDFT      = 3
	ExportKDFMKiB   = 16384
	ExportKDFP      = 2
	ExportCipherAlg = "AES-256-GCM"

	exportSaltLen  = 16
	exportNonceLen = 12
	exportKeyLen   = 32
)

// ErrDecrypt is the single error for a wrong passphrase or a tampered encrypted file. Its
// message is identical in every case so it reveals nothing about which check failed.
var ErrDecrypt = errors.New("could not decrypt: wrong passphrase or damaged file")

// ErrExportFormat wraps every structural rejection of an export file (unknown format, mode,
// version, fields, bad salt/nonce, disallowed KDF parameters, passphrase with no-secrets).
var ErrExportFormat = errors.New("invalid config file")

// KDFFunc derives a 32-byte key. Production passes argon2id wrapped in the API's hashing
// semaphore; this package never imports argon2.
type KDFFunc func(passphrase, salt []byte, t, mKiB uint32, p uint8) ([]byte, error)

// ExportFile is the outer JSON object of an export file. Payload is a JSON string (base64
// ciphertext) for encrypted files and a JSON object for no-secrets files.
type ExportFile struct {
	Format        string          `json:"format"`
	FormatVersion int             `json:"format_version"`
	AppVersion    string          `json:"app_version"` // informational, not authenticated
	CreatedAt     string          `json:"created_at"`  // informational, not authenticated
	Mode          string          `json:"mode"`
	KDF           *KDFParams      `json:"kdf,omitempty"`
	Cipher        *CipherParams   `json:"cipher,omitempty"`
	Payload       json.RawMessage `json:"payload"`
}

// KDFParams is the file's "kdf" object.
type KDFParams struct {
	Alg  string `json:"alg"`
	T    uint32 `json:"t"`
	MKiB uint32 `json:"m_kib"`
	P    uint8  `json:"p"`
	Salt string `json:"salt"` // std base64, exactly 16 bytes
}

// CipherParams is the file's "cipher" object.
type CipherParams struct {
	Alg   string `json:"alg"`
	Nonce string `json:"nonce"` // std base64, exactly 12 bytes
}

// ExportPayload is what an export file carries: all endpoints and slots. SecretsOmitted on each
// object is written to and read from the file's "omitted" list.
type ExportPayload struct {
	Endpoints []Endpoint `json:"endpoints"`
	Slots     []Slot     `json:"slots"`
}

func formatErr(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrExportFormat, fmt.Sprintf(format, args...))
}

func newExportFile(mode string, now time.Time, appVersion string) ExportFile {
	return ExportFile{
		Format:        ExportFormat,
		FormatVersion: ExportFormatVersion,
		AppVersion:    appVersion,
		CreatedAt:     now.UTC().Format(time.RFC3339),
		Mode:          mode,
	}
}

// SealEncrypted builds an encrypted export file: salt and nonce are read from r (production:
// the system CSPRNG reader), the key comes from kdf, and the payload is sealed with AES-256-GCM using
// the header AAD.
func SealEncrypted(r io.Reader, payload ExportPayload, passphrase []byte, kdf KDFFunc, now time.Time, appVersion string) ([]byte, error) {
	plain, err := encodePayload(payload)
	if err != nil {
		return nil, err
	}
	return sealPlaintext(r, plain, passphrase, kdf, now, appVersion)
}

func sealPlaintext(r io.Reader, plain, passphrase []byte, kdf KDFFunc, now time.Time, appVersion string) ([]byte, error) {
	salt := make([]byte, exportSaltLen)
	nonce := make([]byte, exportNonceLen)
	if _, err := io.ReadFull(r, salt); err != nil {
		return nil, fmt.Errorf("export salt: %w", err)
	}
	if _, err := io.ReadFull(r, nonce); err != nil {
		return nil, fmt.Errorf("export nonce: %w", err)
	}
	f := newExportFile(ExportModeEncrypted, now, appVersion)
	f.KDF = &KDFParams{Alg: ExportKDFAlg, T: ExportKDFT, MKiB: ExportKDFMKiB, P: ExportKDFP,
		Salt: base64.StdEncoding.EncodeToString(salt)}
	f.Cipher = &CipherParams{Alg: ExportCipherAlg, Nonce: base64.StdEncoding.EncodeToString(nonce)}
	gcm, err := exportGCM(passphrase, salt, kdf)
	if err != nil {
		return nil, err
	}
	ct := gcm.Seal(nil, nonce, plain, exportAAD(f))
	f.Payload, err = json.Marshal(base64.StdEncoding.EncodeToString(ct))
	if err != nil {
		return nil, err
	}
	return json.Marshal(f)
}

// SealNoSecrets builds a no-secrets export file: every secret is emptied and recorded in the
// object's "omitted" list (StripSecrets). The file is not authenticated.
func SealNoSecrets(payload ExportPayload, now time.Time, appVersion string) ([]byte, error) {
	plain, err := encodePayload(StripSecrets(payload))
	if err != nil {
		return nil, err
	}
	f := newExportFile(ExportModeNoSecrets, now, appVersion)
	f.Payload = plain
	return json.Marshal(f)
}

// OpenExport decodes, checks and (for encrypted files) decrypts an export file. passphrase must
// be empty for no-secrets files. KDF parameters are checked before kdf is called. A wrong
// passphrase or tampered ciphertext/AAD returns ErrDecrypt; structural problems wrap
// ErrExportFormat; a bad imported endpoint id returns ValidationErrors. It returns the payload
// and the file's mode.
func OpenExport(raw, passphrase []byte, kdf KDFFunc) (ExportPayload, string, error) {
	f, err := decodeExportFile(raw)
	if err != nil {
		return ExportPayload{}, "", err
	}
	if f.Mode == ExportModeNoSecrets {
		if len(passphrase) > 0 {
			return ExportPayload{}, "", formatErr("a passphrase must not be sent with a no-secrets file")
		}
		p, err := decodePayload(f.Payload)
		return p, f.Mode, err
	}
	salt, _ := base64.StdEncoding.DecodeString(f.KDF.Salt) // length checked by decodeExportFile
	key, err := kdf(passphrase, salt, f.KDF.T, f.KDF.MKiB, f.KDF.P)
	if err != nil {
		return ExportPayload{}, "", fmt.Errorf("derive export key: %w", err)
	}
	plain, err := decryptPayload(f, key)
	clear(key)
	if err != nil {
		return ExportPayload{}, "", err
	}
	p, err := decodePayload(plain)
	return p, f.Mode, err
}

// decryptPayload opens the ciphertext in f with key, authenticating exportAAD(f).
func decryptPayload(f ExportFile, key []byte) ([]byte, error) {
	var ct64 string
	if err := json.Unmarshal(f.Payload, &ct64); err != nil {
		return nil, ErrDecrypt
	}
	ct, err1 := base64.StdEncoding.DecodeString(ct64)
	nonce, err2 := base64.StdEncoding.DecodeString(f.Cipher.Nonce)
	if err1 != nil || err2 != nil || len(nonce) != exportNonceLen || len(key) != exportKeyLen {
		return nil, ErrDecrypt
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrDecrypt
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrDecrypt
	}
	plain, err := gcm.Open(nil, nonce, ct, exportAAD(f))
	if err != nil {
		return nil, ErrDecrypt
	}
	return plain, nil
}

func exportGCM(passphrase, salt []byte, kdf KDFFunc) (cipher.AEAD, error) {
	key, err := kdf(passphrase, salt, ExportKDFT, ExportKDFMKiB, ExportKDFP)
	if err != nil {
		return nil, fmt.Errorf("derive export key: %w", err)
	}
	defer clear(key)
	if len(key) != exportKeyLen {
		return nil, fmt.Errorf("derive export key: got %d bytes, want %d", len(key), exportKeyLen)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// exportAAD returns the additional authenticated data for f: NUL-separated
// format, version, mode, KDF alg/t/m_kib/p/salt, cipher alg/nonce, exactly as in the file.
func exportAAD(f ExportFile) []byte {
	var kd KDFParams
	var cp CipherParams
	if f.KDF != nil {
		kd = *f.KDF
	}
	if f.Cipher != nil {
		cp = *f.Cipher
	}
	parts := []string{
		ExportFormat, strconv.Itoa(f.FormatVersion), f.Mode,
		kd.Alg, strconv.FormatUint(uint64(kd.T), 10), strconv.FormatUint(uint64(kd.MKiB), 10),
		strconv.FormatUint(uint64(kd.P), 10), kd.Salt, cp.Alg, cp.Nonce,
	}
	var b bytes.Buffer
	for i, p := range parts {
		if i > 0 {
			b.WriteByte(0)
		}
		b.WriteString(p)
	}
	return b.Bytes()
}

// strictUnmarshal decodes one JSON value into v, rejecting unknown fields and trailing data.
func strictUnmarshal(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("trailing data after JSON value")
	}
	return nil
}

// decodeExportFile decodes the outer object and checks every header rule
// except the passphrase rule, without deriving any key.
func decodeExportFile(raw []byte) (ExportFile, error) {
	var f ExportFile
	if err := strictUnmarshal(raw, &f); err != nil {
		return ExportFile{}, formatErr("%v", err)
	}
	if f.Format != ExportFormat {
		return ExportFile{}, formatErr("unknown format")
	}
	if f.FormatVersion != ExportFormatVersion {
		return ExportFile{}, formatErr("unsupported format_version %d", f.FormatVersion)
	}
	payload := bytes.TrimSpace(f.Payload)
	switch f.Mode {
	case ExportModeNoSecrets:
		if f.KDF != nil || f.Cipher != nil {
			return ExportFile{}, formatErr("kdf and cipher are not allowed in a no-secrets file")
		}
		if len(payload) == 0 || payload[0] != '{' {
			return ExportFile{}, formatErr("payload must be an object in a no-secrets file")
		}
	case ExportModeEncrypted:
		if err := checkEncryptedHeader(f); err != nil {
			return ExportFile{}, err
		}
		if len(payload) == 0 || payload[0] != '"' {
			return ExportFile{}, formatErr("payload must be a string in an encrypted file")
		}
	default:
		return ExportFile{}, formatErr("unknown mode")
	}
	return f, nil
}

func checkEncryptedHeader(f ExportFile) error {
	if f.KDF == nil || f.Cipher == nil {
		return formatErr("kdf and cipher are required in an encrypted file")
	}
	k := f.KDF
	if k.Alg != ExportKDFAlg || k.T != ExportKDFT || k.MKiB != ExportKDFMKiB || k.P != ExportKDFP {
		return formatErr("kdf must be argon2id with t=%d, m_kib=%d, p=%d", ExportKDFT, ExportKDFMKiB, ExportKDFP)
	}
	if b, err := base64.StdEncoding.DecodeString(k.Salt); err != nil || len(b) != exportSaltLen {
		return formatErr("kdf.salt must be %d bytes of standard base64", exportSaltLen)
	}
	if f.Cipher.Alg != ExportCipherAlg {
		return formatErr("cipher.alg must be %s", ExportCipherAlg)
	}
	if b, err := base64.StdEncoding.DecodeString(f.Cipher.Nonce); err != nil || len(b) != exportNonceLen {
		return formatErr("cipher.nonce must be %d bytes of standard base64", exportNonceLen)
	}
	return nil
}
