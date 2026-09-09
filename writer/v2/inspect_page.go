package writer

import "encoding/binary"

// PageView is a decoded 512-byte page. See MS-PST 2.2.2.7.1.
type PageView struct {
	Type    byte
	TypeRep byte
	Sig     uint16
	CRC     uint32
	BID     uint64
	Offset  uint64
	Payload []byte
	Raw     []byte
}

// EncodePage builds a 512-byte Unicode page with trailer at the end.
func EncodePage(payload []byte, pageType byte, bid, offset uint64) ([]byte, error) {
	max := PageSize - UnicodePageTrailer
	if len(payload) > max {
		return nil, invalidArg("payload", "page payload %d exceeds %d", len(payload), max)
	}
	if !knownPageType(pageType) {
		return nil, invalidArg("ptype", "unknown page type 0x%02x", pageType)
	}
	buf := make([]byte, PageSize)
	copy(buf, payload)
	crc := crc32PST(buf[:max])
	pageBID := bid
	var sig uint16
	if zeroSigPageType(pageType) {
		// AMap/PMap/FMap/FPMap: wSig is 0 and BID equals IB. MS-PST 2.2.2.7.1.
		pageBID = offset
		sig = 0
	} else {
		sig = signature(bid, offset)
	}
	tr := buf[max:]
	tr[0] = pageType
	tr[1] = pageType
	binary.LittleEndian.PutUint16(tr[2:4], sig)
	binary.LittleEndian.PutUint32(tr[4:8], crc)
	binary.LittleEndian.PutUint64(tr[8:16], pageBID)
	return buf, nil
}

// InspectPage validates a Unicode page at file offset.
func InspectPage(raw []byte, offset uint64) (*PageView, error) {
	if len(raw) != PageSize {
		return nil, invariant(SectionPageTrailer, "size", "page is %d bytes, need %d", len(raw), PageSize)
	}
	max := PageSize - UnicodePageTrailer
	tr := raw[max:]
	ptype := tr[0]
	repeat := tr[1]
	if ptype != repeat {
		return nil, invariant(SectionPageTrailer, "ptypeRepeat", "ptype 0x%02x != ptypeRepeat 0x%02x", ptype, repeat)
	}
	if !knownPageType(ptype) {
		return nil, invariant(SectionPageTrailer, "ptype", "unknown 0x%02x", ptype)
	}
	gotCRC := binary.LittleEndian.Uint32(tr[4:8])
	wantCRC := crc32PST(raw[:max])
	if gotCRC != wantCRC {
		return nil, invariant(SectionPageTrailer, "dwCRC", "got 0x%08x want 0x%08x over first %d bytes (MS-PST %s)", gotCRC, wantCRC, max, SectionCRC)
	}
	bid := binary.LittleEndian.Uint64(tr[8:16])
	gotSig := binary.LittleEndian.Uint16(tr[2:4])
	if zeroSigPageType(ptype) {
		if gotSig != 0 {
			return nil, invariant(SectionPageTrailer, "wSig", "map page wSig 0x%04x want 0 (MS-PST %s)", gotSig, SectionPageTrailer)
		}
		if bid != offset {
			return nil, invariant(SectionPageTrailer, "bid", "map page bid 0x%x want IB 0x%x (MS-PST %s)", bid, offset, SectionPageTrailer)
		}
	} else {
		wantSig := signature(bid, offset)
		if gotSig != wantSig {
			return nil, invariant(SectionSignature, "wSig", "got 0x%04x want 0x%04x for bid=0x%x ib=0x%x", gotSig, wantSig, bid, offset)
		}
	}
	return &PageView{
		Type:    ptype,
		TypeRep: repeat,
		Sig:     gotSig,
		CRC:     gotCRC,
		BID:     bid,
		Offset:  offset,
		Payload: append([]byte(nil), raw[:max]...),
		Raw:     append([]byte(nil), raw...),
	}, nil
}
