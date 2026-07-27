package kafkaclient

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl/scram"
)

const (
	SecurityProtocolPlaintext     = "PLAINTEXT"
	SecurityProtocolTLS           = "SSL"
	SecurityProtocolSASLPlaintext = "SASL_PLAINTEXT"
	SecurityProtocolSASLTLS       = "SASL_SSL"
	SASLMechanismSCRAMSHA512      = "SCRAM-SHA-512"
)

var ErrInvalidConfig = errors.New("invalid Kafka client config")

type Config struct {
	Brokers          []string
	ClientID         string
	SecurityProtocol string
	SASLMechanism    string
	SASLUsername     string
	SASLPassword     string
	TLSCAFile        string
	TLSServerName    string
}

// FromEnv loads an optional Secret-mounted properties file and then applies explicit environment overrides.
func FromEnv(getenv func(string) string, defaultBrokers []string, defaultClientID string) (Config, error) {
	if getenv == nil {
		return Config{}, fmt.Errorf("%w: getenv function is required", ErrInvalidConfig)
	}
	var cfg Config
	propertiesFile := strings.TrimSpace(getenv("AUCTION_KAFKA_CLIENT_PROPERTIES_FILE"))
	if propertiesFile != "" {
		loaded, err := LoadProperties(propertiesFile)
		if err != nil {
			return Config{}, err
		}
		cfg = loaded
	}
	if value := strings.TrimSpace(getenv("AUCTION_KAFKA_BROKERS")); value != "" {
		cfg.Brokers = splitCSV(value)
	}
	if value := strings.TrimSpace(getenv("AUCTION_KAFKA_CLIENT_ID")); value != "" {
		cfg.ClientID = value
	}
	if value := strings.TrimSpace(getenv("AUCTION_KAFKA_SECURITY_PROTOCOL")); value != "" {
		cfg.SecurityProtocol = value
	}
	if value := strings.TrimSpace(getenv("AUCTION_KAFKA_SASL_MECHANISM")); value != "" {
		cfg.SASLMechanism = value
	}
	if value := getenv("AUCTION_KAFKA_SASL_USERNAME"); value != "" {
		cfg.SASLUsername = value
	}
	if value := getenv("AUCTION_KAFKA_SASL_PASSWORD"); value != "" {
		cfg.SASLPassword = value
	}
	if value := strings.TrimSpace(getenv("AUCTION_KAFKA_TLS_CA_FILE")); value != "" {
		cfg.TLSCAFile = value
	}
	if value := strings.TrimSpace(getenv("AUCTION_KAFKA_TLS_SERVER_NAME")); value != "" {
		cfg.TLSServerName = value
	}
	if strings.TrimSpace(cfg.ClientID) == "" {
		cfg.ClientID = strings.TrimSpace(defaultClientID)
	}
	if len(cfg.Brokers) == 0 {
		cfg.Brokers = append([]string(nil), defaultBrokers...)
	}
	if strings.TrimSpace(cfg.SecurityProtocol) == "" {
		cfg.SecurityProtocol = SecurityProtocolPlaintext
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// LoadProperties reads the supported Go Kafka client properties from a Secret-mounted file.
func LoadProperties(path string) (Config, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return Config{}, fmt.Errorf("%w: properties file path is required", ErrInvalidConfig)
	}
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open Kafka client properties: %w", err)
	}
	defer func() { _ = file.Close() }()
	cfg, err := ParseProperties(file)
	if err != nil {
		return Config{}, fmt.Errorf("parse Kafka client properties: %w", err)
	}
	return cfg, nil
}

// ParseProperties accepts a deliberately small, auditable properties subset shared by all Go Kafka clients.
func ParseProperties(reader io.Reader) (Config, error) {
	if reader == nil {
		return Config{}, fmt.Errorf("%w: properties reader is required", ErrInvalidConfig)
	}
	values := make(map[string]string)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 64<<10)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		if strings.HasSuffix(line, "\\") {
			return Config{}, fmt.Errorf("%w: line %d uses unsupported continuation syntax", ErrInvalidConfig, lineNumber)
		}
		separator := strings.IndexAny(line, "=:")
		if separator <= 0 {
			return Config{}, fmt.Errorf("%w: line %d must be key=value", ErrInvalidConfig, lineNumber)
		}
		key := strings.TrimSpace(line[:separator])
		value := strings.TrimSpace(line[separator+1:])
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return Config{}, fmt.Errorf("%w: read properties: %v", ErrInvalidConfig, err)
	}
	if _, exists := values["sasl.jaas.config"]; exists && (values["sasl.username"] == "" || values["sasl.password"] == "") {
		return Config{}, fmt.Errorf("%w: sasl.jaas.config is not supported; mount sasl.username and sasl.password separately", ErrInvalidConfig)
	}
	if _, exists := values["ssl.truststore.location"]; exists && values["ssl.ca.location"] == "" {
		return Config{}, fmt.Errorf("%w: Java truststores are not supported; mount a PEM CA with ssl.ca.location", ErrInvalidConfig)
	}
	cfg := Config{
		Brokers:          splitCSV(values["bootstrap.servers"]),
		ClientID:         values["client.id"],
		SecurityProtocol: values["security.protocol"],
		SASLMechanism:    values["sasl.mechanism"],
		SASLUsername:     values["sasl.username"],
		SASLPassword:     values["sasl.password"],
		TLSCAFile:        values["ssl.ca.location"],
		TLSServerName:    values["ssl.server.name"],
	}
	if strings.TrimSpace(cfg.SecurityProtocol) == "" {
		cfg.SecurityProtocol = SecurityProtocolPlaintext
	}
	return cfg, nil
}

