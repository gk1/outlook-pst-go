package writer

import (
	"encoding/binary"
	"sort"
)

// ColumnView is one TCOLDESC. See MS-PST 2.3.4.2.
type ColumnView struct {
	PropType uint16
	PropID   uint16
	Offset   uint16
	Size     byte
	Bit      byte
}

// Tag is the 32-bit property tag (propID << 16 | propType).
func (c ColumnView) Tag() uint32 {
	return uint32(c.PropType) | uint32(c.PropID)<<16
}

// TableView is a decoded TCINFO with inline rgTCOLDESC. See MS-PST 2.3.4.1.
type TableView struct {
	Signature byte
	NumCols   byte
	RgIB      [4]uint16
	RowIndex  uint32
	RowMatrix uint32
	HidIndex  uint32
	Columns   []ColumnView
	Raw       []byte
}

// TableDraft is the input to EncodeTCINFO. Columns may be in any order.
// ibData is assigned from the 8/4, 2, then 1-byte row-data groups (with
// PidTagLtpRowId / PidTagLtpRowVer fixed when present); rgTCOLDESC is then
// serialized sorted by the 32-bit property tag (MS-PST 2.3.4.1).
type TableDraft struct {
	Columns  []ColumnView
	RowIndex uint32
	Rows     uint32
}

func columnGroup(size byte) (int, bool) {
	switch size {
	case 8:
		return 0, true
	case 4:
		return 1, true
	case 2:
		return 2, true
	case 1:
		return 3, true
	default:
		return 0, false
	}
}

func isLtpRowID(c ColumnView) bool  { return c.PropID == PidTagLtpRowId }
func isLtpRowVer(c ColumnView) bool { return c.PropID == PidTagLtpRowVer }

// assignColumnLayout copies cols, assigns ibData / iBit from row-data groups,
// and returns the slice still in assignment order (not tag order).
func assignColumnLayout(cols []ColumnView) ([]ColumnView, [4]uint16, error) {
	out := append([]ColumnView(nil), cols...)
	var rowID, rowVer int = -1, -1
	var groups [4][]int
	for i, c := range out {
		if _, ok := columnGroup(c.Size); !ok {
			return nil, [4]uint16{}, invalidArg("cbData", "column %d has invalid cbData %d (MS-PST %s allows 1,2,4,8)", i, c.Size, SectionTCOLDESC)
		}
		switch {
		case isLtpRowID(c):
			if rowID >= 0 {
				return nil, [4]uint16{}, invalidArg("tag", "duplicate PidTagLtpRowId")
			}
			if c.Size != 4 {
				return nil, [4]uint16{}, invalidArg("cbData", "PidTagLtpRowId cbData must be 4 (MS-PST %s)", SectionTCRowID)
			}
			rowID = i
		case isLtpRowVer(c):
			if rowVer >= 0 {
				return nil, [4]uint16{}, invalidArg("tag", "duplicate PidTagLtpRowVer")
			}
			if c.Size != 4 {
				return nil, [4]uint16{}, invalidArg("cbData", "PidTagLtpRowVer cbData must be 4 (MS-PST %s)", SectionTCRowID)
			}
			rowVer = i
		default:
			g, _ := columnGroup(c.Size)
			groups[g] = append(groups[g], i)
		}
	}

	reserved := map[byte]bool{}
	if rowID >= 0 {
		reserved[0] = true
	}
	if rowVer >= 0 {
		reserved[1] = true
	}
	nextBit := byte(0)
	nextFreeBit := func() byte {
		for reserved[nextBit] {
			nextBit++
		}
		b := nextBit
		nextBit++
		return b
	}

	if rowID >= 0 {
		out[rowID].Offset = 0
		out[rowID].Bit = 0
	}
	if rowVer >= 0 {
		out[rowVer].Offset = 4
		out[rowVer].Bit = 1
	}

	off := uint16(0)
	if rowID >= 0 || rowVer >= 0 {
		off = 8
	}
	for _, i := range groups[0] { // 8-byte
		out[i].Offset = off
		out[i].Bit = nextFreeBit()
		off += 8
	}
	for _, i := range groups[1] { // remaining 4-byte
		out[i].Offset = off
		out[i].Bit = nextFreeBit()
		off += 4
	}
	var rgib [4]uint16
	rgib[0] = off
	for _, i := range groups[2] { // 2-byte
		out[i].Offset = off
		out[i].Bit = nextFreeBit()
		off += 2
	}
	rgib[1] = off
	for _, i := range groups[3] { // 1-byte
		out[i].Offset = off
		out[i].Bit = nextFreeBit()
		off += 1
	}
	rgib[2] = off
	ceb := uint16(0)
	if n := len(out); n > 0 {
		ceb = uint16((n + 7) / 8)
	}
	rgib[3] = off + ceb
	return out, rgib, nil
}

