// Package merkle implements a fixed-depth binary hash tree with batched
// per-level comparison.
package merkle

import "crypto/sha256"

const Depth = 8 // 256 leaf groups, grouped by an object's first hash byte

type Path struct {
	Depth  uint32
	Prefix uint32
}

func Root() Path { return Path{Depth: 0, Prefix: 0} }

func (p Path) Children() (left, right Path) {
	left = Path{Depth: p.Depth + 1, Prefix: p.Prefix << 1}
	right = Path{Depth: p.Depth + 1, Prefix: (p.Prefix << 1) | 1}
	return
}

type LeafProvider interface {
	LeafGroupIDs(group byte) ([][]byte, error) // sorted
}

var emptyHash = sha256.Sum256(nil)

func LeafHash(sortedIDs [][]byte) [32]byte {
	if len(sortedIDs) == 0 {
		return emptyHash
	}
	h := sha256.New()
	for _, id := range sortedIDs {
		h.Write(id)
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func InternalHash(left, right [32]byte) [32]byte {
	h := sha256.New()
	h.Write(left[:])
	h.Write(right[:])
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// Tree is a full in-memory snapshot, rebuilt per walk rather than kept
// incrementally up to date.
type Tree struct {
	levels [][]([32]byte) // levels[d][prefix]
}

func Build(lp LeafProvider) (*Tree, error) {
	leaves := make([]([32]byte), 1<<Depth)
	for g := range 1 << Depth {
		ids, err := lp.LeafGroupIDs(byte(g))
		if err != nil {
			return nil, err
		}
		leaves[g] = LeafHash(ids)
	}

	levels := make([][]([32]byte), Depth+1)
	levels[Depth] = leaves
	for d := int(Depth) - 1; d >= 0; d-- {
		cur := make([]([32]byte), 1<<d)
		child := levels[d+1]
		for i := range cur {
			cur[i] = InternalHash(child[2*i], child[2*i+1])
		}
		levels[d] = cur
	}
	return &Tree{levels: levels}, nil
}

func (t *Tree) Root() [32]byte {
	return t.levels[0][0]
}

func (t *Tree) NodeHash(p Path) [32]byte {
	return t.levels[p.Depth][p.Prefix]
}

func (t *Tree) ChildrenHashes(p Path) (left, right [32]byte) {
	l, r := p.Children()
	return t.levels[l.Depth][l.Prefix], t.levels[r.Depth][r.Prefix]
}
