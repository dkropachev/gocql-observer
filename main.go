package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gocql/gocql"
)

// clusterConfig holds the runtime configuration for a cluster connection.
type clusterConfig struct {
	Name          string
	ContactPoints []string
	Keyspace      string
	Username      string
	Password      string
	LogFile       string
}

// clusterLogger writes logs to a dedicated file and mirrors them to stdout with a prefix.
type clusterLogger struct {
	name string
	file *os.File
	mu   sync.Mutex
}

func newClusterLogger(name, path string) (*clusterLogger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creating log directory for %s: %w", name, err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening log file for %s: %w", name, err)
	}

	return &clusterLogger{name: name, file: f}, nil
}

func (l *clusterLogger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file.Close()
}

func (l *clusterLogger) Logf(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	ts := time.Now().UTC().Format(time.RFC3339Nano)
	line := fmt.Sprintf("%s %s", ts, fmt.Sprintf(format, args...))

	if _, err := l.file.WriteString(line + "\n"); err != nil {
		log.Printf("failed to write log for %s: %v", l.name, err)
	}

	fmt.Printf("[%s] %s\n", l.name, line)
}

func (l *clusterLogger) Print(v ...interface{}) {
	l.Logf("%s", fmt.Sprint(v...))
}

func (l *clusterLogger) Printf(format string, v ...interface{}) {
	l.Logf(format, v...)
}

func (l *clusterLogger) Println(v ...interface{}) {
	l.Logf("%s", fmt.Sprint(v...))
}

func main() {
	configs, err := loadClusterConfigs()
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}

	queryInterval := durationFromEnv("QUERY_INTERVAL", 15*time.Second)
	runDuration := durationFromEnv("RUN_DURATION", 0)

	ctx, cancel := context.WithCancel(context.Background())
	if runDuration > 0 {
		ctx, cancel = context.WithTimeout(ctx, runDuration)
	}
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("received shutdown signal, stopping...")
		cancel()
	}()

	if err := runObserver(ctx, configs, queryInterval); err != nil {
		log.Fatalf("observer exited with error: %v", err)
	}
}

func loadClusterConfigs() ([]clusterConfig, error) {
	logDir := getEnv("LOG_DIRECTORY", filepath.Join(".", "logs"))

	cfg1, err := buildClusterConfig(1, logDir)
	if err != nil {
		return nil, fmt.Errorf("cluster 1: %w", err)
	}

	cfg2, err := buildClusterConfig(2, logDir)
	if err != nil {
		return nil, fmt.Errorf("cluster 2: %w", err)
	}

	return []clusterConfig{cfg1, cfg2}, nil
}

func buildClusterConfig(idx int, logDir string) (clusterConfig, error) {
	prefix := fmt.Sprintf("CLUSTER%d_", idx)
	name := getEnv(prefix+"NAME", fmt.Sprintf("cluster%d", idx))
	contacts := splitAndClean(getEnv(prefix+"CONTACT_POINTS", ""))
	if len(contacts) == 0 {
		return clusterConfig{}, fmt.Errorf("no contact points configured (env %sCONTACT_POINTS)", prefix)
	}

	keyspace := getEnv(prefix+"KEYSPACE", "")
	username := getEnv(prefix+"USERNAME", "")
	password := getEnv(prefix+"PASSWORD", "")
	logFile := getEnv(prefix+"LOG_FILE", filepath.Join(logDir, fmt.Sprintf("%s.log", name)))

	return clusterConfig{
		Name:          name,
		ContactPoints: contacts,
		Keyspace:      keyspace,
		Username:      username,
		Password:      password,
		LogFile:       logFile,
	}, nil
}

type clusterRuntime struct {
	session *gocql.Session
	logger  *clusterLogger
	cfg     clusterConfig
}

func runObserver(ctx context.Context, configs []clusterConfig, interval time.Duration) error {
	runtimes := make([]*clusterRuntime, 0, len(configs))
	for _, cfg := range configs {
		logger, err := newClusterLogger(cfg.Name, cfg.LogFile)
		if err != nil {
			cleanupRuntimes(runtimes)
			return err
		}

		session, err := connectWithRetry(ctx, cfg, logger)
		if err != nil {
			logger.Close()
			cleanupRuntimes(runtimes)
			return err
		}

		logger.Logf("session ready for contact points: %s", strings.Join(cfg.ContactPoints, ","))
		logPeersSnapshot(session, logger)
		runtimes = append(runtimes, &clusterRuntime{session: session, logger: logger, cfg: cfg})
	}

	var wg sync.WaitGroup
	for _, rt := range runtimes {
		rt := rt
		wg.Add(1)
		go func() {
			defer wg.Done()
			pollCluster(ctx, rt.session, rt.logger, interval)
		}()
	}

	<-ctx.Done()
	wg.Wait()
	cleanupRuntimes(runtimes)
	return nil
}

