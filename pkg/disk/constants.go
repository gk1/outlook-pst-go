// Package disk provides low-level binary format structures for PST files.
// This package implements the disk layer of the MS-PST specification.
// See [MS-PST] Section 2.2 for the physical layer format.
//
// [MS-PST]: https://docs.microsoft.com/en-us/openspecs/office_file_formats/ms-pst/
package disk

// Magic numbers for PST/OST file identification.
// See [MS-PST] Section 2.2.2.6 - dwMagic and wMagicClient fields.
const (
	PSTMagic       = 0x4E444221 // "!BDN" - PST file magic (stored as little-endian)
	ClientMagicPST = 0x4D53     // "SM" - PST client magic
	ClientMagicOST = 0x4F53     // "SO" - OST client magic
)

// Database format versions.
// See [MS-PST] Section 2.2.2.6 - wVer field.
const (
	DatabaseFormatANSIMin    = 14 // Minimum ANSI format version
	DatabaseFormatANSIMax    = 15 // Maximum ANSI format version
	DatabaseFormatUnicodeMin = 20 // Minimum Unicode format version (23 is most common)
	DatabaseFormatUnicodeMax = 23 // Maximum Unicode format version
)

// Database types.
const (
	DatabaseTypePST = 19 // Personal storage table
	DatabaseTypeOST = 12 // Offline storage table
)

// PSTFormat represents the PST file format (ANSI or Unicode).
// See [MS-PST] Section 1.3.1 for the distinction between ANSI and Unicode formats.
type PSTFormat int

const (
	FormatUnknown PSTFormat = iota
	FormatANSI              // 32-bit addresses, 512-byte header. See [MS-PST] Section 2.2.2.6.
	FormatUnicode           // 64-bit addresses, 564-byte header. See [MS-PST] Section 2.2.2.6.
)

func (f PSTFormat) String() string {
	switch f {
	case FormatANSI:
		return "ANSI"
	case FormatUnicode:
		return "Unicode"
	default:
		return "Unknown"
	}
}

// CryptMethod represents the encryption method used in the PST file.
// See [MS-PST] Section 2.2.2.6 - bCryptMethod field.
// Encryption is applied to data blocks but not to page structures.
// See [MS-PST] Section 5.1 for encoding/decoding algorithms.
type CryptMethod byte

const (
	CryptMethodNone    CryptMethod = 0 // NDB_CRYPT_NONE - No encryption
	CryptMethodPermute CryptMethod = 1 // NDB_CRYPT_PERMUTE - Permutative encoding. See [MS-PST] Section 5.1.
	CryptMethodCyclic  CryptMethod = 2 // NDB_CRYPT_CYCLIC - Cyclic encoding. See [MS-PST] Section 5.1.
)

func (c CryptMethod) String() string {
	switch c {
	case CryptMethodNone:
		return "None"
	case CryptMethodPermute:
		return "Permute"
	case CryptMethodCyclic:
		return "Cyclic"
	default:
		return "Unknown"
	}
}

// PageType represents the type of a page in the PST file.
// See [MS-PST] Section 2.2.2.7 - ptype field in PAGETRAILER.
type PageType byte

const (
	PageTypeBBT   PageType = 0x80 // ptypeBBT - Block B-tree page. See [MS-PST] Section 2.2.2.7.7.1.
	PageTypeNBT   PageType = 0x81 // ptypeNBT - Node B-tree page. See [MS-PST] Section 2.2.2.7.7.1.
	PageTypeFMap  PageType = 0x82 // ptypeFMap - Free Map (deprecated)
	PageTypePMap  PageType = 0x83 // ptypePMap - Page Map (deprecated)
	PageTypeAMap  PageType = 0x84 // ptypeAMap - Allocation Map. See [MS-PST] Section 2.2.2.7.2.
	PageTypeFPMap PageType = 0x85 // ptypeFPMap - Free Page Map (deprecated)
	PageTypeDList PageType = 0x86 // ptypeDL - Density List. See [MS-PST] Section 2.2.2.7.4.
)

func (p PageType) String() string {
	switch p {
	case PageTypeBBT:
		return "BBT"
	case PageTypeNBT:
		return "NBT"
	case PageTypeFMap:
		return "FMap"
	case PageTypePMap:
		return "PMap"
	case PageTypeAMap:
		return "AMap"
	case PageTypeFPMap:
		return "FPMap"
	case PageTypeDList:
		return "DList"
	default:
		return "Unknown"
	}
}

// BlockType represents the type of internal block structure.
// See [MS-PST] Section 2.2.2.8.3 - XBLOCK and XXBLOCK structures.
type BlockType byte

const (
	BlockTypeExternal BlockType = 0x00 // External (data) block. See [MS-PST] Section 2.2.2.8.1.
	BlockTypeExtended BlockType = 0x01 // XBLOCK - Extended block (tree of data blocks). See [MS-PST] Section 2.2.2.8.3.1.
	BlockTypeSubnode  BlockType = 0x02 // SLBLOCK/SIBLOCK - Subnode block. See [MS-PST] Section 2.2.2.8.3.3.
)

func (b BlockType) String() string {
	switch b {
	case BlockTypeExternal:
		return "External"
	case BlockTypeExtended:
		return "Extended"
	case BlockTypeSubnode:
		return "Subnode"
	default:
		return "Unknown"
	}
}

// Size constants.
const (
	PageSize            = 512  // All pages are 512 bytes
	MaxBlockDiskSize    = 8192 // Maximum block size on disk (8KB)
	BytesPerSlot        = 64   // Bytes per AMap slot
	HeapMaxAllocSize    = 3580 // Maximum heap allocation size
	HeapMaxAllocSizeV14 = 3068 // Maximum heap allocation size for ANSI v14
)