// Validate enforces address, transport, and credential invariants without including secrets in errors.
func (c Config) Validate() error {
	if len(c.Brokers) == 0 {
		return fmt.Errorf("%w: at least one broker is required", ErrInvalidConfig)
	}
	seen := make(map[string]struct{}, len(c.Brokers))
	for _, raw := range c.Brokers {
		broker := strings.TrimSpace(raw)
		if broker == "" || strings.ContainsAny(broker, "\r\n\x00") {
			return fmt.Errorf("%w: broker addresses must be non-empty and cannot contain control characters", ErrInvalidConfig)
		}
		host, portText, err := net.SplitHostPort(broker)
		if err != nil || strings.TrimSpace(host) == "" {
			return fmt.Errorf("%w: broker address must use host:port", ErrInvalidConfig)
		}
		port, err := strconv.Atoi(portText)
		if err != nil || port <= 0 || port > 65535 {
			return fmt.Errorf("%w: broker port must be within [1,65535]", ErrInvalidConfig)
		}
		if _, duplicate := seen[broker]; duplicate {
			continue
		}
		seen[broker] = struct{}{}
	}
	clientID := strings.TrimSpace(c.ClientID)
	if clientID == "" || len(clientID) > 128 || strings.ContainsAny(clientID, "\r\n\x00") {
		return fmt.Errorf("%w: client_id must be 1-128 characters without control characters", ErrInvalidConfig)
	}
	protocol := strings.ToUpper(strings.TrimSpace(c.SecurityProtocol))
	switch protocol {
	case SecurityProtocolPlaintext, SecurityProtocolTLS, SecurityProtocolSASLPlaintext, SecurityProtocolSASLTLS:
	default:
		return fmt.Errorf("%w: unsupported security.protocol", ErrInvalidConfig)
	}
	requiresSASL := protocol == SecurityProtocolSASLPlaintext || protocol == SecurityProtocolSASLTLS
	if requiresSASL {
		if strings.ToUpper(strings.TrimSpace(c.SASLMechanism)) != SASLMechanismSCRAMSHA512 {
			return fmt.Errorf("%w: SASL clients must use SCRAM-SHA-512", ErrInvalidConfig)
		}
		if c.SASLUsername == "" || c.SASLPassword == "" {
			return fmt.Errorf("%w: SASL username and password are required", ErrInvalidConfig)
		}
	} else if c.SASLMechanism != "" || c.SASLUsername != "" || c.SASLPassword != "" {
		return fmt.Errorf("%w: SASL fields require a SASL security protocol", ErrInvalidConfig)
	}
	requiresTLS := protocol == SecurityProtocolTLS || protocol == SecurityProtocolSASLTLS
	if !requiresTLS && (strings.TrimSpace(c.TLSCAFile) != "" || strings.TrimSpace(c.TLSServerName) != "") {
		return fmt.Errorf("%w: TLS fields require SSL or SASL_SSL", ErrInvalidConfig)
	}
	return nil
}

// Options returns validated franz-go transport options. Producer and consumer semantics are added by their callers.
func (c Config) Options() ([]kgo.Opt, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	brokers := deduplicate(c.Brokers)
	options := []kgo.Opt{kgo.SeedBrokers(brokers...), kgo.ClientID(strings.TrimSpace(c.ClientID))}
	protocol := strings.ToUpper(strings.TrimSpace(c.SecurityProtocol))
	if protocol == SecurityProtocolTLS || protocol == SecurityProtocolSASLTLS {
		tlsConfig, err := newTLSConfig(c.TLSCAFile, c.TLSServerName)
		if err != nil {
			return nil, err
		}
		options = append(options, kgo.DialTLSConfig(tlsConfig))
	}
	if protocol == SecurityProtocolSASLPlaintext || protocol == SecurityProtocolSASLTLS {
		mechanism := scram.Auth{User: c.SASLUsername, Pass: c.SASLPassword}.AsSha512Mechanism()
		options = append(options, kgo.SASL(mechanism))
	}
	return options, nil
}

func newTLSConfig(caFile, serverName string) (*tls.Config, error) {
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	caFile = strings.TrimSpace(caFile)
	if caFile != "" {
		pemBytes, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("read Kafka TLS CA: %w", err)
		}
		if !roots.AppendCertsFromPEM(pemBytes) {
			return nil, fmt.Errorf("%w: Kafka TLS CA contains no valid PEM certificate", ErrInvalidConfig)
		}
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    roots,
		ServerName: strings.TrimSpace(serverName),
	}, nil
}

func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func deduplicate(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
