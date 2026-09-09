package disk

import (
	"encoding/binary"
	"fmt"
	"io"
)

// BlockReference represents a reference to a block (ID and location).
// See [MS-PST] Section 2.2.2.4 - BREF structure.
type BlockReference struct {
	BID uint64 // Block ID. See [MS-PST] Section 2.2.2.2.
	IB  uint64 // Byte offset in file (ib field)
}

// Root contains the root B-tree information from the header.
// See [MS-PST] Section 2.2.2.5 - ROOT structure.
type Root struct {
	COrphans   uint32         // dwReserved - Number of orphans in BBT (reserved)
	IBFileEOF  uint64         // ibFileEof - End of file offset
	IBAMapLast uint64         // ibAMapLast - Last AMap page location
	CBAMapFree uint64         // cbAMapFree - Free space in AMap pages
	CBPMapFree uint64         // cbPMapFree - Free space in PMap pages (ANSI only)
	BRefNBT    BlockReference // BREFNBT - NBT root reference. See [MS-PST] Section 2.2.2.7.7.
	BRefBBT    BlockReference // BREFBBT - BBT root reference. See [MS-PST] Section 2.2.2.7.7.
	FAMapValid byte           // fAMapValid - AMap validity flag
	BARVec     byte           // bARVec - AddRef vector selector (reserved)
	CARVec     uint16         // cARVec - AddRef vector count (reserved)
}

// Header represents the PST file header.
// See [MS-PST] Section 2.2.2.6 - HEADER structure.
// The header structure differs between ANSI (512 bytes) and Unicode (564 bytes) formats.
type Header struct {
	// Common fields - See [MS-PST] Section 2.2.2.6
	DWMagic         uint32 // dwMagic - Magic number (should be PSTMagic "!BDN")
	DWCRCPartial    uint32 // dwCRCPartial - Partial header CRC
	WMagicClient    uint16 // wMagicClient - Client magic (PST "SM" or OST "SO")
	WVer            uint16 // wVer - Database format version (14-15=ANSI, 20-23=Unicode)
	WVerClient      uint16 // wVerClient - Client version
	BPlatformCreate byte   // bPlatformCreate - Platform created on (1=Win32)
	BPlatformAccess byte   // bPlatformAccess - Platform accessed from
	DWOpenDBID      uint32 // dwOpenDBID - Open database ID (reserved)
	DWOpenClaimID   uint32 // dwOpenClaimID - Open claim ID (reserved)

	// Format-specific (derived from wVer)
	Format PSTFormat // ANSI or Unicode

	// Root information - See [MS-PST] Section 2.2.2.5
	Root Root // ROOT structure containing B-tree references

	// Encryption - See [MS-PST] Section 2.2.2.6 and Section 5.1
	BCryptMethod CryptMethod // bCryptMethod - Encryption method

	// Unicode-specific fields
	BidNextB  uint64 // bidNextB - Next block ID (Unicode format)
	DWCRCFull uint32 // dwCRCFull - Full header CRC (Unicode only)

	// ANSI-specific fields
	BidNextP uint64 // bidNextP - Next page ID
	DWUnique uint32 // dwUnique - Unique value
}

// ReadHeader reads and parses the PST file header.
func ReadHeader(r io.ReaderAt) (*Header, error) {
	// Read enough for the larger Unicode header
	buf := make([]byte, HeaderSizeUnicode)
	n, err := r.ReadAt(buf, 0)
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("failed to read header: %w", err)
	}
	if n < HeaderSizeANSI {
		return nil, fmt.Errorf("file too small for PST header: got %d bytes, need at least %d", n, HeaderSizeANSI)
	}

	h := &Header{}

	// Parse common header fields
	h.DWMagic = binary.LittleEndian.Uint32(buf[0:4])
	h.DWCRCPartial = binary.LittleEndian.Uint32(buf[4:8])
	h.WMagicClient = binary.LittleEndian.Uint16(buf[8:10])
	h.WVer = binary.LittleEndian.Uint16(buf[10:12])
	h.WVerClient = binary.LittleEndian.Uint16(buf[12:14])
	h.BPlatformCreate = buf[14]
	h.BPlatformAccess = buf[15]
	h.DWOpenDBID = binary.LittleEndian.Uint32(buf[16:20])
	h.DWOpenClaimID = binary.LittleEndian.Uint32(buf[20:24])

	// Validate magic number
	if h.DWMagic != PSTMagic {
		return nil, fmt.Errorf("invalid PST magic: got 0x%08X, expected 0x%08X", h.DWMagic, PSTMagic)
	}

	// Validate client magic
	if h.WMagicClient != ClientMagicPST && h.WMagicClient != ClientMagicOST {
		return nil, fmt.Errorf("invalid client magic: got 0x%04X", h.WMagicClient)
	}

	// Determine format based on version
	if h.WVer >= DatabaseFormatUnicodeMin && h.WVer <= DatabaseFormatUnicodeMax {
		h.Format = FormatUnicode
		if n < HeaderSizeUnicode {
			return nil, fmt.Errorf("file too small for Unicode header: got %d bytes, need %d", n, HeaderSizeUnicode)
		}
		return parseUnicodeHeader(h, buf)
	} else if h.WVer >= DatabaseFormatANSIMin && h.WVer <= DatabaseFormatANSIMax {
		h.Format = FormatANSI
		return parseANSIHeader(h, buf)
	}

	return nil, fmt.Errorf("unsupported PST version: %d", h.WVer)
}

