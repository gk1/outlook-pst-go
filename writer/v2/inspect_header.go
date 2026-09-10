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
	Reserved1      uint32
	Reserved2      uint32
	BidNextP       uint64
	Unique         uint32
	NIDs           [32]uint32
	Root           RootView
	Sentinel       byte
	Crypt          byte
	BidNextB       uint64
	CRCFull        uint32
	Raw            []byte
}

// RootView is a decoded 72-byte Unicode ROOT. See MS-PST 2.2.2.5.
type RootView struct {
	Reserved  uint32
	FileEOF   uint64
	AMapLast  uint64
	AMapFree  uint64
	PMapFree  uint64
	NBTBID    uint64
	NBTIB     uint64
	BBTBID    uint64
	BBTIB     uint64
	AMapValid byte
	ARVec     byte
	CARVec    uint16
	Raw       []byte
}

// HeaderDraft is the input to EncodeUnicodeHeader.
// Reserved HEADER/ROOT bytes are always written as zero. rgbFM/rgbFP are
// always 0xFF. Platform bytes are always 0x01. wVer is Unicode 23 and
// wVerClient is 19 (zero defaults to those; any other value is rejected).
type HeaderDraft struct {
	MagicClient uint16
	WVer        uint16
	WVerClient  uint16
	BidNextP    uint64
	Unique      uint32
	NIDs        [32]uint32
	Root        RootDraft
	Crypt       byte
	BidNextB    uint64
}

// RootDraft is the input to EncodeUnicodeRoot.
// fAMapValid is written as given (0=INVALID_AMAP is preserved for
// transaction staging). EncodeUnicodeRoot never coerces 0 to VALID_AMAP2.
type RootDraft struct {
	FileEOF   uint64
	AMapLast  uint64
	AMapFree  uint64
	PMapFree  uint64
	NBTBID    uint64
	NBTIB     uint64
	BBTBID    uint64
	BBTIB     uint64
	AMapValid byte
}

// DefaultRgNID returns the blank-PST nidIndex table from MS-PST 2.2.2.6.
func DefaultRgNID() [32]uint32 {
	var nids [32]uint32
	for i := range nids {
		nids[i] = NIDIndexDefault
	}
	nids[NIDTypeSearchFolder] = NIDIndexSearchFolder
	nids[NIDTypeNormalMessage] = NIDIndexNormalMessage
	nids[NIDTypeAssocMessage] = NIDIndexAssocMessage
	return nids
}

// DefaultHeaderDraft returns a committed Unicode v23 header skeleton.
// NBT/BBT BREFs are deliberately unassigned (BID 0 / IB 0). Page and block
// BID counters start at FirstAllocBID so they do not collide with the null BID.
func DefaultHeaderDraft() HeaderDraft {
	return HeaderDraft{
		MagicClient: ClientMagicPST,
		WVer:        UnicodeWVer,
		WVerClient:  ClientVerPST,
		BidNextP:    FirstAllocBID,
		Unique:      1,
		NIDs:        DefaultRgNID(),
		Crypt:       CryptNone,
		BidNextB:    FirstAllocBID,
		Root: RootDraft{
			FileEOF:   FirstAMapPageOffset + PageSize,
			AMapLast:  FirstAMapPageOffset,
			AMapValid: AMapValid2,
		},
	}
}

