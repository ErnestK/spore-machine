package storage

import (
	"path/filepath"
	"testing"

	"sporemachine/internal/sporepb"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "store.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestPutGetObjectRoundTrip(t *testing.T) {
	for _, tree := range []Tree{TreeData, TreeMetrics} {
		s := openTestStore(t)
		id := []byte{0x01, 0x02, 0x03}
		obj := &sporepb.StoredObject{Type: sporepb.DataType_COMMAND, Content: []byte("echo hi"), Signature: []byte("sig")}

		if _, err := s.PutObject(tree, id, obj); err != nil {
			t.Fatalf("PutObject: %v", err)
		}
		got, err := s.GetObject(tree, id)
		if err != nil {
			t.Fatalf("GetObject: %v", err)
		}
		if got == nil {
			t.Fatalf("GetObject returned nil after Put")
		}
		if got.GetType() != obj.GetType() || string(got.GetContent()) != string(obj.GetContent()) || string(got.GetSignature()) != string(obj.GetSignature()) {
			t.Fatalf("round-trip mismatch: got %+v, want %+v", got, obj)
		}
	}
}

func TestPutObjectDuplicateDoesNotDoubleCountAggregates(t *testing.T) {
	s := openTestStore(t)
	id := []byte{0xaa}
	obj := &sporepb.StoredObject{Type: sporepb.DataType_COMMAND, Content: []byte("payload")}

	isNew, err := s.PutObject(TreeData, id, obj)
	if err != nil {
		t.Fatalf("first PutObject: %v", err)
	}
	if !isNew {
		t.Errorf("first PutObject: isNew = false, want true")
	}
	isNew, err = s.PutObject(TreeData, id, obj)
	if err != nil {
		t.Fatalf("second PutObject: %v", err)
	}
	if isNew {
		t.Errorf("second PutObject (duplicate id): isNew = true, want false")
	}

	st, err := s.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if st.ObjectCount != 1 {
		t.Errorf("ObjectCount = %d, want 1 (duplicate put must not double-count)", st.ObjectCount)
	}
	if st.TotalBytes != int64(len(obj.GetContent())) {
		t.Errorf("TotalBytes = %d, want %d", st.TotalBytes, len(obj.GetContent()))
	}
}

func TestPutObjectMetricsTreeDoesNotAffectAggregates(t *testing.T) {
	s := openTestStore(t)
	before, err := s.Stats()
	if err != nil {
		t.Fatalf("Stats before: %v", err)
	}

	obj := &sporepb.StoredObject{Type: sporepb.DataType_FILE, Content: []byte("metrics-snapshot-bytes")}
	if _, err := s.PutObject(TreeMetrics, []byte{0x77}, obj); err != nil {
		t.Fatalf("PutObject(TreeMetrics): %v", err)
	}

	after, err := s.Stats()
	if err != nil {
		t.Fatalf("Stats after: %v", err)
	}
	if after != before {
		t.Errorf("Stats changed after a metrics-only put: before=%+v after=%+v", before, after)
	}
}

func TestLeafGroupIDsPartitionsByFirstByte(t *testing.T) {
	s := openTestStore(t)
	ids := [][]byte{
		{0x01, 0x01}, {0x01, 0x02}, {0x01, 0x00},
		{0x02, 0x05},
		{0xff, 0x00},
	}
	for _, id := range ids {
		if _, err := s.PutObject(TreeData, id, &sporepb.StoredObject{Content: []byte("x")}); err != nil {
			t.Fatalf("PutObject(%x): %v", id, err)
		}
	}

	group01, err := s.LeafGroupIDs(TreeData, 0x01)
	if err != nil {
		t.Fatalf("LeafGroupIDs(0x01): %v", err)
	}
	wantGroup01 := [][]byte{{0x01, 0x00}, {0x01, 0x01}, {0x01, 0x02}}
	if !equalIDLists(group01, wantGroup01) {
		t.Errorf("LeafGroupIDs(0x01) = %x, want %x", group01, wantGroup01)
	}

	group02, err := s.LeafGroupIDs(TreeData, 0x02)
	if err != nil {
		t.Fatalf("LeafGroupIDs(0x02): %v", err)
	}
	if !equalIDLists(group02, [][]byte{{0x02, 0x05}}) {
		t.Errorf("LeafGroupIDs(0x02) = %x, want [0205]", group02)
	}

	groupEmpty, err := s.LeafGroupIDs(TreeData, 0x03)
	if err != nil {
		t.Fatalf("LeafGroupIDs(0x03): %v", err)
	}
	if len(groupEmpty) != 0 {
		t.Errorf("LeafGroupIDs(0x03) = %x, want empty", groupEmpty)
	}
}