// parseUnicodeHeader parses the Unicode format header (64-bit addresses).
func parseUnicodeHeader(h *Header, buf []byte) (*Header, error) {
	// Offsets for Unicode header
	// After common fields (24 bytes):
	// bidUnused: 24-32 (8 bytes)
	// bidNextP: 32-40 (8 bytes) - next page ID
	// dwUnique: 40-44 (4 bytes)
	// rgnid[32]: 44-172 (128 bytes) - node ID counters
	// qwUnused: 172-180 (8 bytes) - unused
	// root: 180-252 (72 bytes) - root info
	// dwAlign: 252-256 (4 bytes) - alignment
	// rgbFM: 256-384 (128 bytes) - FMap (deprecated)
	// rgbFP: 384-512 (128 bytes) - FPMap (deprecated)
	// bSentinel: 512 (1 byte)
	// bCryptMethod: 513 (1 byte)
	// rgbReserved: 514-516 (2 bytes)
	// bidNextB: 516-524 (8 bytes)
	// dwCRCFull: 524-528 (4 bytes)
	// ... remaining fields

	h.BidNextP = binary.LittleEndian.Uint64(buf[32:40])
	h.DWUnique = binary.LittleEndian.Uint32(buf[40:44])

	// Parse root info (starting at offset 180, after qwUnused)
	rootOffset := 180
	h.Root = parseRootUnicode(buf[rootOffset:])

	// bSentinel at offset 512, bCryptMethod at offset 513
	h.BCryptMethod = CryptMethod(buf[513])

	// bidNextB at offset 516
	h.BidNextB = binary.LittleEndian.Uint64(buf[516:524])

	// Full CRC at offset 524
	h.DWCRCFull = binary.LittleEndian.Uint32(buf[524:528])

	return h, nil
}

// parseANSIHeader parses the ANSI format header (32-bit addresses).
func parseANSIHeader(h *Header, buf []byte) (*Header, error) {
	// Offsets for ANSI header (different from Unicode!)
	// After common fields (24 bytes):
	// bidNextB: 24-28 (4 bytes) - in ANSI, this comes earlier
	// bidNextP: 28-32 (4 bytes)
	// dwUnique: 32-36 (4 bytes)
	// rgnid[32]: 36-164 (128 bytes)
	// root: 164-204 (40 bytes) - smaller in ANSI
	// rgbFM: 204-332 (128 bytes)
	// rgbFP: 332-460 (128 bytes)
	// bSentinel: 460 (1 byte)
	// bCryptMethod: 461 (1 byte)
	// rgbReserved: 462-464 (2 bytes)
	// ullReserved: 464-472 (8 bytes)
	// dwReserved: 472-476 (4 bytes)
	// dwReserved2: 476-480 (4 bytes)
	// ... more reserved

	h.BidNextB = uint64(binary.LittleEndian.Uint32(buf[24:28]))
	h.BidNextP = uint64(binary.LittleEndian.Uint32(buf[28:32]))
	h.DWUnique = binary.LittleEndian.Uint32(buf[32:36])

	// Parse root info (starting at offset 164)
	rootOffset := 164
	h.Root = parseRootANSI(buf[rootOffset:])

	// Encryption method at offset 461
	h.BCryptMethod = CryptMethod(buf[461])

	return h, nil
}

