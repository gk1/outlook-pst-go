package writer

// MS-PST section citations used by the inspector and error taxonomy.
const (
	SectionANSICreate   = "1.3.2"
	SectionNID          = "2.2.2.1"
	SectionBID          = "2.2.2.2"
	SectionRoot         = "2.2.2.5"
	SectionHeader       = "2.2.2.6"
	SectionPageTrailer  = "2.2.2.7.1"
	SectionAMap         = "2.2.2.7.2"
	SectionPMap         = "2.2.2.7.3"
	SectionDList        = "2.2.2.7.4"
	SectionFMap         = "2.2.2.7.5"
	SectionFPMap        = "2.2.2.7.6"
	SectionBTPAGE       = "2.2.2.7.7.1"
	SectionBTENTRY      = "2.2.2.7.7.2"
	SectionBBTENTRY     = "2.2.2.7.7.3"
	SectionRefCount     = "2.2.2.7.7.3.1"
	SectionNBTENTRY     = "2.2.2.7.7.4"
	SectionGrow         = "2.6.1.1.2"
	SectionBlockTrailer = "2.2.2.8.1"
	SectionBlockAlign   = "2.2.2.8"
	SectionXBlock       = "2.2.2.8.3.1"
	SectionXXBlock      = "2.2.2.8.3.2"
	SectionSubnode      = "2.2.2.8.3.3"
	SectionSLBlock      = "2.2.2.8.3.3.1"
	SectionSIBlock      = "2.2.2.8.3.3.2"
	SectionHN           = "2.3.1"
	SectionTCINFO       = "2.3.4.1"
	SectionTCOLDESC     = "2.3.4.2"
	SectionRowMatrix    = "2.3.4.4"
	SectionTCRowID      = "2.3.4.4.1"
	SectionMinPST       = "2.7.1"
	SectionCRC          = "5.3"
	SectionSignature    = "5.5"
	SectionCrypt        = "5.1"
)

// Physical sizes for Unicode PST. ANSI is rejected by this writer.
const (
	UnicodeHeaderSize            = 564
	UnicodeRootSize              = 72
	PageSize                     = 512
	UnicodePageTrailer           = 16
	UnicodeBlockTrailer          = 16
	BytesPerSlot                 = 64
	AMapBitmapBytes              = 496
	SlotsPerAMap                 = AMapBitmapBytes * 8         // 3968
	AMapCoverageBytes            = SlotsPerAMap * BytesPerSlot // 253952
	PMapCoverageBytes            = SlotsPerAMap * PageSize     // 2031616; one PMap per 8 AMaps
	AMapsPerPMap                 = PMapCoverageBytes / AMapCoverageBytes
	FMapHeaderAMaps              = 128 // HEADER.rgbFM; extra FMaps start at AMap 128
	FMapPageAMaps                = AMapBitmapBytes
	FPMapHeaderPMaps             = 128 * 8 // HEADER.rgbFP bits
	FPMapPagePMaps               = AMapBitmapBytes * 8
	FirstAMapPageOffset          = 0x4400
	FirstPMapPageOffset          = 0x4600
	DListPageOffset              = 0x4200
	DListMaxEntries              = 119 // Unicode: 476 bytes of 4-byte entries
	MaxDataBlockCB               = 8176
	MaxAllocBytes                = 8192 // MS-PST max on-disk page/block size
	XBlockHeaderSize             = 8
	SLBlockHeaderSize            = 8 // Unicode: 4-byte header + dwPadding
	SIBlockHeaderSize            = 8
	SLEntrySize                  = 24
	SIEntrySize                  = 16
	MaxXBlockEntries             = (MaxDataBlockCB - XBlockHeaderSize) / 8 // 1021
	MaxSLBlockEntries            = (MaxDataBlockCB - SLBlockHeaderSize) / SLEntrySize
	MaxSIBlockEntries            = (MaxDataBlockCB - SIBlockHeaderSize) / SIEntrySize
	BlockTypeXBlock       byte   = 0x01
	BlockTypeSubnode      byte   = 0x02
	XBlockLevel           byte   = 1
	XXBlockLevel          byte   = 2
	PageBIDIncrement      uint64 = 1 // page BIDs use all bits (MS-PST 2.2.2.2)
	BlockBIDIncrement     uint64 = 4 // block BIDs keep reserved/internal bits clear
	BIDReserved           uint64 = 1 // bid.r MUST be 0
	BIDInternal           uint64 = 2 // bidInternal; internal (XBLOCK/SLBLOCK) blocks
	UnicodeWVer                  = 23
	UnicodeWVerMin               = 23
	UnicodeWVerMax               = 23 // existing reader (disk.ReadHeader) accepts 20-23
	UnicodeWVerWIP               = 37
	ClientVerPST                 = 19
	FirstAllocBID         uint64 = 4 // BID 0 is null; first assignable page/block BID
	NIDIndexDefault       uint32 = 0x400
	NIDIndexSearchFolder  uint32 = 0x4000
	NIDIndexNormalMessage uint32 = 0x10000
	NIDIndexAssocMessage  uint32 = 0x8000
	Sentinel                     = 0x80
	PlatformWin32                = 0x01
	HeaderPartialCRCLen          = 471 // dwCRCPartial covers 471 bytes from wMagicClient
	HeaderFullCRCLen             = 516 // dwCRCFull covers 516 bytes from wMagicClient
	TCINFOFixedSize              = 22
	TCOLDESCSize                 = 8
	HeapSigTC                    = 0x7C
	PtypInteger32                = 0x0003
	MaxTCColumns                 = 255 // TCINFO.cCols is a BYTE (MS-PST 2.3.4.1)

	UnicodeBTEntriesBytes = 488 // rgentries before cEnt (MS-PST 2.2.2.7.7.1)
	UnicodeBTHeaderOff    = 488
	UnicodeBTPaddingOff   = 492
	NBTLeafEntrySize      = 32
	BBTLeafEntrySize      = 24
	BTNonleafEntrySize    = 24
	MaxNBTLeafEntries     = UnicodeBTEntriesBytes / NBTLeafEntrySize   // 15
	MaxBBTLeafEntries     = UnicodeBTEntriesBytes / BBTLeafEntrySize   // 20
	MaxBTNonleafEntries   = UnicodeBTEntriesBytes / BTNonleafEntrySize // 20

	// PidTagLtpRowId / PidTagLtpRowVer complete tags. See MS-PST 2.3.4.4.1.
	PidTagLtpRowId  uint16 = 0x67F2
	PidTagLtpRowVer uint16 = 0x67F3
	TagLtpRowId            = uint32(PtypInteger32) | uint32(PidTagLtpRowId)<<16  // 0x67F20003
	TagLtpRowVer           = uint32(PtypInteger32) | uint32(PidTagLtpRowVer)<<16 // 0x67F30003
)

