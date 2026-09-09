package disk

import (
	"encoding/binary"
	"fmt"
	"io"
)

// SerializeHeader serializes the header to bytes for writing.
// It automatically selects Unicode or ANSI format based on h.Format.
func SerializeHeader(h *Header) ([]byte, error) {
	if h.Format == FormatUnicode {
		return SerializeHeaderUnicode(h)
	}
	return SerializeHeaderANSI(h)
}

// SerializeHeaderUnicode serializes a Unicode format header.
// The buffer is HeaderSizeUnicode (564) bytes; field offsets in this
// function remain the pre-v2 layout and are rejected by writer/v2.
func SerializeHeaderUnicode(h *Header) ([]byte, error) {
	buf := make([]byte, HeaderSizeUnicode)

	// Common header fields (0-24)
	binary.LittleEndian.PutUint32(buf[0:4], h.DWMagic)
	binary.LittleEndian.PutUint32(buf[4:8], h.DWCRCPartial)
	binary.LittleEndian.PutUint16(buf[8:10], h.WMagicClient)
	binary.LittleEndian.PutUint16(buf[10:12], h.WVer)
	binary.LittleEndian.PutUint16(buf[12:14], h.WVerClient)
	buf[14] = h.BPlatformCreate
	buf[15] = h.BPlatformAccess
	binary.LittleEndian.PutUint32(buf[16:20], h.DWOpenDBID)
	binary.LittleEndian.PutUint32(buf[20:24], h.DWOpenClaimID)

	// bidUnused: 24-32 (8 bytes) - reserved, write zeros
	// bidNextP: 32-40 (8 bytes)
	binary.LittleEndian.PutUint64(buf[32:40], h.BidNextP)
	// dwUnique: 40-44 (4 bytes)
	binary.LittleEndian.PutUint32(buf[40:44], h.DWUnique)

	// rgnid[32]: 44-172 (128 bytes) - node ID counters
	// For now, write zeros (these are maintained internally by PST)

	// Root structure: 172-252 (80 bytes)
	serializeRootUnicode(buf[172:], &h.Root)

	// rgbFM: 252-380 (128 bytes) - deprecated FMap, write zeros
	// rgbFP: 380-508 (128 bytes) - deprecated FPMap, write zeros

	// bSentinel: 508 (1 byte) - must be 0x80
	buf[508] = 0x80

	// bCryptMethod: 509 (1 byte)
	buf[509] = byte(h.BCryptMethod)

	// rgbReserved: 510-512 (2 bytes) - write zeros

	// bidNextB: 512-520 (8 bytes)
	binary.LittleEndian.PutUint64(buf[512:520], h.BidNextB)

	// dwCRCFull: 520-524 (4 bytes) - computed after all other fields
	// rgbVersionEncoded: 524-527 (3 bytes)
	// bLockSemaphore: 527 (1 byte)
	// rgbLock: 528-560 (32 bytes)

	// Compute partial CRC (bytes 8-524)
	h.DWCRCPartial = ComputeCRC(buf[8:524])
	binary.LittleEndian.PutUint32(buf[4:8], h.DWCRCPartial)

	// Compute full CRC (bytes 8-520)
	h.DWCRCFull = ComputeCRC(buf[8:520])
	binary.LittleEndian.PutUint32(buf[520:524], h.DWCRCFull)

	return buf, nil
}

