package ltp

import (
	"encoding/binary"
	"fmt"
	"iter"

	"github.com/grokify/outlook-pst-go/pkg/disk"
	"github.com/grokify/outlook-pst-go/pkg/util"
)

// BTHHeader represents the header of a BTree-on-Heap.
// See [MS-PST] Section 2.3.2.1 - BTHHEADER structure.
// BTH provides a generic B-tree implementation stored within a Heap-on-Node.
type BTHHeader struct {
	Signature byte        // bType - Must be 0xB5
	KeySize   byte        // cbKey - Key size in bytes (2, 4, 8, or 16)
	EntrySize byte        // cbEnt - Key + value size in bytes (for leaf entries)
	NumLevels byte        // bIdxLevels - Tree depth (0 = empty, 1 = root is leaf)
	RootID    util.HeapID // hidRoot - HID of root allocation
}

// BTH represents a BTree-on-Heap.
// See [MS-PST] Section 2.3.2 - BTree-on-Heap (BTH).
// BTH is a B-tree structure stored within heap allocations, used for
// property lookups in Property Contexts and row indexing in Table Contexts.
type BTH struct {
	heap   *HeapOnNode
	header BTHHeader
}

// BTHEntry represents a key-value entry in the BTH.
// See [MS-PST] Section 2.3.2.3 - BTH Data Records.
type BTHEntry struct {
	Key   []byte // Key bytes (size defined in BTH header)
	Value []byte // Value bytes (size = EntrySize - KeySize)
}

// NewBTH creates a BTH from a heap and root HID.
func NewBTH(heap *HeapOnNode, rootHID util.HeapID) (*BTH, error) {
	data, err := heap.Read(rootHID)
	if err != nil {
		return nil, fmt.Errorf("failed to read BTH header: %w", err)
	}

	if len(data) < 8 {
		return nil, fmt.Errorf("BTH header too small: %d bytes", len(data))
	}

	bth := &BTH{
		heap: heap,
		header: BTHHeader{
			Signature: data[0],
			KeySize:   data[1],
			EntrySize: data[2],
			NumLevels: data[3],
			RootID:    util.HeapID(binary.LittleEndian.Uint32(data[4:8])),
		},
	}

	if bth.header.Signature != disk.HeapSigBTH {
		return nil, fmt.Errorf("invalid BTH signature: got 0x%02X, expected 0x%02X", bth.header.Signature, disk.HeapSigBTH)
	}

	return bth, nil
}

// IsEmpty returns true if the BTH is empty.
// Note: NumLevels indicates the number of INTERMEDIATE levels.
// NumLevels=0 means root contains leaf entries directly (not empty).
// The BTH is only empty if RootID is 0.
func (b *BTH) IsEmpty() bool {
	return b.header.RootID == 0
}

// KeySize returns the key size in bytes.
func (b *BTH) KeySize() int {
	return int(b.header.KeySize)
}

// ValueSize returns the value size in bytes.
// Note: EntrySize (cbEnt) is already the VALUE size, not key+value.
func (b *BTH) ValueSize() int {
	return int(b.header.EntrySize)
}

// Lookup finds a value by key.
func (b *BTH) Lookup(key []byte) ([]byte, error) {
	if b.IsEmpty() {
		return nil, fmt.Errorf("key not found: BTH is empty")
	}

	if len(key) != int(b.header.KeySize) {
		return nil, fmt.Errorf("invalid key size: got %d, expected %d", len(key), b.header.KeySize)
	}

	// NumLevels indicates intermediate levels (0 = root is leaf)
	return b.search(b.header.RootID, int(b.header.NumLevels), key)
}

// LookupUint16 looks up a value by uint16 key.
func (b *BTH) LookupUint16(key uint16) ([]byte, error) {
	keyBytes := make([]byte, 2)
	binary.LittleEndian.PutUint16(keyBytes, key)
	return b.Lookup(keyBytes)
}

// LookupUint32 looks up a value by uint32 key.
func (b *BTH) LookupUint32(key uint32) ([]byte, error) {
	keyBytes := make([]byte, 4)
	binary.LittleEndian.PutUint32(keyBytes, key)
	return b.Lookup(keyBytes)
}

