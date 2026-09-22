package merkle

import "testing"

// fakeLeafProvider serves fixed leaf-group contents for a fixed set of groups;
// groups not present return no IDs (empty leaf).
type fakeLeafProvider struct {
	groups map[byte][][]byte
}

func (f fakeLeafProvider) LeafGroupIDs(group byte) ([][]byte, error) {
	return f.groups[group], nil
}

func TestBuildRootDeterministic(t *testing.T) {
	lp := fakeLeafProvider{groups: map[byte][][]byte{
		0x00: {[]byte("a"), []byte("b")},
		0x3f: {[]byte("c")},
	}}

	t1, err := Build(lp)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t2, err := Build(lp)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if t1.Root() != t2.Root() {
		t.Fatal("Build: same leaves produced different roots across two builds")
	}
}

func TestRootChangesWhenALeafGroupChanges(t *testing.T) {
	base, err := Build(fakeLeafProvider{groups: map[byte][][]byte{
		0x00: {[]byte("a")},
	}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	changed, err := Build(fakeLeafProvider{groups: map[byte][][]byte{
		0x00: {[]byte("a"), []byte("extra")},
	}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if base.Root() == changed.Root() {
		t.Fatal("Root: changing one leaf group's IDs did not change the root")
	}
}

func TestLeafHashEmptyListReturnsEmptyHash(t *testing.T) {
	got := LeafHash(nil)
	if got != emptyHash {
		t.Fatal("LeafHash(nil): did not return emptyHash")
	}
	got = LeafHash([][]byte{})
	if got != emptyHash {
		t.Fatal("LeafHash([]): did not return emptyHash")
	}
}

func TestChildrenHashesMatchesInternalHashFolding(t *testing.T) {
	tree, err := Build(fakeLeafProvider{groups: map[byte][][]byte{
		0x00: {[]byte("a")},
		0x01: {[]byte("b")},
		0xff: {[]byte("c")},
	}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Root's children folded together must reproduce the root hash.
	root := Root()
	left, right := tree.ChildrenHashes(root)
	if got, want := InternalHash(left, right), tree.NodeHash(root); got != want {
		t.Fatalf("root children fold to %x, want %x", got, want)
	}

	// Same check one level down, on the root's own left child.
	leftPath, _ := root.Children()
	l2, r2 := tree.ChildrenHashes(leftPath)
	if got, want := InternalHash(l2, r2), tree.NodeHash(leftPath); got != want {
		t.Fatalf("left-child's children fold to %x, want %x", got, want)
	}
}

func TestPathChildrenReconstructsParentPrefix(t *testing.T) {
	p := Path{Depth: 3, Prefix: 5} // 0b101
	left, right := p.Children()

	if left.Depth != p.Depth+1 || right.Depth != p.Depth+1 {
		t.Fatalf("Children: depth = %d/%d, want %d", left.Depth, right.Depth, p.Depth+1)
	}
	if left.Prefix>>1 != p.Prefix || right.Prefix>>1 != p.Prefix {
		t.Fatalf("Children: prefixes %b/%b don't reconstruct parent prefix %b", left.Prefix, right.Prefix, p.Prefix)
	}
	if right.Prefix != left.Prefix|1 {
		t.Fatalf("Children: right prefix %b is not left prefix %b with low bit set", right.Prefix, left.Prefix)
	}
}