// SerializeHeaderANSI serializes an ANSI format header (512 bytes).
func SerializeHeaderANSI(h *Header) ([]byte, error) {
	buf := make([]byte, HeaderSizeANSI)

	// Common header fields (0-24)
	binary.LittleEndian.PutUint32(buf[0:4], h.DWMagic)
	binary.LittleEndian.PutUint32(buf[4:8], h.DWCRCPartial)
	binary.LittleEndian.PutUint16(buf[8:10], h.WMagicClient)
	binary.LittleEndian.PutUint16(buf[10:12], h.WVer)
	binary.LittleEndian.PutUint16(buf[12:14], h.WVerClient)
	buf[14] = h.BPlatformCreate
	buf[15] = h.BPlatformAccess
	binary.LittleEndian.PutUint32(buf[16:20], h.DWOpenDBID)
	binary.LittleEndian.PutUint32(buf[20:24], h.DWOpenClaimID)

	// bidNextB: 24-28 (4 bytes) - in ANSI, this comes earlier
	binary.LittleEndian.PutUint32(buf[24:28], uint32(h.BidNextB)) //nolint:gosec // G115: ANSI BID is 32-bit
	// bidNextP: 28-32 (4 bytes)
	binary.LittleEndian.PutUint32(buf[28:32], uint32(h.BidNextP)) //nolint:gosec // G115: ANSI BID is 32-bit
	// dwUnique: 32-36 (4 bytes)
	binary.LittleEndian.PutUint32(buf[32:36], h.DWUnique)

	// rgnid[32]: 36-164 (128 bytes) - node ID counters, write zeros

	// Root structure: 164-204 (40 bytes)
	serializeRootANSI(buf[164:], &h.Root)

	// rgbFM: 204-332 (128 bytes) - deprecated
	// rgbFP: 332-460 (128 bytes) - deprecated

	// bSentinel: 460 (1 byte)
	buf[460] = 0x80

	// bCryptMethod: 461 (1 byte)
	buf[461] = byte(h.BCryptMethod)

	// rgbReserved: 462-464 (2 bytes)
	// ullReserved: 464-472 (8 bytes)
	// Additional reserved fields

	// Compute CRC (bytes 8-471)
	h.DWCRCPartial = ComputeCRC(buf[8:472])
	binary.LittleEndian.PutUint32(buf[4:8], h.DWCRCPartial)

	return buf, nil
}

// serializeRootUnicode serializes the Root structure for Unicode format.
func serializeRootUnicode(buf []byte, r *Root) {
	// Root structure for Unicode (80 bytes):
	// cOrphans: 0-4 (4 bytes)
	binary.LittleEndian.PutUint32(buf[0:4], r.COrphans)
	// padding: 4-8 (4 bytes)
	// ibFileEOF: 8-16 (8 bytes)
	binary.LittleEndian.PutUint64(buf[8:16], r.IBFileEOF)
	// ibAMapLast: 16-24 (8 bytes)
	binary.LittleEndian.PutUint64(buf[16:24], r.IBAMapLast)
	// cbAMapFree: 24-32 (8 bytes)
	binary.LittleEndian.PutUint64(buf[24:32], r.CBAMapFree)
	// cbPMapFree: 32-40 (8 bytes)
	binary.LittleEndian.PutUint64(buf[32:40], r.CBPMapFree)
	// brefNBT: 40-56 (16 bytes) - bid(8) + ib(8)
	binary.LittleEndian.PutUint64(buf[40:48], r.BRefNBT.BID)
	binary.LittleEndian.PutUint64(buf[48:56], r.BRefNBT.IB)
	// brefBBT: 56-72 (16 bytes)
	binary.LittleEndian.PutUint64(buf[56:64], r.BRefBBT.BID)
	binary.LittleEndian.PutUint64(buf[64:72], r.BRefBBT.IB)
	// fAMapValid: 72 (1 byte)
	buf[72] = r.FAMapValid
	// bARVec: 73 (1 byte)
	buf[73] = r.BARVec
	// cARVec: 74-76 (2 bytes)
	binary.LittleEndian.PutUint16(buf[74:76], r.CARVec)
}

