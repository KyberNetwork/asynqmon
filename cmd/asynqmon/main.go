package main

import (
	"bytes"
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hibiken/asynq"
	"github.com/hibiken/asynq/x/metrics"
	"github.com/hibiken/asynqmon"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/cors"
	"gopkg.in/yaml.v3"
)

// RedisConfig holds the connection options for a single redis instance.
type RedisConfig struct {
	Addr             string
	DB               int
	Username         string
	Password         string
	TLS              string
	URL              string
	InsecureTLS      bool
	ClusterNodes     string
	SentinelUsername string
	SentinelPassword string
	DialTimeout      time.Duration
	ReadTimeout      time.Duration
	WriteTimeout     time.Duration
}

// Config holds configurations for the program provided via the command line.
type Config struct {
	// Server port
	Port int

	// Path to a YAML/JSON config file describing multiple redis instances.
	// When set, it takes precedence over the single-instance redis flags below.
	ConfigFile string

	// Redis connection options for the single-instance (flag/env) mode.
	Redis RedisConfig

	// UI related configs
	ReadOnly         bool
	MaxPayloadLength int
	MaxResultLength  int

	// Prometheus related configs
	EnableMetricsExporter bool
	PrometheusServerAddr  string

	// Args are the positional (non-flag) command line arguments
	Args []string
}

// parseFlags parses the command-line arguments provided to the program.
// Typically, os.Args[0] is provided as 'progname' and os.args[1:] as 'args'.
// Returns the Config in case parsing succeeded, or an error. In any case, the
// output of the flag.Parse is returned in output.
//
// Reference: https://eli.thegreenplace.net/2020/testing-flag-parsing-in-go-programs/
func parseFlags(progname string, args []string) (cfg *Config, output string, err error) {
	flags := flag.NewFlagSet(progname, flag.ContinueOnError)
	var buf bytes.Buffer
	flags.SetOutput(&buf)

	var conf Config
	flags.IntVar(&conf.Port, "port", getEnvOrDefaultInt("PORT", 8080), "port number to use for web ui server")
	flags.StringVar(&conf.ConfigFile, "config-file", getEnvDefaultString("CONFIG_FILE", ""), "path to a YAML/JSON config file describing multiple redis instances")
	flags.StringVar(&conf.Redis.Addr, "redis-addr", getEnvDefaultString("REDIS_ADDR", "127.0.0.1:6379"), "address of redis server to connect to")
	flags.IntVar(&conf.Redis.DB, "redis-db", getEnvOrDefaultInt("REDIS_DB", 0), "redis database number")
	flags.StringVar(&conf.Redis.Username, "redis-username", getEnvDefaultString("REDIS_USERNAME", ""), "username to use when connecting to redis server")
	flags.StringVar(&conf.Redis.Password, "redis-password", getEnvDefaultString("REDIS_PASSWORD", ""), "password to use when connecting to redis server")
	flags.StringVar(&conf.Redis.TLS, "redis-tls", getEnvDefaultString("REDIS_TLS", ""), "server name for TLS validation used when connecting to redis server")
	flags.StringVar(&conf.Redis.URL, "redis-url", getEnvDefaultString("REDIS_URL", ""), "URL to redis server")
	flags.StringVar(&conf.Redis.SentinelUsername, "redis-sentinel-username", getEnvDefaultString("REDIS_SENTINEL_USERNAME", ""), "username to use when connecting to redis sentinel")
	flags.StringVar(&conf.Redis.SentinelPassword, "redis-sentinel-password", getEnvDefaultString("REDIS_SENTINEL_PASSWORD", ""), "password to use when connecting to redis sentinel")
	flags.BoolVar(&conf.Redis.InsecureTLS, "redis-insecure-tls", getEnvOrDefaultBool("REDIS_INSECURE_TLS", false), "disable TLS certificate host checks")
	flags.StringVar(&conf.Redis.ClusterNodes, "redis-cluster-nodes", getEnvDefaultString("REDIS_CLUSTER_NODES", ""), "comma separated list of host:port addresses of cluster nodes")
	flags.IntVar(&conf.MaxPayloadLength, "max-payload-length", getEnvOrDefaultInt("MAX_PAYLOAD_LENGTH", 200), "maximum number of utf8 characters printed in the payload cell in the Web UI")
	flags.IntVar(&conf.MaxResultLength, "max-result-length", getEnvOrDefaultInt("MAX_RESULT_LENGTH", 200), "maximum number of utf8 characters printed in the result cell in the Web UI")
	flags.BoolVar(&conf.EnableMetricsExporter, "enable-metrics-exporter", getEnvOrDefaultBool("ENABLE_METRICS_EXPORTER", false), "enable prometheus metrics exporter to expose queue metrics")
	flags.StringVar(&conf.PrometheusServerAddr, "prometheus-addr", getEnvDefaultString("PROMETHEUS_ADDR", ""), "address of prometheus server to query time series")
	flags.BoolVar(&conf.ReadOnly, "read-only", getEnvOrDefaultBool("READ_ONLY", false), "restrict to read-only mode")
	flags.DurationVar(&conf.Redis.DialTimeout, "redis-dial-timeout", getEnvOrDefaultDuration("REDIS_DIAL_TIMEOUT", 0), "timeout for establishing a connection to redis (0 means default: 5s)")
	flags.DurationVar(&conf.Redis.ReadTimeout, "redis-read-timeout", getEnvOrDefaultDuration("REDIS_READ_TIMEOUT", 0), "timeout for redis read operations (0 means default: 3s)")
	flags.DurationVar(&conf.Redis.WriteTimeout, "redis-write-timeout", getEnvOrDefaultDuration("REDIS_WRITE_TIMEOUT", 0), "timeout for redis write operations (0 means default: same as read timeout)")

	err = flags.Parse(args)
	if err != nil {
		return nil, buf.String(), err
	}
	conf.Args = flags.Args()
	return &conf, buf.String(), nil
}