func sortColumnsByTag(cols []ColumnView) error {
	seen := make(map[uint32]struct{}, len(cols))
	for _, c := range cols {
		t := c.Tag()
		if _, ok := seen[t]; ok {
			return invalidArg("tag", "duplicate property tag 0x%08x", t)
		}
		seen[t] = struct{}{}
	}
	sort.Slice(cols, func(i, j int) bool { return cols[i].Tag() < cols[j].Tag() })
	return nil
}

// EncodeTCINFO writes bType, spec-correct rgib, and inline rgTCOLDESC sorted
// by 32-bit property tag. ibData follows the 8/4, 2, 1-byte row groups.
func EncodeTCINFO(d TableDraft) ([]byte, error) {
	cols, rgib, err := assignColumnLayout(d.Columns)
	if err != nil {
		return nil, err
	}
	if err := sortColumnsByTag(cols); err != nil {
		return nil, err
	}
	n := len(cols)
	buf := make([]byte, TCINFOFixedSize+TCOLDESCSize*n)
	buf[0] = HeapSigTC
	buf[1] = byte(n)
	for i := 0; i < 4; i++ {
		binary.LittleEndian.PutUint16(buf[2+i*2:], rgib[i])
	}
	binary.LittleEndian.PutUint32(buf[10:], d.RowIndex)
	binary.LittleEndian.PutUint32(buf[14:], d.Rows)
	colOff := TCINFOFixedSize
	for _, c := range cols {
		binary.LittleEndian.PutUint16(buf[colOff:], c.PropType)
		binary.LittleEndian.PutUint16(buf[colOff+2:], c.PropID)
		binary.LittleEndian.PutUint16(buf[colOff+4:], c.Offset)
		buf[colOff+6] = c.Size
		buf[colOff+7] = c.Bit
		colOff += TCOLDESCSize
	}
	return buf, nil
}

// InspectTable validates TCINFO including the inline rgTCOLDESC array.
// Descriptors MUST be sorted by 32-bit property tag (MS-PST 2.3.4.1).
// ibData packing is checked by size group, not by descriptor order.
func InspectTable(raw []byte) (*TableView, error) {
	if len(raw) < TCINFOFixedSize {
		return nil, invariant(SectionTCINFO, "size", "TCINFO is %d bytes, need at least %d", len(raw), TCINFOFixedSize)
	}
	if raw[0] != HeapSigTC {
		return nil, invariant(SectionTCINFO, "bType", "got 0x%02x want 0x%02x (TC client signature)", raw[0], HeapSigTC)
	}
	n := int(raw[1])
	need := TCINFOFixedSize + TCOLDESCSize*n
	if len(raw) < need {
		return nil, invariant(SectionTCINFO, "rgTCOLDESC", "header claims %d columns (%d bytes) but buffer is %d; rgTCOLDESC is inline per MS-PST %s", n, need, len(raw), SectionTCOLDESC)
	}
	var rgib [4]uint16
	for i := 0; i < 4; i++ {
		rgib[i] = binary.LittleEndian.Uint16(raw[2+i*2:])
	}
	if rgib[0] > rgib[1] || rgib[1] > rgib[2] || rgib[2] > rgib[3] {
		return nil, invariant(SectionTCINFO, "rgib", "offsets not non-decreasing: %v (MS-PST %s)", rgib, SectionRowMatrix)
	}
	cols := make([]ColumnView, n)
	off := TCINFOFixedSize
	seenBit := make(map[byte]int)
	for i := 0; i < n; i++ {
		c := ColumnView{
			PropType: binary.LittleEndian.Uint16(raw[off:]),
			PropID:   binary.LittleEndian.Uint16(raw[off+2:]),
			Offset:   binary.LittleEndian.Uint16(raw[off+4:]),
			Size:     raw[off+6],
			Bit:      raw[off+7],
		}
		if prev, ok := seenBit[c.Bit]; ok {
			return nil, invariant(SectionTCOLDESC, "iBit", "column %d reuses iBit %d of column %d", i, c.Bit, prev)
		}
		seenBit[c.Bit] = i
		if n > 0 && int(c.Bit) >= n {
			return nil, invariant(SectionTCOLDESC, "iBit", "column %d iBit %d >= cCols %d", i, c.Bit, n)
		}
		cols[i] = c
		off += TCOLDESCSize
	}
	if err := validateColumnLayout(cols, rgib); err != nil {
		return nil, err
	}
	return &TableView{
		Signature: raw[0],
		NumCols:   raw[1],
		RgIB:      rgib,
		RowIndex:  binary.LittleEndian.Uint32(raw[10:14]),
		RowMatrix: binary.LittleEndian.Uint32(raw[14:18]),
		HidIndex:  binary.LittleEndian.Uint32(raw[18:22]),
		Columns:   cols,
		Raw:       append([]byte(nil), raw[:need]...),
	}, nil
}

