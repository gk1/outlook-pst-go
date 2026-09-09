package writer

import "encoding/binary"

// ColumnView is one TCOLDESC. See MS-PST 2.3.4.2.
type ColumnView struct {
	PropType uint16
	PropID   uint16
	Offset   uint16
	Size     byte
	Bit      byte
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

// TableDraft is the input to EncodeTCINFO. Columns may be in any order;
// the encoder groups them 8-byte, then 4-byte, then 2-byte, then 1-byte
// and assigns ibData / iBit. Invalid sizes are rejected.
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

func groupColumns(cols []ColumnView) ([]ColumnView, [4]uint16, error) {
	var groups [4][]ColumnView
	for i, c := range cols {
		g, ok := columnGroup(c.Size)
		if !ok {
			return nil, [4]uint16{}, invalidArg("cbData", "column %d has invalid cbData %d (MS-PST %s allows 1,2,4,8)", i, c.Size, SectionTCOLDESC)
		}
		groups[g] = append(groups[g], c)
	}
	out := make([]ColumnView, 0, len(cols))
	var off uint16
	var rgib [4]uint16
	appendGroup := func(g int) {
		for _, c := range groups[g] {
			c.Offset = off
			c.Bit = byte(len(out))
			off += uint16(c.Size)
			out = append(out, c)
		}
	}
	appendGroup(0) // 8-byte
	appendGroup(1) // 4-byte
	rgib[0] = off  // TCI_4b
	appendGroup(2) // 2-byte
	rgib[1] = off  // TCI_2b
	appendGroup(3) // 1-byte
	rgib[2] = off  // TCI_1b
	ceb := uint16(0)
	if n := len(out); n > 0 {
		ceb = uint16((n + 7) / 8)
	}
	rgib[3] = off + ceb
	return out, rgib, nil
}

// EncodeTCINFO writes bType, spec-correct rgib, and inline rgTCOLDESC.
func EncodeTCINFO(d TableDraft) ([]byte, error) {
	cols, rgib, err := groupColumns(d.Columns)
	if err != nil {
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

// InspectTable validates TCINFO including the inline rgTCOLDESC array,
// descriptor grouping (8,4,2,1), ibData packing, and rgib geometry.
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
	var lastGroup = -1
	for i, c := range cols {
		g, ok := columnGroup(c.Size)
		if !ok {
			return invariant(SectionTCOLDESC, "cbData", "column %d cbData %d is not 1, 2, 4, or 8", i, c.Size)
		}
		if g < lastGroup {
			return invariant(SectionTCOLDESC, "order", "column %d size %d is out of 8/4/2/1 group order (MS-PST %s)", i, c.Size, SectionRowMatrix)
		}
		lastGroup = g
	}
	var packed uint16
	var sum8, sum4, sum2, sum1 uint16
	for i, c := range cols {
		g, _ := columnGroup(c.Size)
		if c.Offset != packed {
			return invariant(SectionTCOLDESC, "ibData", "column %d ibData %d, packed offset %d", i, c.Offset, packed)
		}
		packed += uint16(c.Size)
		switch g {
		case 0:
			sum8 += uint16(c.Size)
		case 1:
			sum4 += uint16(c.Size)
		case 2:
			sum2 += uint16(c.Size)
		case 3:
			sum1 += uint16(c.Size)
		}
		switch g {
		case 0, 1:
			if c.Offset >= rgib[0] && rgib[0] != packed && c.Offset+uint16(c.Size) > rgib[0] {
				return invariant(SectionTCINFO, "rgib", "column %d (size %d) extends past TCI_4b=%d", i, c.Size, rgib[0])
			}
		case 2:
			if c.Offset < rgib[0] || c.Offset >= rgib[1] {
				return invariant(SectionTCINFO, "rgib", "2-byte column %d ibData %d not in [%d,%d)", i, c.Offset, rgib[0], rgib[1])
			}
		case 3:
			if c.Offset < rgib[1] || c.Offset >= rgib[2] {
				return invariant(SectionTCINFO, "rgib", "1-byte column %d ibData %d not in [%d,%d)", i, c.Offset, rgib[1], rgib[2])
			}
		}
	}
	want4 := sum8 + sum4
	want2 := want4 + sum2
	want1 := want2 + sum1
	ceb := uint16(0)
	if n := len(cols); n > 0 {
		ceb = uint16((n + 7) / 8)
	}
	wantBM := want1 + ceb
	if rgib[0] != want4 || rgib[1] != want2 || rgib[2] != want1 || rgib[3] != wantBM {
		return invariant(SectionTCINFO, "rgib", "rgib=%v want TCI_4b=%d TCI_2b=%d TCI_1b=%d TCI_bm=%d (MS-PST %s)", rgib, want4, want2, want1, wantBM, SectionRowMatrix)
	}
	return nil
}
