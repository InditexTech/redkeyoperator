// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/go-redis/redismock/v9"
	"github.com/stretchr/testify/assert"
)

// TestNewRedisClient verifies the creation of a new RedisClient.
func TestNewRedisClient(t *testing.T) {
	ctx := context.Background()
	rc := NewRedisClient(ctx, "localhost", "", 0)

	assert.NotNil(t, rc)
	assert.Equal(t, "localhost:6379", rc.client.Options().Addr)
	assert.Equal(t, "", rc.client.Options().Password)
	assert.Equal(t, 0, rc.client.Options().DB)
}

// TestCheckConnection_Success ensures CheckConnection succeeds when the ping is valid.
func TestCheckConnection_Success(t *testing.T) {
	db, mock := redismock.NewClientMock()
	mock.ExpectPing().SetVal("PONG")

	rc := &RedisClient{
		client: db,
		ctx:    context.Background(),
	}

	err := rc.CheckConnection(3, 10)
	assert.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestParseRedisInfo ensures parseRedisInfo parses known sections correctly.
func TestParseRedisInfo(t *testing.T) {
	infoOutput := `
# Server
redis_version:7.2.4
redis_git_sha1:00000000
redis_git_dirty:0
redis_build_id:3a23a5ac67fe7008
redis_mode:cluster
os:Linux 5.14.0-427.47.1.el9_4.x86_64 x86_64
arch_bits:64
monotonic_clock:POSIX clock_gettime
multiplexing_api:epoll
atomicvar_api:c11-builtin
gcc_version:11.4.0
process_id:1
process_supervised:no
run_id:f47aa6a4a286e7cc0b4bf9bb4f3010fcf03604a7
tcp_port:6379
server_time_usec:1740669833903806
uptime_in_seconds:19231
uptime_in_days:0
hz:10
configured_hz:10
lru_clock:12616585
executable:/redis-server
config_file:/conf/redis.conf
io_threads_active:0
listener0:name=tcp,bind=*,bind=-::*,port=6379

# Clients
connected_clients:1
cluster_connections:8
maxclients:10000
client_recent_max_input_buffer:0
client_recent_max_output_buffer:0
blocked_clients:0
tracking_clients:0
clients_in_timeout_table:0
total_blocking_keys:0
total_blocking_keys_on_nokey:0

# Memory
used_memory:2092160
used_memory_human:2.00M
used_memory_rss:9621504
used_memory_rss_human:9.18M
used_memory_peak:2256432
used_memory_peak_human:2.15M
used_memory_peak_perc:92.72%
used_memory_overhead:1593192
used_memory_startup:1582488
used_memory_dataset:498968
used_memory_dataset_perc:97.90%
allocator_allocated:2391080
allocator_active:10027008
allocator_resident:26411008
total_system_memory:405239263232
total_system_memory_human:377.41G
used_memory_lua:31744
used_memory_vm_eval:31744
used_memory_lua_human:31.00K
used_memory_scripts_eval:0
number_of_cached_scripts:0
number_of_functions:0
number_of_libraries:0
used_memory_vm_functions:32768
used_memory_vm_total:64512
used_memory_vm_total_human:63.00K
used_memory_functions:184
used_memory_scripts:184
used_memory_scripts_human:184B
maxmemory:734003200
maxmemory_human:700.00M
maxmemory_policy:allkeys-lru
allocator_frag_ratio:4.19
allocator_frag_bytes:7635928
allocator_rss_ratio:2.63
allocator_rss_bytes:16384000
rss_overhead_ratio:0.36
rss_overhead_bytes:-16789504
mem_fragmentation_ratio:4.69
mem_fragmentation_bytes:7569472
mem_not_counted_for_evict:0
mem_replication_backlog:0
mem_total_replication_buffers:0
mem_clients_slaves:0
mem_clients_normal:0
mem_cluster_links:8576
mem_aof_buffer:0
mem_allocator:jemalloc-5.3.0
active_defrag_running:0
lazyfree_pending_objects:0
lazyfreed_objects:0

# Persistence
loading:0
async_loading:0
current_cow_peak:0
current_cow_size:0
current_cow_size_age:0
current_fork_perc:0.00
current_save_keys_processed:0
current_save_keys_total:0
rdb_changes_since_last_save:30
rdb_bgsave_in_progress:0
rdb_last_save_time:1740650602
rdb_last_bgsave_status:ok
rdb_last_bgsave_time_sec:0
rdb_current_bgsave_time_sec:-1
rdb_saves:0
rdb_last_cow_size:798720
rdb_last_load_keys_expired:0
rdb_last_load_keys_loaded:0
aof_enabled:0
aof_rewrite_in_progress:0
aof_rewrite_scheduled:0
aof_last_rewrite_time_sec:-1
aof_current_rewrite_time_sec:-1
aof_last_bgrewrite_status:ok
aof_rewrites:0
aof_rewrites_consecutive_failures:0
aof_last_write_status:ok
aof_last_cow_size:0
module_fork_in_progress:0
module_fork_last_cow_size:0

# Stats
total_connections_received:14633
total_commands_processed:98866
instantaneous_ops_per_sec:0
total_net_input_bytes:7946281
total_net_output_bytes:12668546
total_net_repl_input_bytes:713
total_net_repl_output_bytes:1956
instantaneous_input_kbps:0.00
instantaneous_output_kbps:0.00
instantaneous_input_repl_kbps:0.00
instantaneous_output_repl_kbps:0.00
rejected_connections:0
sync_full:6
sync_partial_ok:1
sync_partial_err:6
expired_keys:0
expired_stale_perc:0.00
expired_time_cap_reached_count:0
expire_cycle_cpu_milliseconds:96
evicted_keys:0
evicted_clients:0
total_eviction_exceeded_time:0
current_eviction_exceeded_time:0
keyspace_hits:0
keyspace_misses:0
pubsub_channels:0
pubsub_patterns:0
pubsubshard_channels:0
latest_fork_usec:370
total_forks:3
migrate_cached_sockets:0
slave_expires_tracked_keys:0
active_defrag_hits:0
active_defrag_misses:0
active_defrag_key_hits:0
active_defrag_key_misses:0
total_active_defrag_time:0
current_active_defrag_time:0
tracking_total_keys:0
tracking_total_items:0
tracking_total_prefixes:0
unexpected_error_replies:0
total_error_replies:36
dump_payload_sanitizations:0
total_reads_processed:112577
total_writes_processed:97810
io_threaded_reads_processed:0
io_threaded_writes_processed:0
reply_buffer_shrinks:864
reply_buffer_expands:0
eventloop_cycles:421856
eventloop_duration_sum:59356127
eventloop_duration_cmd_sum:1366174
instantaneous_eventloop_cycles_per_sec:11
instantaneous_eventloop_duration_usec:164
acl_access_denied_auth:0
acl_access_denied_cmd:0
acl_access_denied_key:0
acl_access_denied_channel:0

# Replication
role:master
connected_slaves:0
master_failover_state:no-failover
master_replid:4309a4210ad7d278441b9b461c2964d489cbed88
master_replid2:0000000000000000000000000000000000000000
master_repl_offset:238
second_repl_offset:-1
repl_backlog_active:0
repl_backlog_size:1048576
repl_backlog_first_byte_offset:0
repl_backlog_histlen:0

# CPU
used_cpu_sys:27.747819
used_cpu_user:31.339997
used_cpu_sys_children:0.003594
used_cpu_user_children:0.000000
used_cpu_sys_main_thread:27.722297
used_cpu_user_main_thread:31.311569

# Modules

# Commandstats
cmdstat_ping:calls=3888,usec=3159,usec_per_call=0.81,rejected_calls=0,failed_calls=0
cmdstat_cluster|info:calls=666,usec=59767,usec_per_call=89.74,rejected_calls=0,failed_calls=0
cmdstat_cluster|reset:calls=1,usec=778,usec_per_call=778.00,rejected_calls=0,failed_calls=0
cmdstat_cluster|myid:calls=2605,usec=4326,usec_per_call=1.66,rejected_calls=0,failed_calls=0
cmdstat_cluster|nodes:calls=9128,usec=836899,usec_per_call=91.68,rejected_calls=0,failed_calls=0
cmdstat_cluster|getkeysinslot:calls=8742,usec=11158,usec_per_call=1.28,rejected_calls=0,failed_calls=0
cmdstat_cluster|addslots:calls=1,usec=176,usec_per_call=176.00,rejected_calls=0,failed_calls=0
cmdstat_cluster|meet:calls=188,usec=4091,usec_per_call=21.76,rejected_calls=0,failed_calls=0
cmdstat_cluster|forget:calls=28,usec=2542,usec_per_call=90.79,rejected_calls=0,failed_calls=8
cmdstat_cluster|count-failure-reports:calls=1589,usec=4121,usec_per_call=2.59,rejected_calls=0,failed_calls=0
cmdstat_cluster|setslot:calls=67778,usec=284612,usec_per_call=4.20,rejected_calls=0,failed_calls=9
cmdstat_set:calls=30,usec=139,usec_per_call=4.63,rejected_calls=19,failed_calls=0
cmdstat_hello:calls=951,usec=5237,usec_per_call=5.51,rejected_calls=0,failed_calls=0
cmdstat_info:calls=696,usec=146500,usec_per_call=210.49,rejected_calls=0,failed_calls=0
cmdstat_psync:calls=7,usec=194,usec_per_call=27.71,rejected_calls=0,failed_calls=0
cmdstat_client|setinfo:calls=1902,usec=1351,usec_per_call=0.71,rejected_calls=0,failed_calls=0
cmdstat_dbsize:calls=373,usec=423,usec_per_call=1.13,rejected_calls=0,failed_calls=0
cmdstat_replconf:calls=293,usec=701,usec_per_call=2.39,rejected_calls=0,failed_calls=0

# Errorstats
errorstat_ERR:count=17
errorstat_MOVED:count=19

# Latencystats
latency_percentiles_usec_ping:p50=1.003,p99=3.007,p99.9=14.015
latency_percentiles_usec_cluster|info:p50=112.127,p99=163.839,p99.9=197.631
latency_percentiles_usec_cluster|reset:p50=778.239,p99=778.239,p99.9=778.239
latency_percentiles_usec_cluster|myid:p50=1.003,p99=5.023,p99.9=21.119
latency_percentiles_usec_cluster|nodes:p50=108.031,p99=161.791,p99.9=440.319
latency_percentiles_usec_cluster|getkeysinslot:p50=1.003,p99=4.015,p99.9=15.039
latency_percentiles_usec_cluster|addslots:p50=176.127,p99=176.127,p99.9=176.127
latency_percentiles_usec_cluster|meet:p50=25.087,p99=49.151,p99.9=64.255
latency_percentiles_usec_cluster|forget:p50=28.031,p99=346.111,p99.9=346.111
latency_percentiles_usec_cluster|count-failure-reports:p50=2.007,p99=9.023,p99.9=28.031
latency_percentiles_usec_cluster|setslot:p50=2.007,p99=38.143,p99.9=65.023
latency_percentiles_usec_set:p50=4.015,p99=27.007,p99.9=27.007
latency_percentiles_usec_hello:p50=5.023,p99=16.063,p99.9=35.071
latency_percentiles_usec_info:p50=167.935,p99=626.687,p99.9=684.031
latency_percentiles_usec_psync:p50=29.055,p99=54.015,p99.9=54.015
latency_percentiles_usec_client|setinfo:p50=1.003,p99=3.007,p99.9=3.007
latency_percentiles_usec_dbsize:p50=1.003,p99=2.007,p99.9=13.055
latency_percentiles_usec_replconf:p50=2.007,p99=4.015,p99.9=54.015

# Cluster
cluster_enabled:1

# Keyspace
db0:keys=30,expires=0,avg_ttl=0
`

	parsed := parseRedisInfo(infoOutput)

	// Table-driven tests for different sections and keys.
	tests := []struct {
		section  string
		key      string
		expected interface{}
	}{
		{"server", "redis_version", "7.2.4"},
		{"clients", "connected_clients", int64(1)},
		{"memory", "used_memory", 2092160},
		{"stats", "total_connections_received", int64(14633)},
		{"replication", "role", "master"},
		{"cpu", "used_cpu_sys", 27.747819},
		{"cluster", "cluster_enabled", "1"},
		{"keyspace", "db0", "keys=30,expires=0,avg_ttl=0"},
		{"errorstats", "errorstat_ERR", "count=17"},
		{"errorstats", "errorstat_MOVED", "count=19"},
		{"latencystats", "latency_percentiles_usec_ping", "p50=1.003,p99=3.007,p99.9=14.015"},
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("%s-%s", tc.section, tc.key), func(t *testing.T) {
			var actual interface{}
			switch tc.section {
			case "server":
				actual = parsed.Server[tc.key]
			case "clients":
				actual = parsed.Clients[tc.key]
			case "memory":
				actual = parsed.Memory[tc.key]
			case "stats":
				actual = parsed.Stats[tc.key]
			case "replication":
				actual = parsed.Replication[tc.key]
			case "cpu":
				actual = parsed.CPU[tc.key]
			case "cluster":
				actual = parsed.Cluster[tc.key]
			case "keyspace":
				actual = parsed.Keyspace[tc.key]
			case "errorstats":
				actual = parsed.ErrorStats[tc.key]
			case "latencystats":
				actual = parsed.LatencyStats[tc.key]
			default:
				t.Fatalf("Unknown section: %s", tc.section)
			}
			assert.Equal(t, tc.expected, actual)
		})
	}
}

