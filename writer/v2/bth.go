package writer

import (
	"bytes"
	"encoding/binary"
)

// BTH is a BTree-on-Heap builder. See MS-PST 2.3.2.
// Keys are unique; Insert rejects duplicates. Serialization is sorted by key.
type BTH struct {
	heap    *Heap
	keySize byte
	valSize byte
	ents    []bthKV
}

type bthKV struct {
	key []byte
	val []byte
}

func NewBTH(h *Heap, keySize, valSize byte) (*BTH, error) {
	if h == nil {
		return nil, invalidArg("heap", "nil heap")
	}
	switch keySize {
	case 2, 4, 8, 16:
	default:
		return nil, invalidArg("cbKey", "cbKey %d, want 2, 4, 8, or 16 (MS-PST %s)", keySize, SectionBTH)
	}
	if valSize == 0 {
		return nil, invalidArg("cbEnt", "cbEnt must be > 0 (MS-PST %s)", SectionBTH)
	}
	return &BTH{heap: h, keySize: keySize, valSize: valSize}, nil
}

func (b *BTH) Insert(key, val []byte) error {
	if b == nil {
		return invalidArg("bth", "nil BTH")
	}
	if len(key) != int(b.keySize) {
		return invalidArg("cbKey", "key %d bytes, want %d", len(key), b.keySize)
	}
	if len(val) != int(b.valSize) {
		return invalidArg("cbEnt", "value %d bytes, want %d", len(val), b.valSize)
	}
	for _, e := range b.ents {
		if bytes.Equal(e.key, key) {
			return invalidArg("key", "duplicate BTH key")
		}
	}
	b.ents = append(b.ents, bthKV{key: append([]byte(nil), key...), val: append([]byte(nil), val...)})
	return nil
}

func (b *BTH) Count() int {
	if b == nil {
		return 0
	}
	return len(b.ents)
}

func (b *BTH) leafCap() int {
	sz := int(b.keySize) + int(b.valSize)
	if sz == 0 {
		return 0
	}
	return HeapMaxAlloc / sz
}

func (b *BTH) indexCap() int {
	sz := int(b.keySize) + 4
	return HeapMaxAlloc / sz
}

func encodeBTHHeader(keySize, valSize, levels byte, root uint32) []byte {
	h := make([]byte, BTHHeaderSize)
	h[0] = HeapSigBTH
	h[1] = keySize
	h[2] = valSize
	h[3] = levels
	binary.LittleEndian.PutUint32(h[4:8], root)
	return h
}

func (b *BTH) packLeaf(ents []bthKV) []byte {
	es := int(b.keySize) + int(b.valSize)
	buf := make([]byte, len(ents)*es)
	for i, e := range ents {
		off := i * es
		copy(buf[off:], e.key)
		copy(buf[off+int(b.keySize):], e.val)
	}
	return buf
}

func (b *BTH) packIndex(keys [][]byte, children []uint32) []byte {
	es := int(b.keySize) + 4
	buf := make([]byte, len(keys)*es)
	for i := range keys {
		off := i * es
		copy(buf[off:], keys[i])
		binary.LittleEndian.PutUint32(buf[off+int(b.keySize):], children[i])
	}
	return buf
}

// Build allocates the BTH into the heap and returns the BTHHEADER HID.
func (b *BTH) Build() (uint32, error) {
	if b == nil || b.heap == nil {
		return 0, invalidArg("bth", "nil BTH")
	}
	if len(b.ents) == 0 {
		hid, err := b.heap.Allocate(encodeBTHHeader(b.keySize, b.valSize, 0, 0))
		if err != nil {
			return 0, err
		}
		b.heap.SetRoot(hid)
		return hid, nil
	}
	sorted := append([]bthKV(nil), b.ents...)
	sortBTH(sorted)
	cap := b.leafCap()
	if cap < 1 {
		return 0, limitErr("cbEnt", "BTH leaf entry does not fit HN (MS-PST %s)", SectionBTH)
	}
	var leafHIDs []uint32
	var leafKeys [][]byte
	for i := 0; i < len(sorted); i += cap {
		end := i + cap
		if end > len(sorted) {
			end = len(sorted)
		}
		hid, err := b.heap.Allocate(b.packLeaf(sorted[i:end]))
		if err != nil {
			return 0, err
		}
		leafHIDs = append(leafHIDs, hid)
		leafKeys = append(leafKeys, sorted[end-1].key)
	}
	var root uint32
	var levels byte
	if len(leafHIDs) == 1 {
		root = leafHIDs[0]
		levels = 0
	} else {
		hid, lv, err := b.buildIndex(leafHIDs, leafKeys)
		if err != nil {
			return 0, err
		}
		root = hid
		levels = lv
	}
	hh, err := b.heap.Allocate(encodeBTHHeader(b.keySize, b.valSize, levels, root))
	if err != nil {
		return 0, err
	}
	b.heap.SetRoot(hh)
	return hh, nil
}

func (b *BTH) buildIndex(hids []uint32, maxKeys [][]byte) (uint32, byte, error) {
	cap := b.indexCap()
	if cap < 1 {
		return 0, 0, limitErr("cbEnt", "BTH index entry does not fit HN (MS-PST %s)", SectionBTH)
	}
	var level byte
	for {
		level++
		if len(hids) <= cap {
			hid, err := b.heap.Allocate(b.packIndex(maxKeys, hids))
			return hid, level, err
		}
		var parentHIDs []uint32
		var parentKeys [][]byte
		for i := 0; i < len(hids); i += cap {
			end := i + cap
			if end > len(hids) {
				end = len(hids)
			}
			hid, err := b.heap.Allocate(b.packIndex(maxKeys[i:end], hids[i:end]))
			if err != nil {
				return 0, 0, err
			}
			parentHIDs = append(parentHIDs, hid)
			parentKeys = append(parentKeys, maxKeys[end-1])
		}
		hids, maxKeys = parentHIDs, parentKeys
	}
}

