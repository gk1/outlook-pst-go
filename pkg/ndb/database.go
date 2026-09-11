// Package ndb provides the Node Database (NDB) layer for PST files.
// The NDB layer sits between the disk layer and the LTP layer, providing
// node and block access abstractions over the raw file format.
// See [MS-PST] Section 2.2.2 for the NDB layer specification.
//
// [MS-PST]: https://docs.microsoft.com/en-us/openspecs/office_file_formats/ms-pst/
package ndb

import (
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/grokify/outlook-pst-go/pkg/disk"
	"github.com/grokify/outlook-pst-go/pkg/util"
)

// OpenMode represents how the PST file is opened.
type OpenMode int

const (
	// OpenModeReadOnly opens the file for reading only (default).
	OpenModeReadOnly OpenMode = iota
	// OpenModeReadWrite opens the file for reading and writing.
	OpenModeReadWrite
)

// Database represents an open PST database.
// It provides access to the Node B-tree (NBT) and Block B-tree (BBT).
// See [MS-PST] Section 1.3.2 for the NDB layer overview.
type Database struct {
	file   *os.File
	header *disk.Header

	// B-tree roots (lazy loaded)
	// See [MS-PST] Section 2.2.2.7.7 for B-tree page structures
	nbtRoot     *disk.BTPage
	bbtRoot     *disk.BTPage
	nbtRootOnce sync.Once
	bbtRootOnce sync.Once

	// Caches
	mu         sync.RWMutex
	nodeCache  map[util.NodeID]*NodeInfo
	blockCache map[util.BlockID]*BlockInfo

	// Write support
	readOnly bool // True if opened in read-only mode
	dirty    bool // True if modifications have been made

	// ID counters for allocation (used during write operations)
	nextBID util.BlockID // Next available block ID
	nextNID uint32       // Next available node index (per type)
}

// NodeInfo contains information about a node from the NBT.
// See [MS-PST] Section 2.2.2.7.7.4 - NBTENTRY structure.
type NodeInfo struct {
	NID       util.NodeID  // nid - Node ID identifying this node
	DataBID   util.BlockID // bidData - Block ID of data block
	SubBID    util.BlockID // bidSub - Block ID of subnode block
	ParentNID util.NodeID  // nidParent - Parent node ID (for folders/messages)
}

// BlockInfo contains information about a block from the BBT.
// See [MS-PST] Section 2.2.2.7.7.3 - BBTENTRY structure.
type BlockInfo struct {
	BID      util.BlockID // bref.bid - Block ID
	Location uint64       // bref.ib - Byte offset in file
	Size     uint16       // cb - Unaligned data size
	RefCount uint16       // cRef - Reference count
}

// Open opens a PST file for reading only.
func Open(filename string) (*Database, error) {
	return OpenWithMode(filename, OpenModeReadOnly)
}

// OpenReadWrite opens a PST file for reading and writing.
func OpenReadWrite(filename string) (*Database, error) {
	return OpenWithMode(filename, OpenModeReadWrite)
}

// OpenWithMode opens a PST file with the specified mode.
func OpenWithMode(filename string, mode OpenMode) (*Database, error) {
	var f *os.File
	var err error

	if mode == OpenModeReadWrite {
		f, err = os.OpenFile(filename, os.O_RDWR, 0)
	} else {
		f, err = os.Open(filename)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}

	header, err := disk.ReadHeader(f)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("failed to read header: %w", err)
	}

	db := &Database{
		file:       f,
		header:     header,
		nodeCache:  make(map[util.NodeID]*NodeInfo),
		blockCache: make(map[util.BlockID]*BlockInfo),
		readOnly:   mode == OpenModeReadOnly,
		nextBID:    util.BlockID(header.BidNextB),
	}

	return db, nil
}

// Close closes the database.
func (db *Database) Close() error {
	if db.file != nil {
		return db.file.Close()
	}
	return nil
}

// Header returns the PST file header.
func (db *Database) Header() *disk.Header {
	return db.header
}

// Format returns the PST format (ANSI or Unicode).
func (db *Database) Format() disk.PSTFormat {
	return db.header.Format
}