// TestGetInfo_Success ensures GetInfo returns parsed info when Redis server responds properly.
func TestGetInfo_Success(t *testing.T) {
	db, mock := redismock.NewClientMock()
	infoOutput := "# Server\nredis_version:6.2.5\n"
	mock.ExpectInfo("all").SetVal(infoOutput)

	rc := &RedisClient{
		client: db,
		ctx:    context.Background(),
	}

	info, err := rc.GetInfo()
	assert.NoError(t, err)
	assert.NotNil(t, info)
	assert.Equal(t, "6.2.5", info.Server["redis_version"])
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestGetInfo_Fail ensures GetInfo returns an error when Info command fails.
func TestGetInfo_Fail(t *testing.T) {
	db, mock := redismock.NewClientMock()
	mock.ExpectInfo("all").SetErr(errors.New("failed info"))

	rc := &RedisClient{
		client: db,
		ctx:    context.Background(),
	}

	info, err := rc.GetInfo()
	assert.Error(t, err)
	assert.Nil(t, info)
	assert.Contains(t, err.Error(), "failed to get info")
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestGetClusterInfo_Success ensures GetClusterInfo returns properly parsed cluster info.
func TestGetClusterInfo_Success(t *testing.T) {
	db, mock := redismock.NewClientMock()
	mock.ExpectClusterInfo().SetVal(`
cluster_state:ok
cluster_slots_assigned:16384
cluster_slots_ok:16384
cluster_slots_pfail:0
cluster_slots_fail:0
cluster_known_nodes:5
cluster_size:5
cluster_current_epoch:13877
cluster_my_epoch:13877
cluster_stats_messages_ping_sent:19544
cluster_stats_messages_pong_sent:52643
cluster_stats_messages_meet_sent:187
cluster_stats_messages_update_sent:6
cluster_stats_messages_sent:72380
cluster_stats_messages_ping_received:19675
cluster_stats_messages_pong_received:72169
cluster_stats_messages_meet_received:188
cluster_stats_messages_fail_received:1
cluster_stats_messages_update_received:4
cluster_stats_messages_received:92037
total_cluster_links_buffer_limit_exceeded:0
`)

	rc := &RedisClient{
		client: db,
		ctx:    context.Background(),
	}

	cInfo, err := rc.GetClusterInfo()
	assert.NoError(t, err)
	assert.NotNil(t, cInfo)
	assert.Equal(t, "ok", cInfo.State)
	assert.Equal(t, 16384, cInfo.SlotsAssigned)
	assert.Equal(t, 5, cInfo.KnownNodes)
	assert.Equal(t, 5, cInfo.ClusterSize)
	assert.Equal(t, 13877, cInfo.CurrentEpoch)
	assert.Equal(t, 13877, cInfo.MyEpoch)
	assert.Equal(t, 19544, cInfo.MessagesPingSent)
	assert.Equal(t, 52643, cInfo.MessagesPongSent)
	assert.Equal(t, 187, cInfo.MessagesMeetSent)
	assert.Equal(t, 6, cInfo.MessagesUpdateSent)
	assert.Equal(t, 72380, cInfo.MessagesSent)
	assert.Equal(t, 19675, cInfo.MessagesPingReceived)
	assert.Equal(t, 72169, cInfo.MessagesPongReceived)
	assert.Equal(t, 188, cInfo.MessagesMeetReceived)
	assert.Equal(t, 4, cInfo.MessagesUpdateReceived)
	assert.Equal(t, 92037, cInfo.MessagesReceived)
	assert.Equal(t, 1, cInfo.MessagesFailReceived)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestGetClusterInfo_Success ensures GetClusterInfo returns properly parsed cluster info.
func TestGetClusterNodes_Success(t *testing.T) {
	db, mock := redismock.NewClientMock()
	mock.ExpectClusterNodes().SetVal(`
222d03eb91487e6542cff1e105d911deb37a5ddd 10.253.43.143:6379@16379 master - 0 1740670560026 13876 connected 9828-10923 12560-13103 14744-16383
77e5805a3550270e5cf23ed42bc2d0577426d876 10.252.6.201:6379@16379 myself,master - 0 1740670560000 13877 connected 2456-3275 4912-6277 7914-7917 8738-9553 9558-9827
bb1704c223955cf9a533142e4569f7aba510b1ea 10.252.26.193:6379@16379 master - 0 1740670561031 13846 connected 0-815 9554-9557 10924-11739 13104-14743
e420256dda2dbfb8db95658397ca8af3c3889b31 10.253.21.209:6379@16379 master - 0 1740670562035 13852 connected 816-1635 3276-4091 6278-7097 11740-12559
0d691cdfe68b44134f8cdbca0d81563754a5aa6f 10.252.8.20:6379@16379 master - 0 1740670559023 13874 connected 1636-2455 4092-4911 7098-7913 7918-8737
`)

	rc := &RedisClient{
		client: db,
		ctx:    context.Background(),
	}

	cNodes, err := rc.GetNodesInfo()
	assert.NoError(t, err)
	assert.NotNil(t, cNodes)
	assert.Len(t, cNodes, 5)
	assert.Equal(t, "222d03eb91487e6542cff1e105d911deb37a5ddd", cNodes[0].ID)
	assert.Equal(t, "77e5805a3550270e5cf23ed42bc2d0577426d876", cNodes[1].ID)
	assert.Equal(t, "bb1704c223955cf9a533142e4569f7aba510b1ea", cNodes[2].ID)
	assert.Equal(t, "e420256dda2dbfb8db95658397ca8af3c3889b31", cNodes[3].ID)
	assert.Equal(t, "0d691cdfe68b44134f8cdbca0d81563754a5aa6f", cNodes[4].ID)
	assert.Equal(t, "10.253.43.143", cNodes[0].IP)
	assert.Equal(t, "master", cNodes[0].Role)
	assert.Equal(t, []string([]string{"9828-10923", "12560-13103", "14744-16383"}), cNodes[0].Slots)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestGetClusterInfo_Fail ensures GetClusterInfo returns an error when ClusterInfo command fails.
func TestGetClusterInfo_Fail(t *testing.T) {
	db, mock := redismock.NewClientMock()
	mock.ExpectClusterInfo().SetErr(errors.New("some error"))

	rc := &RedisClient{
		client: db,
		ctx:    context.Background(),
	}

	cInfo, err := rc.GetClusterInfo()
	assert.Error(t, err)
	assert.Nil(t, cInfo)
	assert.Contains(t, err.Error(), "some error")
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestGetNodesInfo_Success ensures GetNodesInfo properly parses cluster node info.
func TestGetNodesInfo_Success(t *testing.T) {
	// Example from "redis-cli cluster nodes" output
	clusterNodes := `
abc123 127.0.0.1:6379 master - 0 1625161000000 1 connected 0-5460
abc456 127.0.0.1:6380 myself,slave abc123 0 1625161005000 2 connected
`

	db, mock := redismock.NewClientMock()
	mock.ExpectClusterNodes().SetVal(clusterNodes)

	// We'll ignore the ClusterCountFailureReports calls for this example test
	// or optionally set an expectation to return 0 for each node's failure count
	mock.ExpectClusterCountFailureReports("abc123").SetVal(0)
	mock.ExpectClusterCountFailureReports("abc456").SetVal(0)

	rc := &RedisClient{
		client: db,
		ctx:    context.Background(),
	}

	nodes, err := rc.GetNodesInfo()
	assert.NoError(t, err)
	assert.Len(t, nodes, 2)

	assert.Equal(t, "abc123", nodes[0].ID)
	assert.Equal(t, "127.0.0.1", nodes[0].IP)
	assert.Equal(t, "master", nodes[0].Role)

	assert.Equal(t, "abc456", nodes[1].ID)
	assert.Equal(t, "127.0.0.1", nodes[1].IP)
	assert.Contains(t, nodes[1].Role, "slave") // "myself,slave"
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestGetNodesInfo_Fail ensures GetNodesInfo returns an error when ClusterNodes command fails.
func TestGetNodesInfo_Fail(t *testing.T) {
	db, mock := redismock.NewClientMock()
	mock.ExpectClusterNodes().SetErr(errors.New("failed cluster nodes"))

	rc := &RedisClient{
		client: db,
		ctx:    context.Background(),
	}

	nodes, err := rc.GetNodesInfo()
	assert.Error(t, err)
	assert.Nil(t, nodes)
	assert.Contains(t, err.Error(), "failed cluster nodes")
	assert.NoError(t, mock.ExpectationsWereMet())
}