func validateHeaderDraft(d HeaderDraft) error {
	client := d.MagicClient
	if client == 0 {
		client = ClientMagicPST
	}
	if client == ClientMagicOST {
		return unsupported(FeatureOST, "wMagicClient SO")
	}
	if client != ClientMagicPST {
		return invalidArg("wMagicClient", "got 0x%04x want 0x%04x (MS-PST %s)", client, ClientMagicPST, SectionHeader)
	}
	wver := d.WVer
	if wver == 0 {
		wver = UnicodeWVer
	}
	if wver >= 14 && wver <= 15 {
		return unsupported(FeatureANSI, fmt.Sprintf("wVer=%d (MS-PST %s: do not create new ANSI PST files)", wver, SectionANSICreate))
	}
	if wver == UnicodeWVerWIP {
		return unsupported(FeatureWIPCrypt, fmt.Sprintf("wVer=%d (MS-PST %s)", wver, SectionHeader))
	}
	if wver != UnicodeWVer {
		return invalidArg("wVer", "got %d, this writer emits wVer=%d so output round-trips through disk.ReadHeader (MS-PST %s)", wver, UnicodeWVer, SectionHeader)
	}
	wverClient := d.WVerClient
	if wverClient == 0 {
		wverClient = ClientVerPST
	}
	if wverClient != ClientVerPST {
		return invalidArg("wVerClient", "got %d want %d (MS-PST %s)", wverClient, ClientVerPST, SectionHeader)
	}
	switch d.Crypt {
	case CryptNone, CryptPermute, CryptCyclic:
	case CryptWIP:
		return unsupported(FeatureWIPCrypt, "bCryptMethod 0x10")
	default:
		return invalidArg("bCryptMethod", "unknown 0x%02x (MS-PST %s)", d.Crypt, SectionCrypt)
	}
	switch d.Root.AMapValid {
	case AMapValid2, AMapInvalid:
	case AMapValid1:
		return invalidArg("fAMapValid", "VALID_AMAP1 (0x01) is deprecated (MS-PST %s)", SectionAMapTxn)
	default:
		return invalidArg("fAMapValid", "unknown 0x%02x (MS-PST %s)", d.Root.AMapValid, SectionRoot)
	}
	return nil
}

func writeHeaderCRC(buf []byte) {
	full := crc32PST(buf[OffMagicClient : OffMagicClient+HeaderFullCRCLen])
	binary.LittleEndian.PutUint32(buf[OffCRCFull:], full)
	partial := crc32PST(buf[OffMagicClient : OffMagicClient+HeaderPartialCRCLen])
	binary.LittleEndian.PutUint32(buf[OffCRCPartial:], partial)
}

// EncodeUnicodeRoot writes the 72-byte Unicode ROOT. Reserved fields are zero.
// fAMapValid is preserved, including INVALID_AMAP (0) for transaction staging.
func EncodeUnicodeRoot(r RootDraft) ([]byte, error) {
	switch r.AMapValid {
	case AMapValid2, AMapInvalid:
	case AMapValid1:
		return nil, invalidArg("fAMapValid", "VALID_AMAP1 (0x01) is deprecated (MS-PST %s)", SectionAMapTxn)
	default:
		return nil, invalidArg("fAMapValid", "unknown 0x%02x (MS-PST %s)", r.AMapValid, SectionRoot)
	}
	buf := make([]byte, UnicodeRootSize)
	binary.LittleEndian.PutUint64(buf[OffRootFileEOF:], r.FileEOF)
	binary.LittleEndian.PutUint64(buf[OffRootAMapLast:], r.AMapLast)
	binary.LittleEndian.PutUint64(buf[OffRootAMapFree:], r.AMapFree)
	binary.LittleEndian.PutUint64(buf[OffRootPMapFree:], r.PMapFree)
	binary.LittleEndian.PutUint64(buf[OffRootNBTBID:], r.NBTBID)
	binary.LittleEndian.PutUint64(buf[OffRootNBTIB:], r.NBTIB)
	binary.LittleEndian.PutUint64(buf[OffRootBBTBID:], r.BBTBID)
	binary.LittleEndian.PutUint64(buf[OffRootBBTIB:], r.BBTIB)
	buf[OffRootAMapValid] = r.AMapValid
	return buf, nil
}