func validateColumnLayout(cols []ColumnView, rgib [4]uint16) error {
	for i, c := range cols {
		if _, ok := columnGroup(c.Size); !ok {
			return invariant(SectionTCOLDESC, "cbData", "column %d cbData %d is not 1, 2, 4, or 8", i, c.Size)
		}
	}
	for i := 1; i < len(cols); i++ {
		if cols[i].Tag() <= cols[i-1].Tag() {
			return invariant(SectionTCINFO, "tag", "rgTCOLDESC not strictly sorted by 32-bit property tag at column %d (0x%08x then 0x%08x; MS-PST %s)", i, cols[i-1].Tag(), cols[i].Tag(), SectionTCINFO)
		}
	}
	if err := validateSpecialColumns(cols); err != nil {
		return err
	}
	return validateIbDataGroups(cols, rgib)
}

func validateSpecialColumns(cols []ColumnView) error {
	for i, c := range cols {
		switch {
		case isLtpRowID(c):
			if c.Size != 4 || c.Offset != 0 || c.Bit != 0 {
				return invariant(SectionTCRowID, "PidTagLtpRowId", "column %d PidTagLtpRowId must have iBit=0 ibData=0 cbData=4 (got iBit=%d ibData=%d cbData=%d)", i, c.Bit, c.Offset, c.Size)
			}
		case isLtpRowVer(c):
			if c.Size != 4 || c.Offset != 4 || c.Bit != 1 {
				return invariant(SectionTCRowID, "PidTagLtpRowVer", "column %d PidTagLtpRowVer must have iBit=1 ibData=4 cbData=4 (got iBit=%d ibData=%d cbData=%d)", i, c.Bit, c.Offset, c.Size)
			}
		default:
			if c.Bit == 0 && hasPropID(cols, PidTagLtpRowId) {
				return invariant(SectionTCRowID, "iBit", "column %d reuses PidTagLtpRowId iBit 0", i)
			}
			if c.Bit == 1 && hasPropID(cols, PidTagLtpRowVer) {
				return invariant(SectionTCRowID, "iBit", "column %d reuses PidTagLtpRowVer iBit 1", i)
			}
		}
	}
	return nil
}

func hasPropID(cols []ColumnView, id uint16) bool {
	for _, c := range cols {
		if c.PropID == id {
			return true
		}
	}
	return false
}

func packedOffsets(start uint16, width, n int) []uint16 {
	out := make([]uint16, n)
	off := start
	for i := 0; i < n; i++ {
		out[i] = off
		off += uint16(width)
	}
	return out
}

func sortedOffsets(cols []ColumnView) []uint16 {
	out := make([]uint16, len(cols))
	for i, c := range cols {
		out[i] = c.Offset
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func sameOffsets(got, want []uint16) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func validateIbDataGroups(cols []ColumnView, rgib [4]uint16) error {
	var eights, fours, twos, ones []ColumnView
	hasID, hasVer := false, false
	for _, c := range cols {
		switch {
		case isLtpRowID(c):
			hasID = true
		case isLtpRowVer(c):
			hasVer = true
		default:
			switch c.Size {
			case 8:
				eights = append(eights, c)
			case 4:
				fours = append(fours, c)
			case 2:
				twos = append(twos, c)
			case 1:
				ones = append(ones, c)
			}
		}
	}
	start := uint16(0)
	if hasID || hasVer {
		start = 8
	}
	want8 := packedOffsets(start, 8, len(eights))
	if !sameOffsets(sortedOffsets(eights), want8) {
		return invariant(SectionTCOLDESC, "ibData", "8-byte ibData %v want packed %v (MS-PST %s)", sortedOffsets(eights), want8, SectionRowMatrix)
	}
	want4 := packedOffsets(start+uint16(8*len(eights)), 4, len(fours))
	if !sameOffsets(sortedOffsets(fours), want4) {
		return invariant(SectionTCOLDESC, "ibData", "4-byte ibData %v want packed %v (MS-PST %s)", sortedOffsets(fours), want4, SectionRowMatrix)
	}
	want4b := start + uint16(8*len(eights)+4*len(fours))
	want2 := packedOffsets(want4b, 2, len(twos))
	if !sameOffsets(sortedOffsets(twos), want2) {
		return invariant(SectionTCOLDESC, "ibData", "2-byte ibData %v want packed %v (MS-PST %s)", sortedOffsets(twos), want2, SectionRowMatrix)
	}
	want2b := want4b + uint16(2*len(twos))
	want1 := packedOffsets(want2b, 1, len(ones))
	if !sameOffsets(sortedOffsets(ones), want1) {
		return invariant(SectionTCOLDESC, "ibData", "1-byte ibData %v want packed %v (MS-PST %s)", sortedOffsets(ones), want1, SectionRowMatrix)
	}
	want1b := want2b + uint16(len(ones))
	ceb := uint16(0)
	if n := len(cols); n > 0 {
		ceb = uint16((n + 7) / 8)
	}
	wantBM := want1b + ceb
	if rgib[0] != want4b || rgib[1] != want2b || rgib[2] != want1b || rgib[3] != wantBM {
		return invariant(SectionTCINFO, "rgib", "rgib=%v want TCI_4b=%d TCI_2b=%d TCI_1b=%d TCI_bm=%d (MS-PST %s)", rgib, want4b, want2b, want1b, wantBM, SectionRowMatrix)
	}
	return nil
}