// CryptMethod returns the encryption method.
func (db *Database) CryptMethod() disk.CryptMethod {
	return db.header.BCryptMethod
}

// readPage reads a page from the file at the given offset.
func (db *Database) readPage(offset uint64) ([]byte, error) {
	buf := make([]byte, disk.PageSize)
	n, err := db.file.ReadAt(buf, int64(offset)) //nolint:gosec // G115: PST file size bounded by format
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("failed to read page at offset %d: %w", offset, err)
	}
	if n != disk.PageSize {
		return nil, fmt.Errorf("incomplete page read: got %d bytes, expected %d", n, disk.PageSize)
	}
	return buf, nil
}

// loadNBTRoot loads the NBT root page.
func (db *Database) loadNBTRoot() error {
	var loadErr error
	db.nbtRootOnce.Do(func() {
		ref := db.header.NBTRoot()
		data, err := db.readPage(ref.IB)
		if err != nil {
			loadErr = fmt.Errorf("failed to read NBT root page: %w", err)
			return
		}
		db.nbtRoot, err = disk.ParseBTPage(data, db.header.Format, disk.PageTypeNBT)
		if err != nil {
			loadErr = fmt.Errorf("failed to parse NBT root page: %w", err)
			return
		}
	})
	return loadErr
}

// loadBBTRoot loads the BBT root page.
func (db *Database) loadBBTRoot() error {
	var loadErr error
	db.bbtRootOnce.Do(func() {
		ref := db.header.BBTRoot()
		data, err := db.readPage(ref.IB)
		if err != nil {
			loadErr = fmt.Errorf("failed to read BBT root page: %w", err)
			return
		}
		db.bbtRoot, err = disk.ParseBTPage(data, db.header.Format, disk.PageTypeBBT)
		if err != nil {
			loadErr = fmt.Errorf("failed to parse BBT root page: %w", err)
			return
		}
	})
	return loadErr
}

// LookupNode looks up a node by ID in the NBT.
func (db *Database) LookupNode(nid util.NodeID) (*NodeInfo, error) {
	// Check cache first
	db.mu.RLock()
	if info, ok := db.nodeCache[nid]; ok {
		db.mu.RUnlock()
		return info, nil
	}
	db.mu.RUnlock()

	// Load NBT root if needed
	if err := db.loadNBTRoot(); err != nil {
		return nil, err
	}

	// Search the B-tree
	info, err := db.searchNBT(db.nbtRoot, uint64(nid))
	if err != nil {
		return nil, err
	}

	// Cache the result
	db.mu.Lock()
	db.nodeCache[nid] = info
	db.mu.Unlock()

	return info, nil
}

// searchNBT searches the NBT for a node with the given ID.
func (db *Database) searchNBT(page *disk.BTPage, nid uint64) (*NodeInfo, error) {
	if page.IsLeaf() {
		// Binary search in leaf entries
		for _, entry := range page.NBTEntries {
			if entry.NID == nid {
				//nolint:gosec // G115: NID is 32-bit per MS-PST spec
				return &NodeInfo{
					NID:       util.NodeID(entry.NID),
					DataBID:   util.BlockID(entry.DataBID),
					SubBID:    util.BlockID(entry.SubBID),
					ParentNID: util.NodeID(entry.ParentNID),
				}, nil
			}
		}
		return nil, fmt.Errorf("node not found: 0x%X", nid)
	}

	// Non-leaf: find the appropriate child
	// The key in each intermediate entry represents the MINIMUM key in that subtree.
	// We need to find the LAST entry where entry.Key <= nid.
	childIdx := -1
	for i, entry := range page.NonleafEntries {
		if entry.Key <= nid {
			childIdx = i
		} else {
			break // Keys are sorted, no need to continue
		}
	}
	if childIdx < 0 {
		return nil, fmt.Errorf("node not found: 0x%X (key less than all entries)", nid)
	}
	childRef := &page.NonleafEntries[childIdx].Ref

	// Read child page
	childData, err := db.readPage(childRef.IB)
	if err != nil {
		return nil, err
	}
	childPage, err := disk.ParseBTPage(childData, db.header.Format, disk.PageTypeNBT)
	if err != nil {
		return nil, err
	}

	return db.searchNBT(childPage, nid)
}

