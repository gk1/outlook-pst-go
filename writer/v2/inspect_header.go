package writer

import (
	"encoding/binary"
	"fmt"
)

// HeaderView is a decoded Unicode HEADER. See MS-PST 2.2.2.6.
type HeaderView struct {
	Magic          uint32
	CRCPartial     uint32
	MagicClient    uint16
	WVer           uint16
	WVerClient     uint16
	PlatformCreate byte
	PlatformAccess byte
	BidNextP       uint64
	Unique         uint32
	NIDs           [32]uint32
	FileEOF        uint64
	AMapLast       uint64
	AMapFree       uint64
	PMapFree       uint64
	NBTBID         uint64
	NBTIB          uint64
	BBTBID         uint64
	BBTIB          uint64
	AMapValid      byte
	Sentinel       byte
	Crypt          byte
	BidNextB       uint64
	CRCFull        uint32
	Raw            []byte
}

// HeaderDraft is the input to EncodeUnicodeHeader.
type HeaderDraft struct {
	MagicClient    uint16
	WVer           uint16
	WVerClient     uint16
	PlatformCreate byte
	PlatformAccess byte
	BidNextP       uint64
	Unique         uint32
	NIDs           [32]uint32
	FileEOF        uint64
	AMapLast       uint64
	AMapFree       uint64
	PMapFree       uint64
	NBTBID         uint64
	NBTIB          uint64
	BBTBID         uint64
	BBTIB          uint64
	AMapValid      byte
	Crypt          byte
	BidNextB       uint64
	FM             byte // rgbFM fill; 0 means 0xFF for new files
	FP             byte
}

// DefaultHeaderDraft returns a committed Unicode v23 header skeleton.
func DefaultHeaderDraft() HeaderDraft {
	var nids [32]uint32
	for i := range nids {
		nids[i] = 0x400
	}
	nids[NIDTypeInternal] = 0x80
	nids[NIDTypeNormalFolder] = 0x400
	return HeaderDraft{
		MagicClient:    ClientMagicPST,
		WVer:           UnicodeWVer,
		WVerClient:     ClientVerPST,
		PlatformCreate: PlatformWin32,
		PlatformAccess: PlatformWin32,
		BidNextP:       4,
		Unique:         1,
		NIDs:           nids,
		FileEOF:        FirstAMapPageOffset + PageSize,
		AMapLast:       FirstAMapPageOffset,
		AMapValid:      AMapValid2,
		Crypt:          CryptNone,
		BidNextB:       4,
		NBTBID:         4,
		NBTIB:          0x2000, // placeholder; codecs assign real BREFs
		BBTBID:         8,
		BBTIB:          0x2200,
		FM:             0xFF,
		FP:             0xFF,
	}
}