func equalIDLists(got, want [][]byte) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if string(got[i]) != string(want[i]) {
			return false
		}
	}
	return true
}

func TestUpdateFileSizeMinMaxViaPutObject(t *testing.T) {
	s := openTestStore(t)
	sizes := []int64{50, 20, 80, 60}
	wantMin, wantMax := []int64{50, 20, 20, 20}, []int64{50, 50, 80, 80}

	for i, size := range sizes {
		content := make([]byte, size)
		obj := &sporepb.StoredObject{Type: sporepb.DataType_FILE, Content: content}
		id := []byte{byte(i)}
		if _, err := s.PutObject(TreeData, id, obj); err != nil {
			t.Fatalf("PutObject step %d: %v", i, err)
		}
		min, max, err := s.FileSizeMinMax()
		if err != nil {
			t.Fatalf("FileSizeMinMax step %d: %v", i, err)
		}
		if min != wantMin[i] || max != wantMax[i] {
			t.Errorf("step %d: FileSizeMinMax = (min=%d, max=%d), want (min=%d, max=%d)", i, min, max, wantMin[i], wantMax[i])
		}
	}
}

func TestPutObjectsWritesWholeBatchInOneCall(t *testing.T) {
	s := openTestStore(t)
	items := []ObjectToPut{
		{ID: []byte{0x01}, Obj: &sporepb.StoredObject{Type: sporepb.DataType_FILE, Content: []byte("aaa")}},
		{ID: []byte{0x02}, Obj: &sporepb.StoredObject{Type: sporepb.DataType_FILE, Content: []byte("bb")}},
		{ID: []byte{0x03}, Obj: &sporepb.StoredObject{Type: sporepb.DataType_COMMAND, Content: []byte("c")}},
	}

	newFlags, err := s.PutObjects(TreeData, items)
	if err != nil {
		t.Fatalf("PutObjects: %v", err)
	}
	if len(newFlags) != len(items) {
		t.Fatalf("len(newFlags) = %d, want %d", len(newFlags), len(items))
	}
	for i, isNew := range newFlags {
		if !isNew {
			t.Errorf("item %d: isNew = false, want true (first write)", i)
		}
	}

	for _, it := range items {
		got, err := s.GetObject(TreeData, it.ID)
		if err != nil || got == nil {
			t.Fatalf("GetObject(%x): err=%v got=%v", it.ID, err, got)
		}
		if string(got.GetContent()) != string(it.Obj.GetContent()) {
			t.Errorf("GetObject(%x) content = %q, want %q", it.ID, got.GetContent(), it.Obj.GetContent())
		}
	}

	st, err := s.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	wantBytes := int64(len("aaa") + len("bb") + len("c"))
	if st.ObjectCount != 3 || st.TotalBytes != wantBytes {
		t.Errorf("Stats = %+v, want ObjectCount=3 TotalBytes=%d", st, wantBytes)
	}
}

func TestPutObjectsDuplicateWithinBatchOnlyCountsFirstAsNew(t *testing.T) {
	s := openTestStore(t)
	id := []byte{0xaa}
	obj := &sporepb.StoredObject{Type: sporepb.DataType_FILE, Content: []byte("x")}

	newFlags, err := s.PutObjects(TreeData, []ObjectToPut{{ID: id, Obj: obj}, {ID: id, Obj: obj}})
	if err != nil {
		t.Fatalf("PutObjects: %v", err)
	}
	if !newFlags[0] || newFlags[1] {
		t.Errorf("newFlags = %v, want [true false] (second occurrence of the same id is not new)", newFlags)
	}

	st, err := s.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if st.ObjectCount != 1 {
		t.Errorf("ObjectCount = %d, want 1 (duplicate id within one batch must not double-count)", st.ObjectCount)
	}
}

func TestPutObjectsEmptyBatchIsANoOp(t *testing.T) {
	s := openTestStore(t)
	newFlags, err := s.PutObjects(TreeData, nil)
	if err != nil {
		t.Fatalf("PutObjects(nil): %v", err)
	}
	if len(newFlags) != 0 {
		t.Errorf("newFlags = %v, want empty", newFlags)
	}
}

func TestEmptyStoreStatsAndFileSizeMinMaxAreZero(t *testing.T) {
	s := openTestStore(t)

	st, err := s.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if st.ObjectCount != 0 || st.TotalBytes != 0 {
		t.Errorf("Stats on empty store = %+v, want zero value", st)
	}

	min, max, err := s.FileSizeMinMax()
	if err != nil {
		t.Fatalf("FileSizeMinMax: %v", err)
	}
	if min != 0 || max != 0 {
		t.Errorf("FileSizeMinMax on empty store = (%d, %d), want (0, 0)", min, max)
	}
}
