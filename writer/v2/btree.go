package writer

import (
	"encoding/binary"
)

// BREF is a Unicode block/page reference (BID + IB). See MS-PST 2.2.2.4.
type BREF struct {
	BID uint64
	IB  uint64
}

// NBTEntry is a Unicode NBTENTRY. See MS-PST 2.2.2.7.7.4.
type NBTEntry struct {
	NID       uint64
	DataBID   uint64
	SubBID    uint64
	ParentNID uint32
}

func (e NBTEntry) key() uint64 { return e.NID }

// BBTEntry is a Unicode BBTENTRY. See MS-PST 2.2.2.7.7.3.
type BBTEntry struct {
	BID      uint64
	IB       uint64
	CB       uint16
	RefCount uint16
	// DataRefs/SubRefs occupy the unused 4 bytes of the 24-byte Unicode
	// BBTENTRY slot so reopen can recover extra-ref kinds before PST-006
	// reconstructs them from XBLOCK/SLENTRY. Zero when there are none.
	DataRefs uint16
	SubRefs  uint16
}

func (e BBTEntry) key() uint64 { return e.BID }

// BTEntry is a Unicode BTENTRY. btkey is the first key of the child page.
// See MS-PST 2.2.2.7.7.2.
type BTEntry struct {
	Key uint64
	Ref BREF
}

func (e BTEntry) key() uint64 { return e.Key }

// BTPageView is a decoded Unicode BTPAGE. See MS-PST 2.2.2.7.7.1.
type BTPageView struct {
	Type    byte
	Level   byte
	Count   byte
	Max     byte
	EntSize byte
	NBT     []NBTEntry
	BBT     []BBTEntry
	Kids    []BTEntry
	Page    *PageView
}

func btEntSize(pageType, level byte) (int, error) {
	switch {
	case pageType == PageNBT && level == 0:
		return NBTLeafEntrySize, nil
	case pageType == PageBBT && level == 0:
		return BBTLeafEntrySize, nil
	case (pageType == PageNBT || pageType == PageBBT) && level > 0:
		return BTNonleafEntrySize, nil
	default:
		return 0, invalidArg("ptype", "not an NBT/BBT page 0x%02x level %d (MS-PST %s)", pageType, level, SectionBTPAGE)
	}
}

func btEntMax(entSize int) int {
	return UnicodeBTEntriesBytes / entSize
}

func encodeBTPayload(pageType, level byte, nbt []NBTEntry, bbt []BBTEntry, kids []BTEntry) ([]byte, error) {
	entSize, err := btEntSize(pageType, level)
	if err != nil {
		return nil, err
	}
	max := btEntMax(entSize)
	var n int
	switch {
	case level == 0 && pageType == PageNBT:
		n = len(nbt)
	case level == 0 && pageType == PageBBT:
		n = len(bbt)
	default:
		n = len(kids)
	}
	if n > max {
		return nil, invalidArg("cEnt", "page has %d entries, max %d (MS-PST %s)", n, max, SectionBTPAGE)
	}
	if n > 255 {
		return nil, invalidArg("cEnt", "cEnt %d exceeds BYTE", n)
	}
	buf := make([]byte, PagePayloadMax())
	var prev uint64
	for i := 0; i < n; i++ {
		off := i * entSize
		var k uint64
		switch {
		case level == 0 && pageType == PageNBT:
			e := nbt[i]
			if e.NID == 0 {
				return nil, invalidArg("nid", "null NID (MS-PST %s)", SectionNBTENTRY)
			}
			if e.NID>>32 != 0 {
				return nil, invalidArg("nid", "Unicode NID high bits must be 0, got 0x%x", e.NID)
			}
			k = e.NID
			binary.LittleEndian.PutUint64(buf[off:], e.NID)
			binary.LittleEndian.PutUint64(buf[off+8:], e.DataBID)
			binary.LittleEndian.PutUint64(buf[off+16:], e.SubBID)
			binary.LittleEndian.PutUint32(buf[off+24:], e.ParentNID)
		case level == 0 && pageType == PageBBT:
			e := bbt[i]
			if e.BID == 0 {
				return nil, invalidArg("bid", "null BBT BID (MS-PST %s)", SectionBBTENTRY)
			}
			if BIDHasReserved(e.BID) {
				return nil, invalidArg("bid", "reserved bit must be 0 (MS-PST %s)", SectionBID)
			}
			if e.RefCount == 0 {
				return nil, invalidArg("cRef", "BBT cRef must be at least 1 for the BBTENTRY (MS-PST %s)", SectionRefCount)
			}
			k = e.BID
			binary.LittleEndian.PutUint64(buf[off:], e.BID)
			binary.LittleEndian.PutUint64(buf[off+8:], e.IB)
			if uint32(e.DataRefs)+uint32(e.SubRefs) >= uint32(e.RefCount) {
				return nil, invalidArg("cRef", "extra refs data=%d sub=%d leave no BBTENTRY for BID 0x%x", e.DataRefs, e.SubRefs, e.BID)
			}
			binary.LittleEndian.PutUint16(buf[off+16:], e.CB)
			binary.LittleEndian.PutUint16(buf[off+18:], e.RefCount)
			binary.LittleEndian.PutUint16(buf[off+20:], e.DataRefs)
			binary.LittleEndian.PutUint16(buf[off+22:], e.SubRefs)
		default:
			e := kids[i]
			if e.Ref.BID == 0 {
				return nil, invalidArg("bid", "child page BID is null (MS-PST %s)", SectionBID)
			}
			k = e.Key
			binary.LittleEndian.PutUint64(buf[off:], e.Key)
			binary.LittleEndian.PutUint64(buf[off+8:], e.Ref.BID)
			binary.LittleEndian.PutUint64(buf[off+16:], e.Ref.IB)
		}
		if i > 0 && k <= prev {
			return nil, invalidArg("btkey", "entries must be strictly increasing (prev=0x%x key=0x%x, MS-PST %s)", prev, k, SectionBTPAGE)
		}
		prev = k
	}
	buf[UnicodeBTHeaderOff] = byte(n)
	buf[UnicodeBTHeaderOff+1] = byte(max)
	buf[UnicodeBTHeaderOff+2] = byte(entSize)
	buf[UnicodeBTHeaderOff+3] = level
	return buf, nil
}