// InspectRoot validates a 72-byte Unicode ROOT.
func InspectRoot(raw []byte) (*RootView, error) {
	if len(raw) < UnicodeRootSize {
		return nil, invariant(SectionRoot, "size", "ROOT is %d bytes, need %d", len(raw), UnicodeRootSize)
	}
	b := raw[:UnicodeRootSize]
	reserved := binary.LittleEndian.Uint32(b[OffRootReserved:])
	if reserved != 0 {
		return nil, invariant(SectionRoot, "dwReserved", "got 0x%08x want 0 (MS-PST %s)", reserved, SectionRoot)
	}
	if b[OffRootARVec] != 0 {
		return nil, invariant(SectionRoot, "bReserved", "got 0x%02x want 0 (MS-PST %s)", b[OffRootARVec], SectionRoot)
	}
	car := binary.LittleEndian.Uint16(b[OffRootCARVec:])
	if car != 0 {
		return nil, invariant(SectionRoot, "wReserved", "got 0x%04x want 0 (MS-PST %s)", car, SectionRoot)
	}
	amap := b[OffRootAMapValid]
	switch amap {
	case AMapValid2, AMapInvalid:
		// MS-PST 2.2.2.5. InspectHeader requires VALID_AMAP2 for a committed
		// file; transaction staging uses INVALID_AMAP. VALID_AMAP1 is rejected.
	case AMapValid1:
		return nil, invariant(SectionRoot, "fAMapValid", "VALID_AMAP1 (0x01) is deprecated (MS-PST %s)", SectionAMapTxn)
	default:
		return nil, invariant(SectionRoot, "fAMapValid", "unknown 0x%02x", amap)
	}
	return &RootView{
		Reserved:  reserved,
		FileEOF:   binary.LittleEndian.Uint64(b[OffRootFileEOF:]),
		AMapLast:  binary.LittleEndian.Uint64(b[OffRootAMapLast:]),
		AMapFree:  binary.LittleEndian.Uint64(b[OffRootAMapFree:]),
		PMapFree:  binary.LittleEndian.Uint64(b[OffRootPMapFree:]),
		NBTBID:    binary.LittleEndian.Uint64(b[OffRootNBTBID:]),
		NBTIB:     binary.LittleEndian.Uint64(b[OffRootNBTIB:]),
		BBTBID:    binary.LittleEndian.Uint64(b[OffRootBBTBID:]),
		BBTIB:     binary.LittleEndian.Uint64(b[OffRootBBTIB:]),
		AMapValid: amap,
		ARVec:     b[OffRootARVec],
		CARVec:    car,
		Raw:       append([]byte(nil), b...),
	}, nil
}