// EncodeUnicodeHeader writes a 564-byte HEADER at spec offsets and CRCs.
func EncodeUnicodeHeader(d HeaderDraft) []byte {
	buf := make([]byte, UnicodeHeaderSize)
	if d.MagicClient == 0 {
		d.MagicClient = ClientMagicPST
	}
	if d.WVer == 0 {
		d.WVer = UnicodeWVer
	}
	if d.WVerClient == 0 {
		d.WVerClient = ClientVerPST
	}
	if d.PlatformCreate == 0 {
		d.PlatformCreate = PlatformWin32
	}
	if d.PlatformAccess == 0 {
		d.PlatformAccess = PlatformWin32
	}
	if d.FM == 0 {
		d.FM = 0xFF
	}
	if d.FP == 0 {
		d.FP = 0xFF
	}
	binary.LittleEndian.PutUint32(buf[OffMagic:], PSTMagic)
	binary.LittleEndian.PutUint16(buf[OffMagicClient:], d.MagicClient)
	binary.LittleEndian.PutUint16(buf[OffWVer:], d.WVer)
	binary.LittleEndian.PutUint16(buf[OffWVerClient:], d.WVerClient)
	buf[OffPlatformCreate] = d.PlatformCreate
	buf[OffPlatformAccess] = d.PlatformAccess
	binary.LittleEndian.PutUint64(buf[OffBidNextP:], d.BidNextP)
	binary.LittleEndian.PutUint32(buf[OffUnique:], d.Unique)
	for i, n := range d.NIDs {
		binary.LittleEndian.PutUint32(buf[OffRgNID+i*4:], n)
	}
	binary.LittleEndian.PutUint64(buf[OffRoot+OffRootFileEOF:], d.FileEOF)
	binary.LittleEndian.PutUint64(buf[OffRoot+OffRootAMapLast:], d.AMapLast)
	binary.LittleEndian.PutUint64(buf[OffRoot+OffRootAMapFree:], d.AMapFree)
	binary.LittleEndian.PutUint64(buf[OffRoot+OffRootPMapFree:], d.PMapFree)
	binary.LittleEndian.PutUint64(buf[OffRoot+OffRootNBTBID:], d.NBTBID)
	binary.LittleEndian.PutUint64(buf[OffRoot+OffRootNBTIB:], d.NBTIB)
	binary.LittleEndian.PutUint64(buf[OffRoot+OffRootBBTBID:], d.BBTBID)
	binary.LittleEndian.PutUint64(buf[OffRoot+OffRootBBTIB:], d.BBTIB)
	buf[OffRoot+OffRootAMapValid] = d.AMapValid
	for i := OffRgbFM; i < OffRgbFP; i++ {
		buf[i] = d.FM
	}
	for i := OffRgbFP; i < OffSentinel; i++ {
		buf[i] = d.FP
	}
	buf[OffSentinel] = Sentinel
	buf[OffCrypt] = d.Crypt
	binary.LittleEndian.PutUint64(buf[OffBidNextB:], d.BidNextB)
	// CRC: partial = 471 bytes from offset 8; full = 516 bytes from offset 8.
	full := crc32PST(buf[OffMagicClient : OffMagicClient+HeaderFullCRCLen])
	binary.LittleEndian.PutUint32(buf[OffCRCFull:], full)
	partial := crc32PST(buf[OffMagicClient : OffMagicClient+HeaderPartialCRCLen])
	binary.LittleEndian.PutUint32(buf[OffCRCPartial:], partial)
	return buf
}