// EncodeNBTLeaf encodes a Unicode NBT leaf BTPAGE.
func EncodeNBTLeaf(entries []NBTEntry, bid, ib uint64) ([]byte, error) {
	payload, err := encodeBTPayload(PageNBT, 0, entries, nil, nil)
	if err != nil {
		return nil, err
	}
	return EncodePage(payload, PageNBT, bid, ib)
}

// EncodeBBTLeaf encodes a Unicode BBT leaf BTPAGE.
func EncodeBBTLeaf(entries []BBTEntry, bid, ib uint64) ([]byte, error) {
	payload, err := encodeBTPayload(PageBBT, 0, nil, entries, nil)
	if err != nil {
		return nil, err
	}
	return EncodePage(payload, PageBBT, bid, ib)
}

// EncodeBTNonleaf encodes a Unicode NBT or BBT intermediate BTPAGE.
// Each BTEntry.Key must be the first key of the child page (MS-PST 2.2.2.7.7.2).
func EncodeBTNonleaf(pageType byte, level byte, kids []BTEntry, bid, ib uint64) ([]byte, error) {
	if level == 0 {
		return nil, invalidArg("cLevel", "nonleaf page must have cLevel > 0 (MS-PST %s)", SectionBTPAGE)
	}
	payload, err := encodeBTPayload(pageType, level, nil, nil, kids)
	if err != nil {
		return nil, err
	}
	return EncodePage(payload, pageType, bid, ib)
}

