// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"

	"slices"

	"github.com/inditextech/redisoperator/robin/metrics"
	"github.com/inditextech/redisoperator/robin/util"
	redisgo "github.com/redis/go-redis/v9"
)

// -----------------------------------------------------------------------------
// Constants
// -----------------------------------------------------------------------------
const (
	RedisPort          = 6379
	SectionServer      = "server"
	SectionClients     = "clients"
	SectionMemory      = "memory"
	SectionPersistence = "persistence"
	SectionStats       = "stats"
	SectionReplication = "replication"
	SectionCPU         = "cpu"
	SectionCluster     = "cluster"
	SectionKeyspace    = "keyspace"
	SectionCmdStats    = "commandstats"
	SectionErrorStats  = "errorstats"
	SectionLatency     = "latencystats"
)

// -----------------------------------------------------------------------------
// Redis Client
// -----------------------------------------------------------------------------

// RedisClient encapsulates a connection to Redis.
type RedisClient struct {
	client *redisgo.Client
	ctx    context.Context
}

// NewRedisClient creates a new RedisClient for the given address.
func NewRedisClient(ctx context.Context, addr, password string, db int) *RedisClient {
	client := redisgo.NewClient(&redisgo.Options{
		Addr:     fmt.Sprintf("%s:%d", addr, RedisPort),
		Password: password,
		DB:       db,
	})
	return &RedisClient{
		client: client,
		ctx:    ctx,
	}
}

// CheckConnection pings the Redis server until a connection is established.
func (rc *RedisClient) CheckConnection(maxRetries int, backoff time.Duration) error {
	if maxRetries <= 0 {
		return fmt.Errorf("maxRetries must be greater than 0")
	}
	if backoff <= 0 {
		return fmt.Errorf("backoff must be greater than 0")
	}
	for range maxRetries {
		if _, err := rc.client.Ping(rc.ctx).Result(); err == nil {
			return nil
		}
		time.Sleep(backoff)
	}
	return fmt.Errorf("failed to connect after %d retries", maxRetries)
}

// RedisInfo represents structured Redis INFO output.
type RedisInfo struct {
	Server       map[string]string
	Clients      map[string]int64
	Memory       map[string]string
	Persistence  map[string]string
	Stats        map[string]string
	Replication  map[string]string
	CPU          map[string]float64
	Cluster      map[string]string
	Keyspace     map[string]string
	CommandStats map[string]string
	ErrorStats   map[string]string
	LatencyStats map[string]string
}

// GetInfo retrieves and parses the Redis INFO output for the given IP.
func (rc *RedisClient) GetInfo() (*RedisInfo, error) {
	info, err := rc.client.Info(rc.ctx, "all").Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get info from %s: %w", rc.client.Options().Addr, err)
	}

	// Parse the response into a structured format
	return parseRedisInfo(info), nil
}

// parseRedisInfo processes Redis INFO output into a structured format.
func parseRedisInfo(info string) *RedisInfo {
	parsedInfo := &RedisInfo{
		Server:       make(map[string]string),
		Clients:      make(map[string]int64),
		Memory:       make(map[string]string),
		Persistence:  make(map[string]string),
		Stats:        make(map[string]string),
		Replication:  make(map[string]string),
		CPU:          make(map[string]float64),
		Cluster:      make(map[string]string),
		Keyspace:     make(map[string]string),
		CommandStats: make(map[string]string),
		ErrorStats:   make(map[string]string),
		LatencyStats: make(map[string]string),
	}

	section := ""
	lines := strings.Split(strings.TrimSpace(info), "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Identify the current section if line starts with '#'
		if strings.HasPrefix(line, "#") {
			section = strings.ToLower(strings.TrimPrefix(line, "#"))
			section = strings.TrimSpace(section)
			continue
		}

		// Parse "key:value" structure
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			log.Printf("Skipping malformed line: %s", line)
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		// Populate parsedInfo based on the recognized section
		switch section {
		case SectionServer:
			parsedInfo.Server[key] = value
		case SectionClients:
			intVal := util.ParseInt64(value)
			parsedInfo.Clients[key] = intVal
		case SectionMemory:
			parsedInfo.Memory[key] = value
		case SectionPersistence:
			parsedInfo.Persistence[key] = value
		case SectionStats:
			parsedInfo.Stats[key] = value
		case SectionReplication:
			parsedInfo.Replication[key] = value
		case SectionCPU:
			floatVal := util.ParseFloat(value)
			parsedInfo.CPU[key] = floatVal
		case SectionCluster:
			parsedInfo.Cluster[key] = value
		case SectionKeyspace:
			parsedInfo.Keyspace[key] = value
		case SectionCmdStats:
			parsedInfo.CommandStats[key] = value
		case SectionErrorStats:
			parsedInfo.ErrorStats[key] = value
		case SectionLatency:
			parsedInfo.LatencyStats[key] = value
		default:
			log.Printf("Ignoring unknown info section '%s', key: '%s'", section, key)
		}
	}

	return parsedInfo
}