// EncodeUnicodeHeader writes a 564-byte HEADER at spec offsets and CRCs.
func EncodeUnicodeHeader(d HeaderDraft) ([]byte, error) {
	if err := validateHeaderDraft(d); err != nil {
		return nil, err
	}
	if d.MagicClient == 0 {
		d.MagicClient = ClientMagicPST
	}
	if d.WVer == 0 {
		d.WVer = UnicodeWVer
	}
	if d.WVerClient == 0 {
		d.WVerClient = ClientVerPST
	}
	root, err := EncodeUnicodeRoot(d.Root)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, UnicodeHeaderSize)
	binary.LittleEndian.PutUint32(buf[OffMagic:], PSTMagic)
	binary.LittleEndian.PutUint16(buf[OffMagicClient:], d.MagicClient)
	binary.LittleEndian.PutUint16(buf[OffWVer:], d.WVer)
	binary.LittleEndian.PutUint16(buf[OffWVerClient:], d.WVerClient)
	buf[OffPlatformCreate] = PlatformWin32
	buf[OffPlatformAccess] = PlatformWin32
	binary.LittleEndian.PutUint64(buf[OffBidNextP:], d.BidNextP)
	binary.LittleEndian.PutUint32(buf[OffUnique:], d.Unique)
	for i, n := range d.NIDs {
		binary.LittleEndian.PutUint32(buf[OffRgNID+i*4:], n)
	}
	copy(buf[OffRoot:], root)
	for i := OffRgbFM; i < OffRgbFP; i++ {
		buf[i] = 0xFF
	}
	for i := OffRgbFP; i < OffSentinel; i++ {
		buf[i] = 0xFF
	}
	buf[OffSentinel] = Sentinel
	buf[OffCrypt] = d.Crypt
	binary.LittleEndian.PutUint64(buf[OffBidNextB:], d.BidNextB)
	writeHeaderCRC(buf)
	return buf, nil
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
	if wver == UnicodeWVerWIP {
		return nil, unsupported(FeatureWIPCrypt, fmt.Sprintf("wVer=%d (MS-PST %s)", wver, SectionHeader))
	}
	if wver != UnicodeWVer {
		return nil, invariant(SectionHeader, "wVer", "got %d want %d (writer contract; existing reader max is %d)", wver, UnicodeWVer, UnicodeWVerMax)
	}
	wverClient := binary.LittleEndian.Uint16(b[OffWVerClient:])
	if wverClient != ClientVerPST {
		return nil, invariant(SectionHeader, "wVerClient", "got %d want %d", wverClient, ClientVerPST)
	}
	client := binary.LittleEndian.Uint16(b[OffMagicClient:])
	if client == ClientMagicOST {
		return nil, unsupported(FeatureOST, "wMagicClient SO")
	}
	if client != ClientMagicPST {
		return nil, invariant(SectionHeader, "wMagicClient", "got 0x%04x want 0x%04x", client, ClientMagicPST)
	}
	if b[OffPlatformCreate] != PlatformWin32 {
		return nil, invariant(SectionHeader, "bPlatformCreate", "got 0x%02x want 0x%02x", b[OffPlatformCreate], PlatformWin32)
	}
	if b[OffPlatformAccess] != PlatformWin32 {
		return nil, invariant(SectionHeader, "bPlatformAccess", "got 0x%02x want 0x%02x", b[OffPlatformAccess], PlatformWin32)
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
	if qw := binary.LittleEndian.Uint64(b[OffQWUnused:]); qw != 0 {
		return nil, invariant(SectionHeader, "qwUnused", "got 0x%016x want 0", qw)
	}
	if align := binary.LittleEndian.Uint32(b[OffAlign:]); align != 0 {
		return nil, invariant(SectionHeader, "dwAlign", "got 0x%08x want 0", align)
	}
	if binary.LittleEndian.Uint16(b[OffRgbReserved:]) != 0 {
		return nil, invariant(SectionHeader, "rgbReserved", "got 0x%04x want 0", binary.LittleEndian.Uint16(b[OffRgbReserved:]))
	}
	for i := OffRgbFM; i < OffSentinel; i++ {
		if b[i] != 0xFF {
			field := "rgbFM"
			if i >= OffRgbFP {
				field = "rgbFP"
			}
			return nil, invariant(SectionHeader, field, "byte %d is 0x%02x, MUST be 0xFF (MS-PST %s)", i, b[i], SectionHeader)
		}
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
	root, err := InspectRoot(b[OffRoot : OffRoot+UnicodeRootSize])
	if err != nil {
		return nil, err
	}
	if root.AMapValid != AMapValid2 {
		return nil, invariant(SectionRoot, "fAMapValid", "committed header MUST use VALID_AMAP2 0x02, got 0x%02x (MS-PST 2.6.1.3.7)", root.AMapValid)
	}
	h := &HeaderView{
		Magic:          magic,
		CRCPartial:     gotPartial,
		MagicClient:    client,
		WVer:           wver,
		WVerClient:     wverClient,
		PlatformCreate: b[OffPlatformCreate],
		PlatformAccess: b[OffPlatformAccess],
		Reserved1:      binary.LittleEndian.Uint32(b[OffReserved1:]),
		Reserved2:      binary.LittleEndian.Uint32(b[OffReserved2:]),
		BidNextP:       binary.LittleEndian.Uint64(b[OffBidNextP:]),
		Unique:         binary.LittleEndian.Uint32(b[OffUnique:]),
		Root:           *root,
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
