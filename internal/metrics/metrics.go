// Package metrics collects, encrypts, and stores a periodic metrics snapshot.
package metrics

import (
	"context"
	"runtime"
	"time"

	"google.golang.org/protobuf/proto"

	"sporemachine/internal/commandexec"
	"sporemachine/internal/cryptoutil"
	"sporemachine/internal/gossipsync"
	"sporemachine/internal/membership"
	"sporemachine/internal/procstats"
	"sporemachine/internal/sporepb"
	"sporemachine/internal/storage"
)

const collectInterval = 3 * time.Minute

type Collector struct {
	nodeID       string
	createdAtUTC string
	parentID     string

	store  *storage.Store
	mem    *membership.Membership
	exec   *commandexec.Executor
	gossip *gossipsync.GossipSync

	metricsPublicKey [32]byte
	sampler          *procstats.Sampler
}

func New(
	nodeID, createdAtUTC, parentID string,
	store *storage.Store,
	mem *membership.Membership,
	exec *commandexec.Executor,
	gossip *gossipsync.GossipSync,
	metricsPublicKey [32]byte,
) *Collector {
	return &Collector{
		nodeID:           nodeID,
		createdAtUTC:     createdAtUTC,
		parentID:         parentID,
		store:            store,
		mem:              mem,
		exec:             exec,
		gossip:           gossip,
		metricsPublicKey: metricsPublicKey,
		sampler:          procstats.NewSampler(),
	}
}

// Cycle runs one collection cycle immediately, without waiting for the
// ticker. Exposed for tests — production code only reaches cycle() via Run.
func (c *Collector) Cycle() {
	c.cycle()
}

func (c *Collector) Run(ctx context.Context) {
	ticker := time.NewTicker(collectInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.cycle()
		}
	}
}

// cycle stores the encrypted snapshot via node-storage; no separate publish
// step is needed, since the next gossip round picks it up as a root-hash
// change like any other object.
func (c *Collector) cycle() {
	cpuPercent, rssBytes := c.sampler.Sample()
	counts := c.mem.Counts()
	cmdStats := c.exec.Stats()
	storeStats, err := c.store.Stats()
	if err != nil {
		return
	}
	fileMin, fileMax, err := c.store.FileSizeMinMax()
	if err != nil {
		return
	}
	sync := c.gossip.ResetAndRead()

	snap := &sporepb.MetricsSnapshot{
		NodeId:         c.nodeID,
		TimestampUnix:  time.Now().Unix(),
		CreatedAtUtc:   c.createdAtUTC,
		ParentId:       c.parentID,
		CpuPercent:     cpuPercent,
		MemoryRssBytes: rssBytes,
		Goroutines:     int64(runtime.NumGoroutine()),
		MembersAlive:   counts.Alive,
		MembersSuspect: counts.Suspect,
		MembersDead:    counts.Dead,
		Commands: &sporepb.CommandStats{
			Stored:       cmdStats.Stored,
			Succeeded:    cmdStats.Succeeded,
			Failed:       cmdStats.Failed,
			ExecMsMax:    cmdStats.ExecMsMax,
			ExecMsMin:    cmdStats.ExecMsMin,
			ExecMsAvg:    cmdStats.ExecMsAvg,
			ExecMsMedian: cmdStats.ExecMsMedian,
		},
		Storage: &sporepb.StorageStats{
			ObjectCount: storeStats.ObjectCount,
			TotalBytes:  storeStats.TotalBytes,
			FileSizeMax: fileMax,
			FileSizeMin: fileMin,
		},
		MerkleSync: &sporepb.MerkleSyncStats{
			Rounds:        sync.Rounds,
			ObjectsSynced: sync.ObjectsSynced,
			BytesSynced:   sync.BytesSynced,
		},
	}

	plaintext, err := proto.Marshal(snap)
	if err != nil {
		return
	}
	ciphertext, err := cryptoutil.EncryptMetrics(c.metricsPublicKey, plaintext)
	if err != nil {
		return
	}
	id := cryptoutil.ObjectID(ciphertext)
	obj := &sporepb.StoredObject{Content: ciphertext}
	_, _ = c.store.PutObject(storage.TreeMetrics, id, obj)
}