// ClusterInfo represents structured Redis cluster information.
type ClusterInfo struct {
	State                        string
	SlotsAssigned                int
	SlotsOK                      int
	SlotsPFail                   int
	SlotsFail                    int
	KnownNodes                   int
	ClusterSize                  int
	CurrentEpoch                 int
	MyEpoch                      int
	MessagesPingSent             int
	MessagesPongSent             int
	MessagesMeetSent             int
	MessagesSent                 int
	MessagesPingReceived         int
	MessagesPongReceived         int
	MessagesMeetReceived         int
	MessagesReceived             int
	TotalClusterLinksBufferLimit int
	MessagesUpdateSent           int
	MessagesUpdateReceived       int
	MessagesFailReceived         int
}

// GetClusterInfo retrieves and parses cluster information from Redis.
func (rc *RedisClient) GetClusterInfo() (*ClusterInfo, error) {
	info, err := rc.client.ClusterInfo(rc.ctx).Result()
	if err != nil {
		log.Printf("Error fetching cluster info: %v", err)
		return nil, err
	}

	// Parse response into a structured format
	clusterInfo := &ClusterInfo{}
	lines := strings.Split(strings.TrimSpace(info), "\n")
	if len(lines) == 0 {
		return nil, fmt.Errorf("empty cluster info response")
	}

	for _, line := range lines {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			log.Printf("Skipping malformed cluster info line: %s", line)
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		// Map values to struct fields
		switch key {
		case metrics.ClusterState:
			clusterInfo.State = value
		case metrics.ClusterSlotsAssigned:
			clusterInfo.SlotsAssigned = util.ParseInt(value)
		case metrics.ClusterSlotsOk:
			clusterInfo.SlotsOK = util.ParseInt(value)
		case metrics.ClusterSlotsPFail:
			clusterInfo.SlotsPFail = util.ParseInt(value)
		case metrics.ClusterSlotsFail:
			clusterInfo.SlotsFail = util.ParseInt(value)
		case metrics.ClusterKnownNodes:
			clusterInfo.KnownNodes = util.ParseInt(value)
		case metrics.ClusterSize:
			clusterInfo.ClusterSize = util.ParseInt(value)
		case metrics.ClusterCurrentEpoch:
			clusterInfo.CurrentEpoch = util.ParseInt(value)
		case metrics.ClusterMyEpoch:
			clusterInfo.MyEpoch = util.ParseInt(value)
		case metrics.ClusterStatsMMS:
			clusterInfo.MessagesMeetSent = util.ParseInt(value)
		case metrics.ClusterStatsMMR:
			clusterInfo.MessagesMeetReceived = util.ParseInt(value)
		case metrics.ClusterStatsMS:
			clusterInfo.MessagesSent = util.ParseInt(value)
		case metrics.ClusterStatsMR:
			clusterInfo.MessagesReceived = util.ParseInt(value)
		case metrics.ClusterStatsMPS:
			clusterInfo.MessagesPingSent = util.ParseInt(value)
		case metrics.ClusterStatsMPR:
			clusterInfo.MessagesPingReceived = util.ParseInt(value)
		case metrics.ClusterStatsMPongS:
			clusterInfo.MessagesPongSent = util.ParseInt(value)
		case metrics.ClusterStatsMPongR:
			clusterInfo.MessagesPongReceived = util.ParseInt(value)
		case metrics.TotalClusterLinksBufEx:
			clusterInfo.TotalClusterLinksBufferLimit = util.ParseInt(value)
		case metrics.ClusterStatsMessagesUpdateSent:
			clusterInfo.MessagesUpdateSent = util.ParseInt(value)
		case metrics.ClusterStatsMessagesUpdateReceived:
			clusterInfo.MessagesUpdateReceived = util.ParseInt(value)
		case metrics.ClusterStatsMessagesFailReceived:
			clusterInfo.MessagesFailReceived = util.ParseInt(value)
		default:
		}
	}

	return clusterInfo, nil
}