// File layout constants.
const (
	FirstAMapPageLocation = 0x4400 // Location of first AMap page
	DListPageLocation     = 0x4200 // Location of DList page
)

// Header size constants.
const (
	HeaderSizeANSI    = 512 // ANSI header size in bytes
	HeaderSizeUnicode = 564 // Unicode header size in bytes (MS-PST 2.2.2.6)
)

// Trailer size constants.
const (
	PageTrailerSizeANSI     = 12 // ANSI page trailer size
	PageTrailerSizeUnicode  = 16 // Unicode page trailer size
	BlockTrailerSizeANSI    = 12 // ANSI block trailer size
	BlockTrailerSizeUnicode = 16 // Unicode block trailer size
)

// Entry size constants.
const (
	NBTLeafEntrySizeANSI    = 16 // ANSI NBT leaf entry size
	NBTLeafEntrySizeUnicode = 32 // Unicode NBT leaf entry size
	BBTLeafEntrySizeANSI    = 12 // ANSI BBT leaf entry size
	BBTLeafEntrySizeUnicode = 24 // Unicode BBT leaf entry size
)

// Heap constants.
// See [MS-PST] Section 2.3.1 - Heap-on-Node (HN).
const (
	HeapSignature = 0xEC // bSig - Heap-on-Node signature byte. See [MS-PST] Section 2.3.1.2.
	HeapSigGMP    = 0x6C // bClientSig - Internal GMP client
	HeapSigTC     = 0x7C // bClientSig - Table Context client. See [MS-PST] Section 2.3.4.
	HeapSigSMP    = 0x8C // bClientSig - Internal SMP client
	HeapSigHMP    = 0x9C // bClientSig - Internal HMP client
	HeapSigBTH    = 0xB5 // bClientSig - BTree-on-Heap client. See [MS-PST] Section 2.3.2.
	HeapSigPC     = 0xBC // bClientSig - Property Context client. See [MS-PST] Section 2.3.3.
)

// AMapStatus represents the validity status of the allocation map.
// See [MS-PST] Section 2.2.2.5 - fAMapValid field and Section 5.4 for write algorithm.
type AMapStatus byte

const (
	// AMapStatusValid indicates the AMap is in a consistent state.
	// This is the normal state after successful PST operations.
	AMapStatusValid AMapStatus = 0x00

	// AMapStatusInvalid indicates a write is in progress or incomplete.
	// This is set at the start of a write operation (phase 1 of two-phase commit).
	AMapStatusInvalid AMapStatus = 0x01

	// AMapStatusValid2 indicates write completed successfully.
	// This is set after write completion (phase 2 of two-phase commit).
	// On next open, this gets normalized to AMapStatusValid.
	AMapStatusValid2 AMapStatus = 0x02
)

func (s AMapStatus) String() string {
	switch s {
	case AMapStatusValid:
		return "Valid"
	case AMapStatusInvalid:
		return "Invalid"
	case AMapStatusValid2:
		return "Valid2"
	default:
		return "Unknown"
	}
}

// Maximum data sizes.
const (
	// MaxDataBlockSize is the maximum payload size for a data block (before alignment/trailer).
	// Unicode: 8192 - 16 (trailer) - 64 (alignment padding worst case) = 8176
	// But actual max is 8176 bytes of data per [MS-PST] 2.2.2.8.3.1.
	MaxDataBlockSizeUnicode = 8176
	MaxDataBlockSizeANSI    = 8180

	// MaxXBlockEntries is the maximum number of BID entries in an XBLOCK.
	MaxXBlockEntriesUnicode = 1020 // (8192 - 8 header - 16 trailer) / 8
	MaxXBlockEntriesANSI    = 2040 // (8192 - 8 header - 12 trailer) / 4

	// MaxSubnodeLeafEntries is the maximum number of entries in a subnode leaf block.
	MaxSubnodeLeafEntriesUnicode = 340 // (8192 - 8 header - 16 trailer) / 24
	MaxSubnodeLeafEntriesANSI    = 680 // (8192 - 4 header - 12 trailer) / 12
)

// B-tree page constants.
const (
	// MaxBTPageEntriesNBTUnicode is max NBT leaf entries per page (Unicode).
	// (512 - 16 trailer - 4 header) / 32 = 15
	MaxBTPageEntriesNBTUnicode = 15

	// MaxBTPageEntriesNBTANSI is max NBT leaf entries per page (ANSI).
	// (512 - 12 trailer - 4 header) / 16 = 31
	MaxBTPageEntriesNBTANSI = 31

	// MaxBTPageEntriesBBTUnicode is max BBT leaf entries per page (Unicode).
	// (512 - 16 trailer - 4 header) / 24 = 20
	MaxBTPageEntriesBBTUnicode = 20

	// MaxBTPageEntriesBBTANSI is max BBT leaf entries per page (ANSI).
	// (512 - 12 trailer - 4 header) / 12 = 41
	MaxBTPageEntriesBBTANSI = 41

	// MaxBTPageEntriesNonleafUnicode is max non-leaf entries per page (Unicode).
	// (512 - 16 trailer - 4 header) / 24 = 20
	MaxBTPageEntriesNonleafUnicode = 20

	// MaxBTPageEntriesNonleafANSI is max non-leaf entries per page (ANSI).
	// (512 - 12 trailer - 4 header) / 12 = 41
	MaxBTPageEntriesNonleafANSI = 41
)

// AlignDisk returns the disk-aligned size (to 64-byte boundary).
func AlignDisk(size uint64) uint64 {
	return (size + 63) &^ 63
}

// AlignDisk32 returns the disk-aligned size for 32-bit values.
func AlignDisk32(size uint32) uint32 {
	return (size + 63) &^ 63
}
