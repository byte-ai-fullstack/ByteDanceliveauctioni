package kafkaclient

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/twmb/franz-go/pkg/kgo"
)

func TestParsePropertiesLoadsSupportedFieldsWithoutSecretsInErrors(t *testing.T) {
	testPassword := strings.Repeat("fixture-", 3) + "value=equals"
	cfg, err := ParseProperties(strings.NewReader(`
# production relay
bootstrap.servers=kafka-1:9092,kafka-2:9092,kafka-1:9092
client.id=outbox-relay-1
security.protocol=SASL_SSL
sasl.mechanism=SCRAM-SHA-512
sasl.username=relay-user
sasl.password=` + testPassword + `
ssl.server.name=kafka.internal
`))
	if err != nil {
		t.Fatalf("ParseProperties: %v", err)
	}
	if got, want := cfg.Brokers, []string{"kafka-1:9092", "kafka-2:9092", "kafka-1:9092"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("brokers=%v want=%v", got, want)
	}
	if cfg.SASLPassword != testPassword || cfg.SecurityProtocol != SecurityProtocolSASLTLS {
		t.Fatalf("unexpected parsed config: %+v", cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	options, err := cfg.Options()
	if err != nil {
		t.Fatalf("Options: %v", err)
	}
	client, err := kgo.NewClient(options...)
	if err != nil {
		t.Fatalf("kgo.NewClient: %v", err)
	}
	client.Close()
}

func TestParsePropertiesRejectsJavaOnlyAndMalformedSyntax(t *testing.T) {
	tests := []string{
		"sasl.jaas.config=secret\n",
		"ssl.truststore.location=/secret/truststore.p12\n",
		"missing-separator\n",
		"bootstrap.servers=kafka:9092\\\ncontinued=true\n",
	}
	for _, input := range tests {
		if _, err := ParseProperties(strings.NewReader(input)); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("input=%q error=%v want ErrInvalidConfig", input, err)
		}
	}
	if _, err := ParseProperties(nil); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("nil reader error=%v", err)
	}
}

func TestFromEnvLoadsFileAndAppliesNonSecretOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.properties")
	if err := os.WriteFile(path, []byte("bootstrap.servers=kafka-file:9092\nclient.id=file-id\n"), 0o600); err != nil {
		t.Fatalf("write properties: %v", err)
	}
	values := map[string]string{
		"AUCTION_KAFKA_CLIENT_PROPERTIES_FILE": path,
		"AUCTION_KAFKA_BROKERS":                "kafka-env:19092",
		"AUCTION_KAFKA_CLIENT_ID":              "env-id",
	}
	cfg, err := FromEnv(func(key string) string { return values[key] }, []string{"kafka-default:9092"}, "default-id")
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if got, want := cfg.Brokers, []string{"kafka-env:19092"}; !reflect.DeepEqual(got, want) || cfg.ClientID != "env-id" {
		t.Fatalf("config=%+v", cfg)
	}
}

func TestConfigValidateRejectsUnsafeOrIncompleteTransport(t *testing.T) {
	base := Config{Brokers: []string{"kafka:9092"}, ClientID: "relay", SecurityProtocol: SecurityProtocolPlaintext}
	tests := []Config{
		{},
		{Brokers: []string{"missing-port"}, ClientID: "relay", SecurityProtocol: SecurityProtocolPlaintext},
		{Brokers: []string{"kafka:70000"}, ClientID: "relay", SecurityProtocol: SecurityProtocolPlaintext},
		{Brokers: base.Brokers, SecurityProtocol: SecurityProtocolPlaintext},
		{Brokers: base.Brokers, ClientID: "relay", SecurityProtocol: "UNKNOWN"},
		{Brokers: base.Brokers, ClientID: "relay", SecurityProtocol: SecurityProtocolSASLTLS, SASLMechanism: "PLAIN", SASLUsername: "u", SASLPassword: "p"},
		{Brokers: base.Brokers, ClientID: "relay", SecurityProtocol: SecurityProtocolSASLTLS, SASLMechanism: SASLMechanismSCRAMSHA512},
		{Brokers: base.Brokers, ClientID: "relay", SecurityProtocol: SecurityProtocolPlaintext, SASLUsername: "u"},
		{Brokers: base.Brokers, ClientID: "relay", SecurityProtocol: SecurityProtocolPlaintext, TLSCAFile: "/tmp/ca.pem"},
	}
	for _, cfg := range tests {
		if err := cfg.Validate(); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("config=%+v error=%v want ErrInvalidConfig", cfg, err)
		}
	}
	if _, err := (Config{Brokers: base.Brokers, ClientID: "relay", SecurityProtocol: SecurityProtocolTLS, TLSCAFile: "/does/not/exist"}).Options(); err == nil {
		t.Fatal("missing TLS CA returned no error")
	}
}

func TestLoadPropertiesAndFromEnvRejectMissingInputs(t *testing.T) {
	if _, err := LoadProperties(""); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("empty path error=%v", err)
	}
	if _, err := LoadProperties(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing properties file returned no error")
	}
	if _, err := FromEnv(nil, nil, "relay"); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("nil getenv error=%v", err)
	}
	if _, err := FromEnv(func(string) string { return "" }, nil, "relay"); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("missing brokers error=%v", err)
	}
}