// Node represents a Redis cluster node.
type Node struct {
	ID       string
	IP       string
	Role     string
	Slots    []string
	MasterID string
	Failures int
}

// GetNodesInfo retrieves and parses the cluster nodes information.
func (rc *RedisClient) GetNodesInfo() ([]Node, error) {
	result, err := rc.client.ClusterNodes(rc.ctx).Result()
	if err != nil {
		log.Printf("Error fetching cluster nodes: %v", err)
		return nil, err
	}

	// Split response into lines
	lines := strings.Split(strings.TrimSpace(result), "\n")
	if len(lines) == 0 {
		return nil, fmt.Errorf("empty cluster nodes response")
	}

	var nodes []Node
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 8 {
			log.Printf("Skipping malformed line: %s", line)
			continue
		}

		// Extract Node Details
		nodeID := fields[0]
		ipPort := fields[1]
		role := fields[2]     // First flag usually indicates role
		masterID := fields[3] // "-" if master, otherwise Master ID
		// connected := fields[7] == "connected"

		// Extract slot information (if available)
		var slots []string
		if len(fields) > 8 {
			slots = fields[8:]
		}

		// Validate role
		if role != "master" && role != "slave" && role != "myself,master" && role != "myself,slave" {
			continue
		}

		// Retrieve failure count
		failures := 0
		failureStr, err := rc.client.ClusterCountFailureReports(rc.ctx, nodeID).Result()
		if err == nil {
			failures = int(failureStr)
		}

		// Construct Node struct
		node := Node{
			ID:       nodeID,
			IP:       strings.Split(ipPort, ":")[0], // Extract only IP
			Role:     role,
			Slots:    slots,
			MasterID: masterID,
			Failures: failures,
		}

		nodes = append(nodes, node)
		// Only include connected nodes
		// if connected {
		// 	nodes = append(nodes, node)
		// }
	}

	return nodes, nil
}

// ClusterCheckResult aggregates the overall cluster state similar to "redis-cli --cluster check".
type ClusterCheckResult struct {
	CommandCodeOutput int
	Errors            []string
	Warnings          []string
}

// -----------------------------------------------------------------------------
// ClusterCheck executes "redis-cli --cluster check <addr>" and parses its output.
// -----------------------------------------------------------------------------
func (rc *RedisClient) ClusterCheck(ctx context.Context) (*ClusterCheckResult, error) {
	// Build the command: "redis-cli --cluster check <host:port>"
	addr := rc.client.Options().Addr // e.g., "host:port"
	cmd := exec.CommandContext(ctx,
		"redis-cli",
		"--cluster",
		"check",
		addr,
	)

	// Capture combined stdout/stderr so we can parse everything in one pass.
	output, err := cmd.CombinedOutput()

	// Safely retrieve the exit code, which might be unavailable if the command fails to start.
	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}

	if err != nil {
		// If the context was canceled or timed out, return immediately.
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("redis-cli command canceled or timed out: %w", ctx.Err())
		}

		// Otherwise, log the error but parse partial output so the caller can decide how to handle it.
		log.Printf(
			"Error executing 'redis-cli --cluster check %s': %v. Partial output:\n%s",
			addr,
			err,
			string(output),
		)
	}

	// Parse the CLI output (whether complete or partial) to fill a ClusterCheckResult.
	result := parseClusterCheckOutput(string(output))
	result.CommandCodeOutput = exitCode

	return result, nil
}

func parseClusterCheckOutput(output string) *ClusterCheckResult {
	scanner := bufio.NewScanner(strings.NewReader(output))

	result := &ClusterCheckResult{
		Errors:   []string{},
		Warnings: []string{},
	}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Capture errors
		if strings.Contains(line, "[ERR]") {
			if !slices.Contains(result.Errors, line) {
				result.Errors = append(result.Errors, line)
			}
		}

		// Capture general warnings
		if strings.Contains(line, "[WARNING]") {
			if !slices.Contains(result.Warnings, line) {
				result.Warnings = append(result.Warnings, line)
			}
		}
	}

	// Check for any scanning error.
	if err := scanner.Err(); err != nil {
		log.Printf("Error reading cluster check output: %v", err)
	}

	return result
}

func (rc *RedisClient) Close() error {
	return rc.client.Close()
}