// InspectHeader validates a Unicode HEADER and returns a view.
// Committed files must use fAMapValid = VALID_AMAP2 (0x02).
func InspectHeader(raw []byte) (*HeaderView, error) {
	if len(raw) < UnicodeHeaderSize {
		return nil, invariant(SectionHeader, "size", "header is %d bytes, need %d", len(raw), UnicodeHeaderSize)
	}
	b := raw[:UnicodeHeaderSize]
	magic := binary.LittleEndian.Uint32(b[OffMagic:])
	if magic != PSTMagic {
		return nil, invariant(SectionHeader, "dwMagic", "got 0x%08x want 0x%08x", magic, PSTMagic)
	}
	wver := binary.LittleEndian.Uint16(b[OffWVer:])
	if wver >= 14 && wver <= 15 {
		return nil, unsupported(FeatureANSI, fmt.Sprintf("wVer=%d (MS-PST %s: do not create new ANSI PST files)", wver, SectionANSICreate))
	}
	if wver < UnicodeWVerMin {
		return nil, invariant(SectionHeader, "wVer", "unsupported version %d", wver)
	}
	client := binary.LittleEndian.Uint16(b[OffMagicClient:])
	if client == ClientMagicOST {
		return nil, unsupported(FeatureOST, "wMagicClient SO")
	}
	if client != ClientMagicPST {
		return nil, invariant(SectionHeader, "wMagicClient", "got 0x%04x want 0x%04x", client, ClientMagicPST)
	}
	if b[OffSentinel] != Sentinel {
		return nil, invariant(SectionHeader, "bSentinel", "got 0x%02x want 0x%02x", b[OffSentinel], Sentinel)
	}
	crypt := b[OffCrypt]
	switch crypt {
	case CryptNone, CryptPermute, CryptCyclic:
	case CryptWIP:
		return nil, unsupported(FeatureWIPCrypt, "bCryptMethod 0x10")
	default:
		return nil, invariant(SectionHeader, "bCryptMethod", "unknown 0x%02x (MS-PST %s)", crypt, SectionCrypt)
	}
	wantPartial := crc32PST(b[OffMagicClient : OffMagicClient+HeaderPartialCRCLen])
	gotPartial := binary.LittleEndian.Uint32(b[OffCRCPartial:])
	if gotPartial != wantPartial {
		return nil, invariant(SectionHeader, "dwCRCPartial", "got 0x%08x want 0x%08x over %d bytes from offset 8 (MS-PST %s)", gotPartial, wantPartial, HeaderPartialCRCLen, SectionCRC)
	}
	wantFull := crc32PST(b[OffMagicClient : OffMagicClient+HeaderFullCRCLen])
	gotFull := binary.LittleEndian.Uint32(b[OffCRCFull:])
	if gotFull != wantFull {
		return nil, invariant(SectionHeader, "dwCRCFull", "got 0x%08x want 0x%08x over %d bytes from offset 8 (MS-PST %s)", gotFull, wantFull, HeaderFullCRCLen, SectionCRC)
	}
	amap := b[OffRoot+OffRootAMapValid]
	switch amap {
	case AMapValid2:
	case AMapInvalid:
		return nil, invariant(SectionRoot, "fAMapValid", "INVALID_AMAP 0x00 is not a committed file (MS-PST 2.6.1.3.7)")
	case AMapValid1:
		return nil, invariant(SectionRoot, "fAMapValid", "VALID_AMAP1 0x01 is deprecated; new files use VALID_AMAP2 0x02")
	default:
		return nil, invariant(SectionRoot, "fAMapValid", "unknown 0x%02x", amap)
	}
	h := &HeaderView{
		Magic:          magic,
		CRCPartial:     gotPartial,
		MagicClient:    client,
		WVer:           wver,
		WVerClient:     binary.LittleEndian.Uint16(b[OffWVerClient:]),
		PlatformCreate: b[OffPlatformCreate],
		PlatformAccess: b[OffPlatformAccess],
		BidNextP:       binary.LittleEndian.Uint64(b[OffBidNextP:]),
		Unique:         binary.LittleEndian.Uint32(b[OffUnique:]),
		FileEOF:        binary.LittleEndian.Uint64(b[OffRoot+OffRootFileEOF:]),
		AMapLast:       binary.LittleEndian.Uint64(b[OffRoot+OffRootAMapLast:]),
		AMapFree:       binary.LittleEndian.Uint64(b[OffRoot+OffRootAMapFree:]),
		PMapFree:       binary.LittleEndian.Uint64(b[OffRoot+OffRootPMapFree:]),
		NBTBID:         binary.LittleEndian.Uint64(b[OffRoot+OffRootNBTBID:]),
		NBTIB:          binary.LittleEndian.Uint64(b[OffRoot+OffRootNBTIB:]),
		BBTBID:         binary.LittleEndian.Uint64(b[OffRoot+OffRootBBTBID:]),
		BBTIB:          binary.LittleEndian.Uint64(b[OffRoot+OffRootBBTIB:]),
		AMapValid:      amap,
		Sentinel:       b[OffSentinel],
		Crypt:          crypt,
		BidNextB:       binary.LittleEndian.Uint64(b[OffBidNextB:]),
		CRCFull:        gotFull,
		Raw:            append([]byte(nil), b...),
	}
	for i := 0; i < 32; i++ {
		h.NIDs[i] = binary.LittleEndian.Uint32(b[OffRgNID+i*4:])
	}
	return h, nil
}