// InspectBTPage validates a Unicode NBT or BBT page at file offset.
func InspectBTPage(raw []byte, offset uint64) (*BTPageView, error) {
	pg, err := InspectPage(raw, offset)
	if err != nil {
		return nil, err
	}
	if pg.Type != PageNBT && pg.Type != PageBBT {
		return nil, invariant(SectionBTPAGE, "ptype", "got 0x%02x want NBT or BBT", pg.Type)
	}
	b := pg.Payload
	if len(b) < PagePayloadMax() {
		return nil, invariant(SectionBTPAGE, "size", "payload %d want %d", len(b), PagePayloadMax())
	}
	cEnt := int(b[UnicodeBTHeaderOff])
	cEntMax := int(b[UnicodeBTHeaderOff+1])
	cbEnt := int(b[UnicodeBTHeaderOff+2])
	cLevel := b[UnicodeBTHeaderOff+3]
	wantSize, err := btEntSize(pg.Type, cLevel)
	if err != nil {
		return nil, invariant(SectionBTPAGE, "cbEnt", "%v", err)
	}
	if cbEnt != wantSize {
		return nil, invariant(SectionBTPAGE, "cbEnt", "got %d want %d (MS-PST %s)", cbEnt, wantSize, SectionBTPAGE)
	}
	wantMax := btEntMax(cbEnt)
	if cEntMax != wantMax {
		return nil, invariant(SectionBTPAGE, "cEntMax", "got %d want %d", cEntMax, wantMax)
	}
	if cEnt > cEntMax {
		return nil, invariant(SectionBTPAGE, "cEnt", "got %d > cEntMax %d", cEnt, cEntMax)
	}
	if cEnt*cbEnt > UnicodeBTEntriesBytes {
		return nil, invariant(SectionBTPAGE, "cEnt", "entries overflow rgentries")
	}
	pad := binary.LittleEndian.Uint32(b[UnicodeBTPaddingOff:])
	if pad != 0 {
		return nil, invariant(SectionBTPAGE, "dwPadding", "got 0x%08x want 0", pad)
	}
	view := &BTPageView{
		Type:    pg.Type,
		Level:   cLevel,
		Count:   byte(cEnt),
		Max:     byte(cEntMax),
		EntSize: byte(cbEnt),
		Page:    pg,
	}
	var prev uint64
	for i := 0; i < cEnt; i++ {
		off := i * cbEnt
		var k uint64
		switch {
		case cLevel == 0 && pg.Type == PageNBT:
			e := NBTEntry{
				NID:       binary.LittleEndian.Uint64(b[off:]),
				DataBID:   binary.LittleEndian.Uint64(b[off+8:]),
				SubBID:    binary.LittleEndian.Uint64(b[off+16:]),
				ParentNID: binary.LittleEndian.Uint32(b[off+24:]),
			}
			if e.NID == 0 || e.NID>>32 != 0 {
				return nil, invariant(SectionNBTENTRY, "nid", "invalid NID 0x%x", e.NID)
			}
			k = e.NID
			view.NBT = append(view.NBT, e)
		case cLevel == 0 && pg.Type == PageBBT:
			e := BBTEntry{
				BID:      binary.LittleEndian.Uint64(b[off:]),
				IB:       binary.LittleEndian.Uint64(b[off+8:]),
				CB:       binary.LittleEndian.Uint16(b[off+16:]),
				RefCount: binary.LittleEndian.Uint16(b[off+18:]),
				DataRefs: binary.LittleEndian.Uint16(b[off+20:]),
				SubRefs:  binary.LittleEndian.Uint16(b[off+22:]),
			}
			if e.BID == 0 || BIDHasReserved(e.BID) {
				return nil, invariant(SectionBBTENTRY, "bid", "invalid BID 0x%x", e.BID)
			}
			if e.RefCount == 0 {
				return nil, invariant(SectionRefCount, "cRef", "cRef is 0 for BID 0x%x", e.BID)
			}
			k = e.BID
			view.BBT = append(view.BBT, e)
		default:
			e := BTEntry{
				Key: binary.LittleEndian.Uint64(b[off:]),
				Ref: BREF{
					BID: binary.LittleEndian.Uint64(b[off+8:]),
					IB:  binary.LittleEndian.Uint64(b[off+16:]),
				},
			}
			if e.Ref.BID == 0 {
				return nil, invariant(SectionBTENTRY, "bid", "null child BID")
			}
			k = e.Key
			view.Kids = append(view.Kids, e)
		}
		if i > 0 && k <= prev {
			return nil, invariant(SectionBTPAGE, "btkey", "entries not strictly increasing (prev=0x%x key=0x%x)", prev, k)
		}
		prev = k
	}
	return view, nil
}

func firstKey(v *BTPageView) (uint64, error) {
	if v.Count == 0 {
		return 0, invariant(SectionBTENTRY, "btkey", "child page is empty")
	}
	switch {
	case v.Level == 0 && v.Type == PageNBT:
		return v.NBT[0].NID, nil
	case v.Level == 0 && v.Type == PageBBT:
		return v.BBT[0].BID, nil
	default:
		return v.Kids[0].Key, nil
	}
}

// chooseChild is last-key-<= descent: the largest i with kids[i].Key <= key.
func chooseChild(kids []BTEntry, key uint64) (int, bool) {
	if len(kids) == 0 || key < kids[0].Key {
		return 0, false
	}
	i := 0
	for j := 1; j < len(kids); j++ {
		if kids[j].Key <= key {
			i = j
		}
	}
	return i, true
}