func cleanupRuntimes(runtimes []*clusterRuntime) {
	for _, rt := range runtimes {
		if rt == nil {
			continue
		}
		if rt.session != nil {
			rt.session.Close()
		}
		if rt.logger != nil {
			rt.logger.Logf("shutting down")
			rt.logger.Close()
		}
	}
}

func newSession(cfg clusterConfig, logger *clusterLogger) (*gocql.Session, error) {
	cluster := gocql.NewCluster(cfg.ContactPoints...)
	cluster.Timeout = 15 * time.Millisecond
	cluster.RetryPolicy = &gocql.SimpleRetryPolicy{
		NumRetries: 0,
	}
	cluster.PoolConfig.HostSelectionPolicy = gocql.TokenAwareHostPolicy(gocql.RoundRobinHostPolicy())
	cluster.Port = 9042
	cluster.ReconnectionPolicy = &gocql.ConstantReconnectionPolicy{
		MaxRetries: 5,
		Interval:   10 * time.Second,
	}

	cluster.SocketKeepalive = time.Second * 30
	cluster.WriteCoalesceWaitTime = 0
	cluster.Compressor = &gocql.SnappyCompressor{}

	cluster.Keyspace = cfg.Keyspace
	cluster.ConnectTimeout = time.Second * 11
	cluster.ProtoVersion = 4
	cluster.Consistency = gocql.LocalOne
	cluster.Logger = logger

	if cfg.Username != "" || cfg.Password != "" {
		cluster.Authenticator = gocql.PasswordAuthenticator{
			Username: cfg.Username,
			Password: cfg.Password,
		}
	}

	session, err := cluster.CreateSession()
	if err != nil {
		return nil, fmt.Errorf("creating session for %s: %w", cfg.Name, err)
	}
	return session, nil
}

func connectWithRetry(ctx context.Context, cfg clusterConfig, logger *clusterLogger) (*gocql.Session, error) {
	const retryDelay = 5 * time.Second
	attempt := 1

	for {
		session, err := newSession(cfg, logger)
		if err == nil {
			if attempt > 1 {
				logger.Logf("connection established after %d attempts", attempt)
			}
			return session, nil
		}

		logger.Logf("connection attempt %d failed: %v", attempt, err)
		attempt++

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("context done before connecting to %s: %w", cfg.Name, ctx.Err())
		case <-time.After(retryDelay):
			logger.Logf("retrying connection to %s", cfg.Name)
		}
	}
}

func pollCluster(ctx context.Context, session *gocql.Session, logger *clusterLogger, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer func() {
		ticker.Stop()
		logger.Logf("shutting down, closing worker thread")
	}()

	logger.Logf("starting poll loop with interval %s", interval)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			start := time.Now()
			var clusterName, releaseVersion string
			query := session.Query("SELECT cluster_name, release_version FROM system.local").WithContext(ctx).Consistency(gocql.One)
			if err := query.Scan(&clusterName, &releaseVersion); err != nil {
				logger.Logf("heartbeat query failed: %v", err)
				continue
			}

			latency := time.Since(start)
			logger.Logf("cluster=%s release=%s latency=%s", clusterName, releaseVersion, latency)
			session.IterateHostPools(func(info gocql.HostPoolInfo) bool {
				logger.Logf("node=%s connect-address=%s shards=%d conns=%d excess-conns=%d", info.Host().HostID(), info.Host().ConnectAddress(), info.GetShardCount(), info.GetConnectionCount(), info.GetExcessConnectionCount())
				return true
			})
		}
	}
}

func logPeersSnapshot(session *gocql.Session, logger *clusterLogger) {
	rows, err := fetchPeers(session)
	if err != nil {
		logger.Logf("failed to fetch system.peers snapshot: %v", err)
		return
	}

	payload, err := json.Marshal(rows)
	if err != nil {
		logger.Logf("failed to marshal system.peers snapshot: %v", err)
		return
	}

	logger.Logf("system.peers snapshot: %s", payload)
}

func fetchPeers(session *gocql.Session) ([]map[string]string, error) {
	iter := session.Query("SELECT * FROM system.peers").Iter()
	defer iter.Close()

	var rows []map[string]string
	row := map[string]interface{}{}
	for iter.MapScan(row) {
		formatted := make(map[string]string, len(row))
		for k, v := range row {
			formatted[k] = fmt.Sprintf("%v", v)
		}
		rows = append(rows, formatted)
		row = map[string]interface{}{}
	}

	if err := iter.Close(); err != nil {
		return nil, err
	}
	return rows, nil
}

func getEnv(key, fallback string) string {
	if val := strings.TrimSpace(os.Getenv(key)); val != "" {
		return val
	}
	return fallback
}

func splitAndClean(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\t'
	})

	var out []string
	for _, f := range fields {
		trimmed := strings.TrimSpace(f)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func durationFromEnv(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}

	d, err := time.ParseDuration(raw)
	if err != nil {
		log.Printf("invalid duration for %s (%q), using fallback %s", key, raw, fallback)
		return fallback
	}
	return d
}