// serializeRootANSI serializes the Root structure for ANSI format.
func serializeRootANSI(buf []byte, r *Root) {
	// Root structure for ANSI (40 bytes):
	// cOrphans: 0-4 (4 bytes)
	binary.LittleEndian.PutUint32(buf[0:4], r.COrphans)
	// ibFileEOF: 4-8 (4 bytes)
	binary.LittleEndian.PutUint32(buf[4:8], uint32(r.IBFileEOF)) //nolint:gosec // G115: ANSI file size is 32-bit
	// ibAMapLast: 8-12 (4 bytes)
	binary.LittleEndian.PutUint32(buf[8:12], uint32(r.IBAMapLast)) //nolint:gosec // G115: ANSI offset is 32-bit
	// cbAMapFree: 12-16 (4 bytes)
	binary.LittleEndian.PutUint32(buf[12:16], uint32(r.CBAMapFree)) //nolint:gosec // G115: ANSI size is 32-bit
	// cbPMapFree: 16-20 (4 bytes)
	binary.LittleEndian.PutUint32(buf[16:20], uint32(r.CBPMapFree)) //nolint:gosec // G115: ANSI size is 32-bit
	// brefNBT: 20-28 (8 bytes) - bid(4) + ib(4)
	binary.LittleEndian.PutUint32(buf[20:24], uint32(r.BRefNBT.BID)) //nolint:gosec // G115: ANSI BID is 32-bit
	binary.LittleEndian.PutUint32(buf[24:28], uint32(r.BRefNBT.IB))  //nolint:gosec // G115: ANSI offset is 32-bit
	// brefBBT: 28-36 (8 bytes)
	binary.LittleEndian.PutUint32(buf[28:32], uint32(r.BRefBBT.BID)) //nolint:gosec // G115: ANSI BID is 32-bit
	binary.LittleEndian.PutUint32(buf[32:36], uint32(r.BRefBBT.IB))  //nolint:gosec // G115: ANSI offset is 32-bit
	// fAMapValid: 36 (1 byte)
	buf[36] = r.FAMapValid
	// bARVec: 37 (1 byte)
	buf[37] = r.BARVec
	// cARVec: 38-40 (2 bytes)
	binary.LittleEndian.PutUint16(buf[38:40], r.CARVec)
}

// WriteHeader writes the header to the given writer at position 0.
func WriteHeader(w io.WriterAt, h *Header) error {
	data, err := SerializeHeader(h)
	if err != nil {
		return fmt.Errorf("failed to serialize header: %w", err)
	}

	n, err := w.WriteAt(data, 0)
	if err != nil {
		return fmt.Errorf("failed to write header: %w", err)
	}
	if n != len(data) {
		return fmt.Errorf("incomplete header write: wrote %d bytes, expected %d", n, len(data))
	}

	return nil
}

// NewHeader creates a new PST header with default values.
// This is used when creating a new PST file.
func NewHeader(format PSTFormat, clientType uint16) *Header {
	h := &Header{
		DWMagic:         PSTMagic,
		WMagicClient:    clientType,
		BPlatformCreate: 0x01, // Win32
		BPlatformAccess: 0x01, // Win32
		Format:          format,
		BCryptMethod:    CryptMethodPermute, // Default encryption
	}

	if format == FormatUnicode {
		h.WVer = DatabaseFormatUnicodeMax // Version 23
		h.WVerClient = 19                 // Standard client version
	} else {
		h.WVer = DatabaseFormatANSIMax // Version 15
		h.WVerClient = 19
	}

	// Initialize root with valid AMap status
	h.Root.FAMapValid = byte(AMapStatusValid)

	return h
}

// SetAMapStatus updates the AMap validity status in the header.
// This is used during the two-phase commit protocol.
func (h *Header) SetAMapStatus(status AMapStatus) {
	h.Root.FAMapValid = byte(status)
}

// GetAMapStatus returns the current AMap validity status.
func (h *Header) GetAMapStatus() AMapStatus {
	return AMapStatus(h.Root.FAMapValid)
}

// UpdateBTreeRoots updates the B-tree root references in the header.
func (h *Header) UpdateBTreeRoots(nbtRef, bbtRef BlockReference) {
	h.Root.BRefNBT = nbtRef
	h.Root.BRefBBT = bbtRef
}

// UpdateFileSize updates the file size in the header.
func (h *Header) UpdateFileSize(size uint64) {
	h.Root.IBFileEOF = size
}

// UpdateNextBlockID updates the next block ID counter.
func (h *Header) UpdateNextBlockID(bid uint64) {
	h.BidNextB = bid
}

// IncrementUnique increments the unique counter.
// This should be called on each modification.
func (h *Header) IncrementUnique() {
	h.DWUnique++
}
