// Package storage is the node's local storage layer: bbolt, two top-level
// buckets ("data", "metrics"), key = raw object_id, value = serialized
// StoredObject.
package storage

import (
	"encoding/binary"
	"fmt"
	"sort"

	"go.etcd.io/bbolt"
	"google.golang.org/protobuf/proto"

	"sporemachine/internal/sporepb"
)

var (
	bucketData    = []byte("data")
	bucketMetrics = []byte("metrics")
	bucketMeta    = []byte("meta")

	keyObjectCount = []byte("n")
	keyTotalBytes  = []byte("b")
	keyFileSizeMax = []byte("fmax")
	keyFileSizeMin = []byte("fmin")
)

type Tree int

const (
	TreeData Tree = iota
	TreeMetrics
)

func (t Tree) bucket() []byte {
	if t == TreeMetrics {
		return bucketMetrics
	}
	return bucketData
}

type Store struct {
	db *bbolt.DB
}

func Open(path string) (*Store, error) {
	db, err := bbolt.Open(path, 0o600, nil)
	if err != nil {
		return nil, fmt.Errorf("storage: open %s: %w", path, err)
	}
	err = db.Update(func(tx *bbolt.Tx) error {
		for _, b := range [][]byte{bucketData, bucketMetrics, bucketMeta} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("storage: init buckets: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

// PutObject is idempotent: writing an existing id again does not double-count
// aggregates. isNew reports whether id was absent before this call — callers
// use it to decide whether this is genuinely new content (e.g. whether a
// COMMAND should be dispatched for execution) rather than a resubmission or a
// gossip/Merkle-sync redelivery of something already known.
func (s *Store) PutObject(tree Tree, id []byte, obj *sporepb.StoredObject) (isNew bool, err error) {
	data, err := proto.Marshal(obj)
	if err != nil {
		return false, fmt.Errorf("storage: marshal object: %w", err)
	}
	err = s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(tree.bucket())
		existing := b.Get(id)
		isNew = existing == nil
		if err := b.Put(id, data); err != nil {
			return err
		}
		if !isNew || tree != TreeData {
			return nil
		}
		meta := tx.Bucket(bucketMeta)
		if err := incrCounter(meta, keyObjectCount, 1); err != nil {
			return err
		}
		if err := incrCounter(meta, keyTotalBytes, int64(len(obj.GetContent()))); err != nil {
			return err
		}
		if obj.GetType() == sporepb.DataType_FILE {
			return updateFileSizeMinMax(meta, int64(len(obj.GetContent())))
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return isNew, nil
}

// ObjectToPut is one item of a batched write via PutObjects.
type ObjectToPut struct {
	ID  []byte
	Obj *sporepb.StoredObject
}

// PutObjects writes multiple objects in a single transaction, instead of one
// transaction per object (each fsync'd on commit) — meant for ingesting a
// batch fetched together (e.g. Merkle catch-up), where per-object
// transactions are the dominant cost. All-or-nothing: if any single write
// fails, the whole batch rolls back rather than partially applying.
// newFlags mirrors items and reports, per item, the same isNew semantics as
// PutObject.
func (s *Store) PutObjects(tree Tree, items []ObjectToPut) (newFlags []bool, err error) {
	if len(items) == 0 {
		return nil, nil
	}
	datas := make([][]byte, len(items))
	for i, it := range items {
		data, err := proto.Marshal(it.Obj)
		if err != nil {
			return nil, fmt.Errorf("storage: marshal object: %w", err)
		}
		datas[i] = data
	}
	newFlags = make([]bool, len(items))
	err = s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(tree.bucket())
		meta := tx.Bucket(bucketMeta)
		for i, it := range items {
			isNew := b.Get(it.ID) == nil
			newFlags[i] = isNew
			if err := b.Put(it.ID, datas[i]); err != nil {
				return err
			}
			if !isNew || tree != TreeData {
				continue
			}
			if err := incrCounter(meta, keyObjectCount, 1); err != nil {
				return err
			}
			if err := incrCounter(meta, keyTotalBytes, int64(len(it.Obj.GetContent()))); err != nil {
				return err
			}
			if it.Obj.GetType() == sporepb.DataType_FILE {
				if err := updateFileSizeMinMax(meta, int64(len(it.Obj.GetContent()))); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return newFlags, nil
}

func (s *Store) GetObject(tree Tree, id []byte) (*sporepb.StoredObject, error) {
	var out *sporepb.StoredObject
	err := s.db.View(func(tx *bbolt.Tx) error {
		raw := tx.Bucket(tree.bucket()).Get(id)
		if raw == nil {
			return nil
		}
		obj := &sporepb.StoredObject{}
		if err := proto.Unmarshal(raw, obj); err != nil {
			return err
		}
		out = obj
		return nil
	})
	return out, err
}

// LeafGroupIDs reads via Cursor.Seek(prefix) instead of a separate index —
// keys are raw object_id bytes, so one leaf group is a contiguous key range.
func (s *Store) LeafGroupIDs(tree Tree, group byte) ([][]byte, error) {
	var ids [][]byte
	err := s.db.View(func(tx *bbolt.Tx) error {
		c := tx.Bucket(tree.bucket()).Cursor()
		prefix := []byte{group}
		for k, _ := c.Seek(prefix); k != nil && k[0] == group; k, _ = c.Next() {
			id := make([]byte, len(k))
			copy(id, k)
			ids = append(ids, id)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(ids, func(i, j int) bool { return compareBytes(ids[i], ids[j]) < 0 })
	return ids, nil
}

func (s *Store) LeafProviderFor(tree Tree) leafProvider {
	return leafProvider{store: s, tree: tree}
}

type leafProvider struct {
	store *Store
	tree  Tree
}

func (lp leafProvider) LeafGroupIDs(group byte) ([][]byte, error) {
	return lp.store.LeafGroupIDs(lp.tree, group)
}

type Stats struct {
	ObjectCount int64
	TotalBytes  int64
}

func (s *Store) Stats() (Stats, error) {
	var st Stats
	err := s.db.View(func(tx *bbolt.Tx) error {
		meta := tx.Bucket(bucketMeta)
		st.ObjectCount = readCounter(meta, keyObjectCount)
		st.TotalBytes = readCounter(meta, keyTotalBytes)
		return nil
	})
	return st, err
}

func (s *Store) FileSizeMinMax() (min, max int64, err error) {
	err = s.db.View(func(tx *bbolt.Tx) error {
		meta := tx.Bucket(bucketMeta)
		max = readCounter(meta, keyFileSizeMax)
		min = readCounter(meta, keyFileSizeMin)
		return nil
	})
	return
}

func incrCounter(meta *bbolt.Bucket, key []byte, delta int64) error {
	v := readCounter(meta, key) + delta
	return putCounter(meta, key, v)
}

func readCounter(meta *bbolt.Bucket, key []byte) int64 {
	raw := meta.Get(key)
	if raw == nil {
		return 0
	}
	return int64(binary.BigEndian.Uint64(raw))
}

func putCounter(meta *bbolt.Bucket, key []byte, v int64) error {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, uint64(v))
	return meta.Put(key, buf)
}

// updateFileSizeMinMax only grows max / only shrinks min: storage is
// grow-only, so a full rescan is never needed.
func updateFileSizeMinMax(meta *bbolt.Bucket, size int64) error {
	curMax := readCounter(meta, keyFileSizeMax)
	curMin := readCounter(meta, keyFileSizeMin)
	firstWrite := meta.Get(keyFileSizeMax) == nil
	if firstWrite || size > curMax {
		if err := putCounter(meta, keyFileSizeMax, size); err != nil {
			return err
		}
	}
	if firstWrite || size < curMin {
		if err := putCounter(meta, keyFileSizeMin, size); err != nil {
			return err
		}
	}
	return nil
}

func compareBytes(a, b []byte) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return len(a) - len(b)
}