func makeTLSConfig(rc RedisConfig) *tls.Config {
	if rc.TLS == "" && !rc.InsecureTLS {
		return nil
	}
	return &tls.Config{
		ServerName:         rc.TLS,
		InsecureSkipVerify: rc.InsecureTLS,
	}
}

func makeRedisConnOpt(rc RedisConfig) (asynq.RedisConnOpt, error) {
	// Connecting to redis-cluster
	if len(rc.ClusterNodes) > 0 {
		return asynq.RedisClusterClientOpt{
			Addrs:        strings.Split(rc.ClusterNodes, ","),
			Username:     rc.Username,
			Password:     rc.Password,
			TLSConfig:    makeTLSConfig(rc),
			DialTimeout:  rc.DialTimeout,
			ReadTimeout:  rc.ReadTimeout,
			WriteTimeout: rc.WriteTimeout,
		}, nil
	}

	// Connecting to redis-sentinels
	if strings.HasPrefix(rc.URL, "redis-sentinel") {
		res, err := asynq.ParseRedisURI(rc.URL)
		if err != nil {
			return nil, err
		}
		connOpt := res.(asynq.RedisFailoverClientOpt) // safe to type-assert
		if rc.SentinelUsername != "" {
			connOpt.SentinelUsername = rc.SentinelUsername
		}
		if rc.SentinelPassword != "" {
			connOpt.SentinelPassword = rc.SentinelPassword
		}
		if rc.Username != "" {
			connOpt.Username = rc.Username
		}
		if rc.Password != "" {
			connOpt.Password = rc.Password
		}
		// The sentinel URL has no db component, so take it from the config.
		connOpt.DB = rc.DB
		connOpt.TLSConfig = makeTLSConfig(rc)
		connOpt.DialTimeout = rc.DialTimeout
		connOpt.ReadTimeout = rc.ReadTimeout
		connOpt.WriteTimeout = rc.WriteTimeout
		return connOpt, nil
	}

	// Connecting to single redis server
	var connOpt asynq.RedisClientOpt
	if len(rc.URL) > 0 {
		res, err := asynq.ParseRedisURI(rc.URL)
		if err != nil {
			return nil, err
		}
		connOpt = res.(asynq.RedisClientOpt) // safe to type-assert
	} else {
		connOpt.Addr = rc.Addr
		connOpt.DB = rc.DB
		connOpt.Username = rc.Username
		connOpt.Password = rc.Password
	}
	if connOpt.TLSConfig == nil {
		connOpt.TLSConfig = makeTLSConfig(rc)
	}
	connOpt.DialTimeout = rc.DialTimeout
	connOpt.ReadTimeout = rc.ReadTimeout
	connOpt.WriteTimeout = rc.WriteTimeout
	return connOpt, nil
}