// Unicode HEADER field offsets. See MS-PST 2.2.2.6.
const (
	OffMagic          = 0
	OffCRCPartial     = 4
	OffMagicClient    = 8
	OffWVer           = 10
	OffWVerClient     = 12
	OffPlatformCreate = 14
	OffPlatformAccess = 15
	OffReserved1      = 16
	OffReserved2      = 20
	OffBidUnused      = 24
	OffBidNextP       = 32
	OffUnique         = 40
	OffRgNID          = 44
	OffQWUnused       = 172
	OffRoot           = 180
	OffAlign          = 252
	OffRgbFM          = 256
	OffRgbFP          = 384
	OffSentinel       = 512
	OffCrypt          = 513
	OffRgbReserved    = 514
	OffBidNextB       = 516
	OffCRCFull        = 524
	OffRgbReserved2   = 528
	OffBReserved      = 531
	OffRgbReserved3   = 532
)

// ROOT field offsets inside the 72-byte Unicode ROOT. See MS-PST 2.2.2.5.
const (
	OffRootReserved  = 0
	OffRootFileEOF   = 4
	OffRootAMapLast  = 12
	OffRootAMapFree  = 20
	OffRootPMapFree  = 28
	OffRootNBTBID    = 36
	OffRootNBTIB     = 44
	OffRootBBTBID    = 52
	OffRootBBTIB     = 60
	OffRootAMapValid = 68
	OffRootARVec     = 69
	OffRootCARVec    = 70
)

// fAMapValid values. The legacy library reverses 0 and 1.
// See MS-PST 2.2.2.5 and 2.6.1.3.7.
const (
	AMapInvalid byte = 0x00 // INVALID_AMAP
	AMapValid1  byte = 0x01 // VALID_AMAP1 (deprecated)
	AMapValid2  byte = 0x02 // VALID_AMAP2
)

// Magic numbers. See MS-PST 2.2.2.6.
const (
	PSTMagic       = 0x4E444221 // "!BDN"
	ClientMagicPST = 0x4D53     // "SM"
	ClientMagicOST = 0x4F53     // "SO"
)

// Crypt methods. See MS-PST 2.2.2.6 / 5.1. WIP 0x10 is rejected.
const (
	CryptNone    byte = 0
	CryptPermute byte = 1
	CryptCyclic  byte = 2
	CryptWIP     byte = 0x10
)

// Page types. See MS-PST 2.2.2.7.
const (
	PageBBT   byte = 0x80
	PageNBT   byte = 0x81
	PageFMap  byte = 0x82
	PagePMap  byte = 0x83
	PageAMap  byte = 0x84
	PageFPMap byte = 0x85
	PageDList byte = 0x86
)

// DList flags. See MS-PST 2.2.2.7.4.2.
const DFLBackfillComplete byte = 0x01

// Special NIDs. See MS-PST 2.4.1 / 2.7.1.
const (
	NIDMessageStore uint32 = 0x21
	NIDNameToIDMap  uint32 = 0x61
	NIDRootFolder   uint32 = 0x122
)

// NID types. See MS-PST 2.2.2.1.
const (
	NIDTypeHID                = 0x00
	NIDTypeInternal           = 0x01
	NIDTypeNormalFolder       = 0x02
	NIDTypeSearchFolder       = 0x03
	NIDTypeNormalMessage      = 0x04
	NIDTypeAttachment         = 0x05
	NIDTypeAssocMessage       = 0x08
	NIDTypeHierarchyTable     = 0x0D
	NIDTypeContentsTable      = 0x0E
	NIDTypeAssocContentsTable = 0x0F
	NIDTypeAttachmentTable    = 0x11
	NIDTypeRecipientTable     = 0x12
)

// MakeNID packs a type and index. See MS-PST 2.2.2.1.
func MakeNID(nidType byte, index uint32) uint32 {
	return uint32(nidType&0x1F) | (index << 5)
}

// NIDTypeOf returns the 5-bit nidType.
func NIDTypeOf(nid uint32) byte {
	return byte(nid & 0x1F)
}

// NIDIndexOf returns nidIndex.
func NIDIndexOf(nid uint32) uint32 {
	return nid >> 5
}

// Align64 rounds size up to a multiple of 64. See MS-PST 2.2.2.8.
func Align64(size uint64) uint64 {
	return (size + 63) &^ 63
}

// BlockDiskSize is the on-disk size of a Unicode block with payload cb.
// Total (cb + BLOCKTRAILER) is rounded up to a 64-byte multiple.
// See MS-PST 2.2.2.8.1.
func BlockDiskSize(cb uint64) uint64 {
	return Align64(cb + UnicodeBlockTrailer)
}