// LookupBlock looks up a block by ID in the BBT.
func (db *Database) LookupBlock(bid util.BlockID) (*BlockInfo, error) {
	// Clear the internal bit for lookup
	lookupBID := bid &^ util.BlockID(0x2)

	// Check cache first
	db.mu.RLock()
	if info, ok := db.blockCache[lookupBID]; ok {
		db.mu.RUnlock()
		return info, nil
	}
	db.mu.RUnlock()

	// Load BBT root if needed
	if err := db.loadBBTRoot(); err != nil {
		return nil, err
	}

	// Search the B-tree
	info, err := db.searchBBT(db.bbtRoot, uint64(lookupBID))
	if err != nil {
		return nil, err
	}

	// Cache the result
	db.mu.Lock()
	db.blockCache[lookupBID] = info
	db.mu.Unlock()

	return info, nil
}

// searchBBT searches the BBT for a block with the given ID.
func (db *Database) searchBBT(page *disk.BTPage, bid uint64) (*BlockInfo, error) {
	if page.IsLeaf() {
		// Binary search in leaf entries
		for _, entry := range page.BBTEntries {
			// Clear internal bit for comparison
			entryBID := entry.BRef.BID &^ 0x2
			if entryBID == bid {
				return &BlockInfo{
					BID:      util.BlockID(entry.BRef.BID),
					Location: entry.BRef.IB,
					Size:     entry.Size,
					RefCount: entry.RefCount,
				}, nil
			}
		}
		return nil, fmt.Errorf("block not found: 0x%X", bid)
	}

	// Non-leaf: find the appropriate child.
	// The key in each intermediate entry represents the MINIMUM BID in that subtree.
	// LookupBlock strips bidInternal before search, and leaves compare the same
	// way, so intermediate keys must ignore that bit too. Otherwise an SLBLOCK
	// whose first-key is 0xCA (internal) is missed when looking up 0xC8.
	childIdx := -1
	internal := uint64(util.BlockIDInternalBit)
	for i, entry := range page.NonleafEntries {
		if entry.Key&^internal <= bid {
			childIdx = i
		} else {
			break // Keys are sorted, no need to continue
		}
	}
	if childIdx < 0 {
		return nil, fmt.Errorf("block not found: 0x%X (key less than all entries)", bid)
	}
	childRef := &page.NonleafEntries[childIdx].Ref

	// Read child page
	childData, err := db.readPage(childRef.IB)
	if err != nil {
		return nil, err
	}
	childPage, err := disk.ParseBTPage(childData, db.header.Format, disk.PageTypeBBT)
	if err != nil {
		return nil, err
	}

	return db.searchBBT(childPage, bid)
}

// ReadBlockData reads and decrypts the data from a block.
func (db *Database) ReadBlockData(bid util.BlockID) ([]byte, error) {
	info, err := db.LookupBlock(bid)
	if err != nil {
		return nil, err
	}

	return db.ReadBlockDataFromInfo(info)
}

// ReadBlockDataFromInfo reads and decrypts block data given BlockInfo.
func (db *Database) ReadBlockDataFromInfo(info *BlockInfo) ([]byte, error) {
	// Calculate aligned size with trailer
	var trailerSize int
	if db.header.Format == disk.FormatUnicode {
		trailerSize = disk.BlockTrailerSizeUnicode
	} else {
		trailerSize = disk.BlockTrailerSizeANSI
	}
	alignedSize := disk.AlignDisk(uint64(info.Size))
	totalSize := alignedSize + uint64(trailerSize)

	// Read block from disk
	buf := make([]byte, totalSize)
	n, err := db.file.ReadAt(buf, int64(info.Location)) //nolint:gosec // G115: PST file size bounded by format
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("failed to read block at offset %d: %w", info.Location, err)
	}
	if n < int(totalSize) { //nolint:gosec // G115: totalSize bounded by block size limit
		return nil, fmt.Errorf("incomplete block read: got %d bytes, expected %d", n, totalSize)
	}

	// Extract data portion (before trailer)
	data := buf[:info.Size]

	// Decrypt if needed (only external blocks are encrypted)
	if !info.BID.IsInternal() && db.header.BCryptMethod != disk.CryptMethodNone {
		data = disk.DecryptBlock(data, db.header.BCryptMethod, uint64(info.BID))
	}

	return data, nil
}