// compareBTHKey compares keys as unsigned little-endian integers (MS-PST 2.3.2).
func compareBTHKey(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := n - 1; i >= 0; i-- {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return 0
}

func sortBTH(ents []bthKV) {
	for i := 1; i < len(ents); i++ {
		j := i
		for j > 0 && compareBTHKey(ents[j].key, ents[j-1].key) < 0 {
			ents[j], ents[j-1] = ents[j-1], ents[j]
			j--
		}
	}
}

// BTHView is a committed BTH reopened from a HeapView.
type BTHView struct {
	heap    *HeapView
	keySize byte
	valSize byte
	levels  byte
	root    uint32
	header  uint32
}

func OpenBTH(h *HeapView, headerHID uint32) (*BTHView, error) {
	if h == nil {
		return nil, invalidArg("heap", "nil heap")
	}
	if headerHID == 0 {
		headerHID = h.Root()
	}
	raw, err := h.Read(headerHID)
	if err != nil {
		return nil, err
	}
	if len(raw) < BTHHeaderSize {
		return nil, invariant(SectionBTH, "BTHHEADER", "header %d bytes", len(raw))
	}
	if raw[0] != HeapSigBTH {
		return nil, invariant(SectionBTH, "bType", "got 0x%02x want 0x%02x", raw[0], HeapSigBTH)
	}
	v := &BTHView{
		heap:    h,
		keySize: raw[1],
		valSize: raw[2],
		levels:  raw[3],
		root:    binary.LittleEndian.Uint32(raw[4:8]),
		header:  headerHID,
	}
	return v, nil
}

func (v *BTHView) Levels() int {
	if v == nil {
		return 0
	}
	return int(v.levels)
}

func (v *BTHView) KeySize() int { return int(v.keySize) }
func (v *BTHView) ValSize() int { return int(v.valSize) }

func (v *BTHView) Lookup(key []byte) ([]byte, error) {
	if v == nil {
		return nil, invalidArg("bth", "nil BTH")
	}
	if len(key) != int(v.keySize) {
		return nil, invalidArg("cbKey", "key %d bytes, want %d", len(key), v.keySize)
	}
	if v.root == 0 {
		return nil, invalidArg("key", "BTH key not found")
	}
	return v.lookup(v.root, v.levels, key)
}

func (v *BTHView) lookup(hid uint32, level byte, key []byte) ([]byte, error) {
	raw, err := v.heap.Read(hid)
	if err != nil {
		return nil, err
	}
	if level == 0 {
		es := int(v.keySize) + int(v.valSize)
		if es == 0 || len(raw)%es != 0 {
			return nil, invariant(SectionBTH, "cbEnt", "leaf size %d not multiple of %d", len(raw), es)
		}
		for i := 0; i < len(raw); i += es {
			if bytes.Equal(raw[i:i+int(v.keySize)], key) {
				return append([]byte(nil), raw[i+int(v.keySize):i+es]...), nil
			}
		}
		return nil, invalidArg("key", "BTH key not found")
	}
	es := int(v.keySize) + 4
	if es == 0 || len(raw)%es != 0 {
		return nil, invariant(SectionBTH, "index", "index size %d not multiple of %d", len(raw), es)
	}
	child := uint32(0)
	for i := 0; i < len(raw); i += es {
		k := raw[i : i+int(v.keySize)]
		hid := binary.LittleEndian.Uint32(raw[i+int(v.keySize):])
		child = hid
		if compareBTHKey(key, k) <= 0 {
			break
		}
	}
	if child == 0 {
		return nil, invariant(SectionBTH, "hidNextLevel", "empty index node")
	}
	return v.lookup(child, level-1, key)
}

func (v *BTHView) Entries() ([]bthKV, error) {
	if v == nil {
		return nil, invalidArg("bth", "nil BTH")
	}
	if v.root == 0 {
		return nil, nil
	}
	return v.collect(v.root, v.levels)
}

func (v *BTHView) collect(hid uint32, level byte) ([]bthKV, error) {
	raw, err := v.heap.Read(hid)
	if err != nil {
		return nil, err
	}
	if level == 0 {
		es := int(v.keySize) + int(v.valSize)
		if es == 0 || len(raw)%es != 0 {
			return nil, invariant(SectionBTH, "cbEnt", "leaf size %d not multiple of %d", len(raw), es)
		}
		out := make([]bthKV, 0, len(raw)/es)
		for i := 0; i < len(raw); i += es {
			out = append(out, bthKV{
				key: append([]byte(nil), raw[i:i+int(v.keySize)]...),
				val: append([]byte(nil), raw[i+int(v.keySize):i+es]...),
			})
		}
		return out, nil
	}
	es := int(v.keySize) + 4
	if es == 0 || len(raw)%es != 0 {
		return nil, invariant(SectionBTH, "index", "index size %d not multiple of %d", len(raw), es)
	}
	var out []bthKV
	for i := 0; i < len(raw); i += es {
		child := binary.LittleEndian.Uint32(raw[i+int(v.keySize):])
		sub, err := v.collect(child, level-1)
		if err != nil {
			return nil, err
		}
		out = append(out, sub...)
	}
	return out, nil
}
