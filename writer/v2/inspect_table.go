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

// TableDraft is the input to EncodeTCINFO.
type TableDraft struct {
	Columns  []ColumnView
	RowIndex uint32
	Rows     uint32
}

// EncodeTCINFO writes bType, rgib, and rgTCOLDESC in one allocation.
func EncodeTCINFO(d TableDraft) []byte {
	n := len(d.Columns)
	buf := make([]byte, TCINFOFixedSize+TCOLDESCSize*n)
	buf[0] = HeapSigTC
	buf[1] = byte(n)
	// rgib: group 4-byte, 2-byte, 1-byte, then CEB. Minimal encoder: all
	// columns treated as occupying their cbData sequentially, CEB last.
	var off uint16
	for _, c := range d.Columns {
		off += uint16(c.Size)
	}
	ceb := uint16((n + 7) / 8)
	if n == 0 {
		ceb = 0
	}
	// TCI_4b = TCI_2b = TCI_1b = data end; TCI_bm = data+CEB.
	binary.LittleEndian.PutUint16(buf[2:], off)
	binary.LittleEndian.PutUint16(buf[4:], off)
	binary.LittleEndian.PutUint16(buf[6:], off)
	binary.LittleEndian.PutUint16(buf[8:], off+ceb)
	binary.LittleEndian.PutUint32(buf[10:], d.RowIndex)
	binary.LittleEndian.PutUint32(buf[14:], d.Rows)
	// hidIndex unused, remains 0 at [18:22]
	colOff := TCINFOFixedSize
	for i, c := range d.Columns {
		binary.LittleEndian.PutUint16(buf[colOff:], c.PropType)
		binary.LittleEndian.PutUint16(buf[colOff+2:], c.PropID)
		binary.LittleEndian.PutUint16(buf[colOff+4:], c.Offset)
		buf[colOff+6] = c.Size
		buf[colOff+7] = c.Bit
		if c.Bit == 0 && c.PropID != 0 {
			buf[colOff+7] = byte(i)
		}
		colOff += TCOLDESCSize
	}
	return buf
}

// InspectTable validates TCINFO including the inline rgTCOLDESC array.
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
		if int(c.Bit) >= n {
			return nil, invariant(SectionTCOLDESC, "iBit", "column %d iBit %d >= cCols %d", i, c.Bit, n)
		}
		cols[i] = c
		off += TCOLDESCSize
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