// GetNode returns a Node for the given node ID.
func (db *Database) GetNode(nid util.NodeID) (*Node, error) {
	info, err := db.LookupNode(nid)
	if err != nil {
		return nil, err
	}

	return newNode(db, info), nil
}

// IsReadOnly returns true if the database was opened in read-only mode.
func (db *Database) IsReadOnly() bool {
	return db.readOnly
}

// IsDirty returns true if the database has uncommitted modifications.
func (db *Database) IsDirty() bool {
	return db.dirty
}

// setDirty marks the database as having uncommitted modifications.
func (db *Database) setDirty() {
	db.dirty = true
}

// clearDirty marks the database as having no uncommitted modifications.
func (db *Database) clearDirty() {
	db.dirty = false
}

// File returns the underlying file handle.
// This is used internally for write operations.
func (db *Database) File() *os.File {
	return db.file
}

// AllocateBlockID allocates and returns a new block ID.
// The returned BID is suitable for use as an external data block ID.
func (db *Database) AllocateBlockID() util.BlockID {
	db.mu.Lock()
	defer db.mu.Unlock()

	bid := db.nextBID
	db.nextBID += util.BlockIDIncrement
	return bid
}

// AllocateInternalBlockID allocates and returns a new internal block ID.
// Internal blocks are used for extended blocks and subnode blocks.
func (db *Database) AllocateInternalBlockID() util.BlockID {
	db.mu.Lock()
	defer db.mu.Unlock()

	bid := db.nextBID | util.BlockIDInternalBit
	db.nextBID += util.BlockIDIncrement
	return bid
}

// NextBlockID returns the next block ID that will be allocated.
func (db *Database) NextBlockID() util.BlockID {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return db.nextBID
}

// InvalidateCache clears the node and block caches.
// This should be called after B-tree modifications.
func (db *Database) InvalidateCache() {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.nodeCache = make(map[util.NodeID]*NodeInfo)
	db.blockCache = make(map[util.BlockID]*BlockInfo)
}

// ReloadBTrees forces a reload of the B-tree root pages.
// This should be called after B-tree structure changes.
func (db *Database) ReloadBTrees() {
	db.nbtRootOnce = sync.Once{}
	db.bbtRootOnce = sync.Once{}
	db.nbtRoot = nil
	db.bbtRoot = nil
}

// WritePage writes a page to the file at the specified offset.
// This is a low-level operation used by write transactions.
func (db *Database) WritePage(offset uint64, data []byte) error {
	if db.readOnly {
		return fmt.Errorf("database is read-only")
	}
	if len(data) != disk.PageSize {
		return fmt.Errorf("invalid page size: got %d, expected %d", len(data), disk.PageSize)
	}

	n, err := db.file.WriteAt(data, int64(offset)) //nolint:gosec // G115: PST file size bounded by format
	if err != nil {
		return fmt.Errorf("failed to write page at offset %d: %w", offset, err)
	}
	if n != disk.PageSize {
		return fmt.Errorf("incomplete page write: wrote %d bytes, expected %d", n, disk.PageSize)
	}

	return nil
}

// WriteBlock writes a block to the file at the specified offset.
// This is a low-level operation used by write transactions.
func (db *Database) WriteBlock(offset uint64, data []byte) error {
	if db.readOnly {
		return fmt.Errorf("database is read-only")
	}

	n, err := db.file.WriteAt(data, int64(offset)) //nolint:gosec // G115: PST file size bounded by format
	if err != nil {
		return fmt.Errorf("failed to write block at offset %d: %w", offset, err)
	}
	if n != len(data) {
		return fmt.Errorf("incomplete block write: wrote %d bytes, expected %d", n, len(data))
	}

	return nil
}

// Sync flushes any buffered data to the underlying storage device.
func (db *Database) Sync() error {
	if db.file == nil {
		return nil
	}
	return db.file.Sync()
}