// fileConfig is the schema of the optional --config-file (YAML; JSON is also
// accepted since YAML is a superset of JSON).
type fileConfig struct {
	Port                  int            `yaml:"port"`
	ReadOnly              bool           `yaml:"read_only"`
	PrometheusAddr        string         `yaml:"prometheus_addr"`
	MaxPayloadLength      int            `yaml:"max_payload_length"`
	MaxResultLength       int            `yaml:"max_result_length"`
	EnableMetricsExporter bool           `yaml:"enable_metrics_exporter"`
	Instances             []fileInstance `yaml:"instances"`
}

// fileInstance describes a single named redis instance in the config file.
// Durations are strings (e.g. "5s") parsed via time.ParseDuration.
type fileInstance struct {
	ID                    string `yaml:"id"`
	Name                  string `yaml:"name"`
	RedisAddr             string `yaml:"redis_addr"`
	RedisDB               int    `yaml:"redis_db"`
	RedisUsername         string `yaml:"redis_username"`
	RedisPassword         string `yaml:"redis_password"`
	RedisTLS              string `yaml:"redis_tls"`
	RedisURL              string `yaml:"redis_url"`
	RedisInsecureTLS      bool   `yaml:"redis_insecure_tls"`
	RedisClusterNodes     string `yaml:"redis_cluster_nodes"`
	RedisSentinelUsername string `yaml:"redis_sentinel_username"`
	RedisSentinelPassword string `yaml:"redis_sentinel_password"`
	RedisDialTimeout      string `yaml:"redis_dial_timeout"`
	RedisReadTimeout      string `yaml:"redis_read_timeout"`
	RedisWriteTimeout     string `yaml:"redis_write_timeout"`
}

// prometheusInstanceLabel is the label added to exported queue metrics to
// identify the redis instance when monitoring more than one. It deliberately
// isn't "instance", which Prometheus reserves for the scrape target and would
// rename to "exported_instance" on scrape.
const prometheusInstanceLabel = "redis_instance"

// instanceIDPattern restricts instance ids to URL-safe identifiers.
var instanceIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// envVarPattern matches ${VAR} references in the config file.
var envVarPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expandEnvVars replaces ${VAR} references in the scalar values of the parsed
// config with the value of the corresponding environment variable. This lets
// secrets such as redis passwords be kept out of the config file. Expansion is
// done on parsed values (not raw text) so references inside comments are
// ignored and values containing YAML special characters can't break parsing.
// Referencing an undefined variable is an error so misconfiguration fails
// loudly instead of silently connecting with an empty value.
func expandEnvVars(root *yaml.Node) error {
	var missing []string
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		if n.Kind == yaml.ScalarNode {
			n.Value = envVarPattern.ReplaceAllStringFunc(n.Value, func(match string) string {
				name := envVarPattern.FindStringSubmatch(match)[1]
				val, ok := os.LookupEnv(name)
				if !ok {
					missing = append(missing, name)
				}
				return val
			})
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	walk(root)
	if len(missing) > 0 {
		return fmt.Errorf("config file references undefined environment variable(s): %s", strings.Join(missing, ", "))
	}
	return nil
}

func parseOptionalDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	return time.ParseDuration(s)
}

