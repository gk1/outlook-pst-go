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
// The encoder always includes PidTagLtpRowId / PidTagLtpRowVer
// (complete tags 0x67F20003 / 0x67F30003) at ibData 0/4 and iBit 0/1.
// Remaining ibData is assigned from the 8/4, 2, then 1-byte row-data
// groups starting at offset 8. rgTCOLDESC is then serialized sorted by
// the 32-bit property tag (MS-PST 2.3.4.1).
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

func isLtpRowID(c ColumnView) bool  { return c.Tag() == TagLtpRowId }
func isLtpRowVer(c ColumnView) bool { return c.Tag() == TagLtpRowVer }

func isWrongTypeLtp(c ColumnView) bool {
	return (c.PropID == PidTagLtpRowId || c.PropID == PidTagLtpRowVer) && c.PropType != PtypInteger32
}

func expectedLtpTag(id uint16) uint32 {
	return uint32(PtypInteger32) | uint32(id)<<16
}

func ltpRowIDColumn() ColumnView {
	return ColumnView{PropType: PtypInteger32, PropID: PidTagLtpRowId, Size: 4}
}

func ltpRowVerColumn() ColumnView {
	return ColumnView{PropType: PtypInteger32, PropID: PidTagLtpRowVer, Size: 4}
}

func ensureSpecialPair(cols []ColumnView) ([]ColumnView, error) {
	out := append([]ColumnView(nil), cols...)
	var hasID, hasVer bool
	for _, c := range out {
		if isWrongTypeLtp(c) {
			return nil, invalidArg("tag", "PidTag 0x%04x must be PtypInteger32 (complete tag 0x%08x), got wPropType 0x%04x (MS-PST %s)", c.PropID, expectedLtpTag(c.PropID), c.PropType, SectionTCRowID)
		}
		if isLtpRowID(c) {
			hasID = true
		}
		if isLtpRowVer(c) {
			hasVer = true
		}
	}
	if !hasID {
		out = append(out, ltpRowIDColumn())
	}
	if !hasVer {
		out = append(out, ltpRowVerColumn())
	}
	return out, nil
}

// assignColumnLayout copies cols, assigns ibData / iBit from row-data groups,
// and returns the slice still in assignment order (not tag order).
// PidTagLtpRowId / PidTagLtpRowVer occupy iBit 0/1 and ibData 0/4; remaining
// 8/4-byte data starts at offset 8. CEB size is (cCols+7)/8.
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
	if rowID < 0 {
		return nil, [4]uint16{}, invalidArg("PidTagLtpRowId", "missing required PidTagLtpRowId 0x%08x (MS-PST %s)", TagLtpRowId, SectionTCRowID)
	}
	if rowVer < 0 {
		return nil, [4]uint16{}, invalidArg("PidTagLtpRowVer", "missing required PidTagLtpRowVer 0x%08x (MS-PST %s)", TagLtpRowVer, SectionTCRowID)
	}

	out[rowID].Offset = 0
	out[rowID].Bit = 0
	out[rowVer].Offset = 4
	out[rowVer].Bit = 1

	nextBit := byte(2)
	off := uint16(8)
	for _, i := range groups[0] { // 8-byte
		out[i].Offset = off
		out[i].Bit = nextBit
		nextBit++
		off += 8
	}
	for _, i := range groups[1] { // remaining 4-byte
		out[i].Offset = off
		out[i].Bit = nextBit
		nextBit++
		off += 4
	}
	var rgib [4]uint16
	rgib[0] = off
	for _, i := range groups[2] { // 2-byte
		out[i].Offset = off
		out[i].Bit = nextBit
		nextBit++
		off += 2
	}
	rgib[1] = off
	for _, i := range groups[3] { // 1-byte
		out[i].Offset = off
		out[i].Bit = nextBit
		nextBit++
		off += 1
	}
	rgib[2] = off
	ceb := uint16((len(out) + 7) / 8)
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
// by 32-bit property tag. Every encoded TC includes the MS-PST 2.3.4.4.1
// special pair. ibData follows the 8/4, 2, 1-byte row groups after that pair.
func EncodeTCINFO(d TableDraft) ([]byte, error) {
	cols, err := ensureSpecialPair(d.Columns)
	if err != nil {
		return nil, err
	}
	if len(cols) > MaxTCColumns {
		return nil, invalidArg("cCols", "%d columns after required PidTagLtpRowId/PidTagLtpRowVer exceeds byte cCols %d (MS-PST %s)", len(cols), MaxTCColumns, SectionTCINFO)
	}
	cols, rgib, err := assignColumnLayout(cols)
	if err != nil {
		return nil, err
	}
	return writeTCINFO(cols, rgib, d.RowIndex, d.Rows)
}