// parseRootUnicode parses the root structure for Unicode format.
func parseRootUnicode(buf []byte) Root {
	// Root structure for Unicode (72 bytes):
	// See [MS-PST] Section 2.2.2.5 - ROOT structure.
	// dwReserved: 0-4 (4 bytes)
	// ibFileEOF: 4-12 (8 bytes)
	// ibAMapLast: 12-20 (8 bytes)
	// cbAMapFree: 20-28 (8 bytes)
	// cbPMapFree: 28-36 (8 bytes)
	// brefNBT: 36-52 (16 bytes) - bid(8) + ib(8)
	// brefBBT: 52-68 (16 bytes)
	// fAMapValid: 68 (1 byte)
	// bARVec: 69 (1 byte)
	// cARVec: 70-72 (2 bytes)

	return Root{
		COrphans:   binary.LittleEndian.Uint32(buf[0:4]),
		IBFileEOF:  binary.LittleEndian.Uint64(buf[4:12]),
		IBAMapLast: binary.LittleEndian.Uint64(buf[12:20]),
		CBAMapFree: binary.LittleEndian.Uint64(buf[20:28]),
		CBPMapFree: binary.LittleEndian.Uint64(buf[28:36]),
		BRefNBT: BlockReference{
			BID: binary.LittleEndian.Uint64(buf[36:44]),
			IB:  binary.LittleEndian.Uint64(buf[44:52]),
		},
		BRefBBT: BlockReference{
			BID: binary.LittleEndian.Uint64(buf[52:60]),
			IB:  binary.LittleEndian.Uint64(buf[60:68]),
		},
		FAMapValid: buf[68],
		BARVec:     buf[69],
		CARVec:     binary.LittleEndian.Uint16(buf[70:72]),
	}
}

// parseRootANSI parses the root structure for ANSI format.
func parseRootANSI(buf []byte) Root {
	// Root structure for ANSI (40 bytes):
	// cOrphans: 0-4 (4 bytes)
	// ibFileEOF: 4-8 (4 bytes)
	// ibAMapLast: 8-12 (4 bytes)
	// cbAMapFree: 12-16 (4 bytes)
	// cbPMapFree: 16-20 (4 bytes)
	// brefNBT: 20-28 (8 bytes) - bid(4) + ib(4)
	// brefBBT: 28-36 (8 bytes)
	// fAMapValid: 36 (1 byte)
	// bARVec: 37 (1 byte)
	// cARVec: 38-40 (2 bytes)

	return Root{
		COrphans:   binary.LittleEndian.Uint32(buf[0:4]),
		IBFileEOF:  uint64(binary.LittleEndian.Uint32(buf[4:8])),
		IBAMapLast: uint64(binary.LittleEndian.Uint32(buf[8:12])),
		CBAMapFree: uint64(binary.LittleEndian.Uint32(buf[12:16])),
		CBPMapFree: uint64(binary.LittleEndian.Uint32(buf[16:20])),
		BRefNBT: BlockReference{
			BID: uint64(binary.LittleEndian.Uint32(buf[20:24])),
			IB:  uint64(binary.LittleEndian.Uint32(buf[24:28])),
		},
		BRefBBT: BlockReference{
			BID: uint64(binary.LittleEndian.Uint32(buf[28:32])),
			IB:  uint64(binary.LittleEndian.Uint32(buf[32:36])),
		},
		FAMapValid: buf[36],
		BARVec:     buf[37],
		CARVec:     binary.LittleEndian.Uint16(buf[38:40]),
	}
}

// IsPST returns true if the file is a PST (not OST).
func (h *Header) IsPST() bool {
	return h.WMagicClient == ClientMagicPST
}

// IsOST returns true if the file is an OST.
func (h *Header) IsOST() bool {
	return h.WMagicClient == ClientMagicOST
}

// IsUnicode returns true if the file uses Unicode format.
func (h *Header) IsUnicode() bool {
	return h.Format == FormatUnicode
}

// IsANSI returns true if the file uses ANSI format.
func (h *Header) IsANSI() bool {
	return h.Format == FormatANSI
}

// CryptMethod returns the encryption method.
func (h *Header) CryptMethod() CryptMethod {
	return h.BCryptMethod
}

// NBTRoot returns the NBT root reference.
func (h *Header) NBTRoot() BlockReference {
	return h.Root.BRefNBT
}

// BBTRoot returns the BBT root reference.
func (h *Header) BBTRoot() BlockReference {
	return h.Root.BRefBBT
}