func (fi fileInstance) redisConfig() (RedisConfig, error) {
	dial, err := parseOptionalDuration(fi.RedisDialTimeout)
	if err != nil {
		return RedisConfig{}, fmt.Errorf("invalid redis_dial_timeout: %w", err)
	}
	read, err := parseOptionalDuration(fi.RedisReadTimeout)
	if err != nil {
		return RedisConfig{}, fmt.Errorf("invalid redis_read_timeout: %w", err)
	}
	write, err := parseOptionalDuration(fi.RedisWriteTimeout)
	if err != nil {
		return RedisConfig{}, fmt.Errorf("invalid redis_write_timeout: %w", err)
	}
	return RedisConfig{
		Addr:             fi.RedisAddr,
		DB:               fi.RedisDB,
		Username:         fi.RedisUsername,
		Password:         fi.RedisPassword,
		TLS:              fi.RedisTLS,
		URL:              fi.RedisURL,
		InsecureTLS:      fi.RedisInsecureTLS,
		ClusterNodes:     fi.RedisClusterNodes,
		SentinelUsername: fi.RedisSentinelUsername,
		SentinelPassword: fi.RedisSentinelPassword,
		DialTimeout:      dial,
		ReadTimeout:      read,
		WriteTimeout:     write,
	}, nil
}

// loadConfigFile reads and parses the config file, applying its global settings
// onto cfg (file values take precedence when set) and returning the instances.
func loadConfigFile(cfg *Config) ([]asynqmon.RedisInstance, error) {
	data, err := os.ReadFile(cfg.ConfigFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}
	// Expand ${VAR} references (e.g. for secrets kept in environment variables).
	if err := expandEnvVars(&root); err != nil {
		return nil, err
	}
	// Decode strictly so typos in keys (e.g. "redis_pasword") fail loudly
	// instead of being silently ignored. yaml.Node.Decode has no strict mode,
	// so round-trip through a Decoder with KnownFields enabled.
	expanded, err := yaml.Marshal(&root)
	if err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(expanded))
	dec.KnownFields(true)
	var fc fileConfig
	if err := dec.Decode(&fc); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}
	if len(fc.Instances) == 0 {
		return nil, fmt.Errorf("config file must define at least one instance")
	}

	// Apply global settings from the file, falling back to flag/env defaults
	// already present on cfg when the file omits a value.
	if fc.Port != 0 {
		cfg.Port = fc.Port
	}
	cfg.ReadOnly = cfg.ReadOnly || fc.ReadOnly
	cfg.EnableMetricsExporter = cfg.EnableMetricsExporter || fc.EnableMetricsExporter
	if fc.PrometheusAddr != "" {
		cfg.PrometheusServerAddr = fc.PrometheusAddr
	}
	if fc.MaxPayloadLength != 0 {
		cfg.MaxPayloadLength = fc.MaxPayloadLength
	}
	if fc.MaxResultLength != 0 {
		cfg.MaxResultLength = fc.MaxResultLength
	}

	seen := make(map[string]bool, len(fc.Instances))
	var instances []asynqmon.RedisInstance
	for i, fi := range fc.Instances {
		if !instanceIDPattern.MatchString(fi.ID) {
			return nil, fmt.Errorf("instance #%d: id %q must be a non-empty URL-safe identifier ([a-zA-Z0-9_-]+)", i+1, fi.ID)
		}
		if seen[fi.ID] {
			return nil, fmt.Errorf("duplicate instance id %q", fi.ID)
		}
		seen[fi.ID] = true
		rc, err := fi.redisConfig()
		if err != nil {
			return nil, fmt.Errorf("instance %q: %w", fi.ID, err)
		}
		connOpt, err := makeRedisConnOpt(rc)
		if err != nil {
			return nil, fmt.Errorf("instance %q: %w", fi.ID, err)
		}
		name := fi.Name
		if name == "" {
			name = fi.ID
		}
		instances = append(instances, asynqmon.RedisInstance{
			ID:           fi.ID,
			Name:         name,
			RedisConnOpt: connOpt,
		})
	}
	return instances, nil
}

// loadInstances returns the list of redis instances to monitor based on cfg.
// When a config file is provided it is used; otherwise a single "default"
// instance is built from the redis flags/env for backward compatibility.
func loadInstances(cfg *Config) ([]asynqmon.RedisInstance, error) {
	if cfg.ConfigFile != "" {
		return loadConfigFile(cfg)
	}
	connOpt, err := makeRedisConnOpt(cfg.Redis)
	if err != nil {
		return nil, err
	}
	return []asynqmon.RedisInstance{{
		ID:           "default",
		Name:         "default",
		RedisConnOpt: connOpt,
	}}, nil
}