// writeTCINFO serializes an already-laid-out column list. Offset/iBit/rgib
// are preserved so Load+Commit cannot reshuffle ibData by tag order.
func writeTCINFO(cols []ColumnView, rgib [4]uint16, rowIndex, rows uint32) ([]byte, error) {
	cols = append([]ColumnView(nil), cols...)
	if err := sortColumnsByTag(cols); err != nil {
		return nil, err
	}
	n := len(cols)
	if n > MaxTCColumns {
		return nil, invalidArg("cCols", "%d columns exceeds byte cCols %d (MS-PST %s)", n, MaxTCColumns, SectionTCINFO)
	}
	buf := make([]byte, TCINFOFixedSize+TCOLDESCSize*n)
	buf[0] = HeapSigTC
	buf[1] = byte(n)
	for i := 0; i < 4; i++ {
		binary.LittleEndian.PutUint16(buf[2+i*2:], rgib[i])
	}
	binary.LittleEndian.PutUint32(buf[10:], rowIndex)
	binary.LittleEndian.PutUint32(buf[14:], rows)
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
// Every TC MUST contain PidTagLtpRowId 0x67F20003 and PidTagLtpRowVer
// 0x67F30003 at the reserved iBit/ibData slots (MS-PST 2.3.4.4.1).
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
	hidIndex := binary.LittleEndian.Uint32(raw[18:22])
	if hidIndex != 0 {
		return nil, invariant(SectionTCINFO, "hidIndex", "deprecated hidIndex must be 0 (MS-PST %s)", SectionTCINFO)
	}
	return &TableView{
		Signature: raw[0],
		NumCols:   raw[1],
		RgIB:      rgib,
		RowIndex:  binary.LittleEndian.Uint32(raw[10:14]),
		RowMatrix: binary.LittleEndian.Uint32(raw[14:18]),
		HidIndex:  hidIndex,
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
	var hasID, hasVer bool
	for i, c := range cols {
		if isWrongTypeLtp(c) {
			return invariant(SectionTCRowID, "tag", "column %d PidTag 0x%04x must be PtypInteger32 (complete tag 0x%08x), got wPropType 0x%04x", i, c.PropID, expectedLtpTag(c.PropID), c.PropType)
		}
		if isLtpRowID(c) {
			hasID = true
		}
		if isLtpRowVer(c) {
			hasVer = true
		}
	}
	if !hasID {
		return invariant(SectionTCRowID, "PidTagLtpRowId", "missing required PidTagLtpRowId 0x%08x (MS-PST %s)", TagLtpRowId, SectionTCRowID)
	}
	if !hasVer {
		return invariant(SectionTCRowID, "PidTagLtpRowVer", "missing required PidTagLtpRowVer 0x%08x (MS-PST %s)", TagLtpRowVer, SectionTCRowID)
	}
	for i, c := range cols {
		switch {
		case isLtpRowID(c):
			if c.Size != 4 || c.Offset != 0 || c.Bit != 0 {
				return invariant(SectionTCRowID, "PidTagLtpRowId", "column %d PidTagLtpRowId 0x%08x must have iBit=0 ibData=0 cbData=4 (got iBit=%d ibData=%d cbData=%d)", i, TagLtpRowId, c.Bit, c.Offset, c.Size)
			}
		case isLtpRowVer(c):
			if c.Size != 4 || c.Offset != 4 || c.Bit != 1 {
				return invariant(SectionTCRowID, "PidTagLtpRowVer", "column %d PidTagLtpRowVer 0x%08x must have iBit=1 ibData=4 cbData=4 (got iBit=%d ibData=%d cbData=%d)", i, TagLtpRowVer, c.Bit, c.Offset, c.Size)
			}
		default:
			if c.Bit == 0 || c.Bit == 1 {
				return invariant(SectionTCRowID, "iBit", "column %d ordinary iBit %d reuses reserved PidTagLtpRowId/PidTagLtpRowVer slot (MS-PST %s)", i, c.Bit, SectionTCRowID)
			}
			if c.Offset < 8 {
				return invariant(SectionTCRowID, "ibData", "column %d ordinary ibData %d overlaps reserved bytes 0-7 (MS-PST %s)", i, c.Offset, SectionTCRowID)
			}
		}
	}
	return nil
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
	for _, c := range cols {
		if isLtpRowID(c) || isLtpRowVer(c) {
			continue
		}
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
	const start uint16 = 8
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
	ceb := uint16((len(cols) + 7) / 8)
	wantBM := want1b + ceb
	if rgib[0] != want4b || rgib[1] != want2b || rgib[2] != want1b || rgib[3] != wantBM {
		return invariant(SectionTCINFO, "rgib", "rgib=%v want TCI_4b=%d TCI_2b=%d TCI_1b=%d TCI_bm=%d (MS-PST %s)", rgib, want4b, want2b, want1b, wantBM, SectionRowMatrix)
	}
	return nil
}

// CEB bits are packed LSB-first in each byte: iBit 0 is 1<<0 of rgbCEB[0].
// See MS-PST 2.3.4.4.
func cebHas(ceb []byte, iBit byte) bool {
	i := int(iBit)
	if i/8 >= len(ceb) {
		return false
	}
	return ceb[i/8]&(1<<uint(i%8)) != 0
}

func cebSet(ceb []byte, iBit byte) {
	i := int(iBit)
	if i/8 >= len(ceb) {
		return
	}
	ceb[i/8] |= 1 << uint(i%8)
}

func tableRowSize(rgib [4]uint16) int { return int(rgib[3]) }

func tableRowsPerBlock(rowSize int) int {
	if rowSize <= 0 {
		return 0
	}
	return MaxDataBlockCB / rowSize
}

func tableRowOffset(idx, rowSize int, blocked bool) int {
	if !blocked {
		return idx * rowSize
	}
	rpb := tableRowsPerBlock(rowSize)
	if rpb < 1 {
		return idx * rowSize
	}
	return (idx/rpb)*MaxDataBlockCB + (idx%rpb)*rowSize
}

// InspectTableRows validates Row Matrix boundaries against the Row Index BTH.
// HID matrices are tightly packed. Subnode matrices pad to MaxDataBlockCB so
// a row never spans a data-tree leaf (MS-PST 2.3.4.4).
func InspectTableRows(tv *TableView, ents []bthKV, matrix []byte, blocked bool) error {
	if tv == nil {
		return invalidArg("tc", "nil TableView")
	}
	rowSize := tableRowSize(tv.RgIB)
	if rowSize <= 0 {
		return invariant(SectionRowMatrix, "TCI_bm", "row size %d", rowSize)
	}
	if tableRowsPerBlock(rowSize) < 1 {
		return invariant(SectionRowMatrix, "cbRow", "row size %d exceeds data block %d (MS-PST %s)", rowSize, MaxDataBlockCB, SectionRowMatrix)
	}
	if tv.RowMatrix == 0 {
		if len(ents) != 0 || len(matrix) != 0 {
			return invariant(SectionTCINFO, "hnidRows", "hnidRows is 0 but matrix/index is not empty")
		}
		return nil
	}
	if blocked && IsHID(tv.RowMatrix) {
		return invariant(SectionTCINFO, "hnidRows", "blocked Row Matrix HID 0x%x, want NID_TYPE_LTP", tv.RowMatrix)
	}
	if !blocked && !IsHID(tv.RowMatrix) {
		return invariant(SectionTCINFO, "hnidRows", "tight Row Matrix HNID 0x%x, want HID", tv.RowMatrix)
	}
	seenIdx := make(map[uint32]uint32, len(ents))
	for _, e := range ents {
		if len(e.key) != 4 || len(e.val) != 4 {
			return invariant(SectionTCRowID, "TCROWID", "BTH entry key %d val %d, want 4/4", len(e.key), len(e.val))
		}
		id := binary.LittleEndian.Uint32(e.key)
		idx := binary.LittleEndian.Uint32(e.val)
		if _, dup := seenIdx[idx]; dup {
			return invariant(SectionTCRowID, "dwRowIndex", "duplicate RowIndex %d", idx)
		}
		seenIdx[idx] = id
		off := tableRowOffset(int(idx), rowSize, blocked)
		if off < 0 || off+rowSize > len(matrix) {
			return invariant(SectionRowMatrix, "row", "row %d (id 0x%x) offset %d+%d exceeds matrix %d", idx, id, off, rowSize, len(matrix))
		}
		row := matrix[off : off+rowSize]
		gotID := binary.LittleEndian.Uint32(row[0:4])
		if gotID != id {
			return invariant(SectionTCRowID, "PidTagLtpRowId", "row %d cell 0x%x BTH key 0x%x", idx, gotID, id)
		}
		ceb := row[tv.RgIB[2]:tv.RgIB[3]]
		if !cebHas(ceb, 0) || !cebHas(ceb, 1) {
			return invariant(SectionTCRowID, "rgbCEB", "row 0x%x missing required PidTagLtpRowId/PidTagLtpRowVer bits", id)
		}
		for _, c := range tv.Columns {
			end := int(c.Offset) + int(c.Size)
			if int(c.Offset) < 0 || end > rowSize {
				return invariant(SectionTCOLDESC, "ibData", "column 0x%04x ibData %d+%d exceeds row %d", c.PropID, c.Offset, c.Size, rowSize)
			}
		}
	}
	if !blocked {
		want := len(ents) * rowSize
		if len(matrix) != want {
			return invariant(SectionRowMatrix, "cb", "HID matrix %d bytes, want %d for %d rows", len(matrix), want, len(ents))
		}
	}
	return nil
}