// search performs a B-tree search starting from the given node.
func (b *BTH) search(hid util.HeapID, level int, key []byte) ([]byte, error) {
	data, err := b.heap.Read(hid)
	if err != nil {
		return nil, fmt.Errorf("failed to read BTH node: %w", err)
	}

	if level == 0 {
		// Leaf level - search for exact key match
		return b.searchLeaf(data, key)
	}

	// Non-leaf level - find child
	childHID, err := b.findChild(data, key)
	if err != nil {
		return nil, err
	}

	return b.search(childHID, level-1, key)
}

// searchLeaf searches for a key in a leaf node.
func (b *BTH) searchLeaf(data []byte, key []byte) ([]byte, error) {
	keySize := int(b.header.KeySize)
	valueSize := int(b.header.EntrySize) // cbEnt is the VALUE size, not full entry size
	entrySize := keySize + valueSize     // Full entry = key + value

	numEntries := len(data) / entrySize

	for i := 0; i < numEntries; i++ {
		offset := i * entrySize
		entryKey := data[offset : offset+keySize]

		// Full scan: leaves are ordered as unsigned LE integers (MS-PST 2.3.2),
		// which is not memcmp order for 2-byte PropIDs (e.g. 0x0FF9 vs 0x3001).
		if compareKeys(key, entryKey) == 0 {
			return data[offset+keySize : offset+keySize+valueSize], nil
		}
	}

	return nil, fmt.Errorf("key not found in BTH")
}

// findChild finds the appropriate child node for a key.
func (b *BTH) findChild(data []byte, key []byte) (util.HeapID, error) {
	keySize := int(b.header.KeySize)
	// Non-leaf entry size is key + HID (4 bytes)
	entrySize := keySize + 4

	numEntries := len(data) / entrySize

	for i := 0; i < numEntries; i++ {
		offset := i * entrySize
		entryKey := data[offset : offset+keySize]

		if compareKeys(key, entryKey) <= 0 {
			// Key is <= this entry's key, use this child
			hid := binary.LittleEndian.Uint32(data[offset+keySize : offset+keySize+4])
			return util.HeapID(hid), nil
		}
	}

	// Key is greater than all keys, use last child
	if numEntries > 0 {
		offset := (numEntries - 1) * entrySize
		hid := binary.LittleEndian.Uint32(data[offset+keySize : offset+keySize+4])
		return util.HeapID(hid), nil
	}

	return 0, fmt.Errorf("BTH node has no entries")
}

// compareKeys compares two keys lexicographically.
func compareKeys(a, b []byte) int {
	minLen := len(a)
	if len(b) < minLen {
		minLen = len(b)
	}
	for i := 0; i < minLen; i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
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

// Entries returns an iterator over all entries in the BTH.
func (b *BTH) Entries() iter.Seq2[BTHEntry, error] {
	return func(yield func(BTHEntry, error) bool) {
		if b.IsEmpty() {
			return
		}
		// NumLevels indicates intermediate levels (0 = root is leaf)
		b.iterateNode(b.header.RootID, int(b.header.NumLevels), yield)
	}
}

// iterateNode iterates over all entries in a BTH node.
func (b *BTH) iterateNode(hid util.HeapID, level int, yield func(BTHEntry, error) bool) bool {
	data, err := b.heap.Read(hid)
	if err != nil {
		yield(BTHEntry{}, err)
		return false
	}

	keySize := int(b.header.KeySize)
	valueSize := int(b.header.EntrySize) // cbEnt is VALUE size

	if level == 0 {
		// Leaf level - entry = key + value
		entrySize := keySize + valueSize
		numEntries := len(data) / entrySize

		for i := 0; i < numEntries; i++ {
			offset := i * entrySize
			entry := BTHEntry{
				Key:   data[offset : offset+keySize],
				Value: data[offset+keySize : offset+keySize+valueSize],
			}
			if !yield(entry, nil) {
				return false
			}
		}
		return true
	}

	// Non-leaf level - entry = key + HID (4 bytes)
	entrySize := keySize + 4
	numEntries := len(data) / entrySize

	for i := 0; i < numEntries; i++ {
		offset := i * entrySize
		childHID := util.HeapID(binary.LittleEndian.Uint32(data[offset+keySize : offset+keySize+4]))
		if !b.iterateNode(childHID, level-1, yield) {
			return false
		}
	}
	return true
}