func main() {
	cfg, output, err := parseFlags(os.Args[0], os.Args[1:])
	if err == flag.ErrHelp {
		fmt.Println(output)
		os.Exit(2)
	} else if err != nil {
		fmt.Printf("error: %v\n", err)
		fmt.Println(output)
		os.Exit(1)
	}

	instances, err := loadInstances(cfg)
	if err != nil {
		log.Fatal(err)
	}

	// Metrics are only labelled per instance by this process's own exporter,
	// and only when there is more than one instance (see below). Filtering
	// otherwise would hide metrics scraped from other exporters, which lack
	// the label.
	var instanceLabel string
	if cfg.EnableMetricsExporter && len(instances) > 1 {
		instanceLabel = prometheusInstanceLabel
	}

	h := asynqmon.New(asynqmon.Options{
		RedisInstances:          instances,
		PrometheusInstanceLabel: instanceLabel,
		PayloadFormatter:        asynqmon.PayloadFormatterFunc(payloadFormatterFunc(cfg)),
		ResultFormatter:         asynqmon.ResultFormatterFunc(resultFormatterFunc(cfg)),
		PrometheusAddress:       cfg.PrometheusServerAddr,
		ReadOnly:                cfg.ReadOnly,
	})
	defer h.Close()

	c := cors.New(cors.Options{
		AllowedMethods: []string{"GET", "POST", "DELETE"},
	})
	mux := http.NewServeMux()
	mux.Handle("/", c.Handler(h))
	if cfg.EnableMetricsExporter {
		// Using NewPedanticRegistry here to test the implementation of Collectors and Metrics.
		reg := prometheus.NewPedanticRegistry()

		// Register a queue metrics collector for each instance so /metrics
		// covers all monitored redis instances. With multiple instances we add
		// a const label to disambiguate identically-named queues (and avoid
		// duplicate metric descriptor collisions). A single instance is
		// registered without the extra label to preserve existing output.
		for _, inst := range instances {
			inspector := asynq.NewInspector(inst.RedisConnOpt)
			collector := metrics.NewQueueMetricsCollector(inspector)
			if len(instances) > 1 {
				prometheus.WrapRegistererWith(prometheus.Labels{prometheusInstanceLabel: inst.ID}, reg).MustRegister(collector)
			} else {
				reg.MustRegister(collector)
			}
		}
		reg.MustRegister(
			// Add the standard process and go metrics to the registry
			prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}),
			prometheus.NewGoCollector(),
		)
		mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	}

	srv := &http.Server{
		Handler:      mux,
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		WriteTimeout: 10 * time.Second,
		ReadTimeout:  10 * time.Second,
	}

	fmt.Printf("Asynq Monitoring WebUI server is listening on port %d\n", cfg.Port)
	log.Fatal(srv.ListenAndServe())
}

func payloadFormatterFunc(cfg *Config) func(string, []byte) string {
	return func(taskType string, payload []byte) string {
		payloadStr := asynqmon.DefaultPayloadFormatter.FormatPayload(taskType, payload)
		return truncate(payloadStr, cfg.MaxPayloadLength)
	}
}

func resultFormatterFunc(cfg *Config) func(string, []byte) string {
	return func(taskType string, result []byte) string {
		resultStr := asynqmon.DefaultResultFormatter.FormatResult(taskType, result)
		return truncate(resultStr, cfg.MaxResultLength)
	}
}

// truncates string s to limit length (in utf8).
func truncate(s string, limit int) string {
	i := 0
	for pos := range s {
		if i == limit {
			return s[:pos] + "…"
		}
		i++
	}
	return s
}

func getEnvDefaultString(key, def string) string {
	v := os.Getenv(key)
	if v == "" {
		return def
	}

	return v
}

func getEnvOrDefaultInt(key string, def int) int {
	v, err := strconv.Atoi(os.Getenv(key))
	if err != nil {
		return def
	}
	return v
}

func getEnvOrDefaultBool(key string, def bool) bool {
	v, err := strconv.ParseBool(os.Getenv(key))
	if err != nil {
		return def
	}
	return v
}

func getEnvOrDefaultDuration(key string, def time.Duration) time.Duration {
	v, err := time.ParseDuration(os.Getenv(key))
	if err != nil {
		return def
	}
	return v
}
