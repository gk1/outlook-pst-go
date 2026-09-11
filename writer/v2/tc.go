package writer

import (
	"encoding/binary"
)

// TC is a Table Context builder over HN. See MS-PST 2.3.4.
// Rows are held in memory and rewritten on Commit so add/update/delete stay
// coherent with the Row Index BTH, CEB, and variable HNIDs.
type TC struct {
	n      *NDB
	heap   *Heap
	draft  []ColumnView
	cols   []ColumnView
	byID   map[uint16]ColumnView
	rgib   [4]uint16
	rows   []tcRow
	byRow  map[uint32]int
	nextID uint32
}

type tcRow struct {
	id    uint32
	ver   uint32
	cells map[uint16][]byte
}

func tcColSize(typ uint16) byte {
	switch typ {
	case PtypBoolean:
		return 1
	case PtypInteger16:
		return 2
	case PtypInteger32, PtypFloating32, PtypError:
		return 4
	case PtypInteger64, PtypFloating64, PtypCurrency, PtypTime:
		return 8
	default:
		return 4
	}
}

func tcFixedCell(typ uint16) bool {
	switch typ {
	case PtypBoolean, PtypInteger16, PtypInteger32, PtypFloating32, PtypError,
		PtypInteger64, PtypFloating64, PtypCurrency, PtypTime:
		return true
	default:
		return false
	}
}

// NewTC starts an empty table. PidTagLtpRowId / PidTagLtpRowVer are injected.
func NewTC(n *NDB, cols []ColumnView) (*TC, error) {
	if n == nil {
		return nil, invalidArg("ndb", "nil NDB")
	}
	draft := make([]ColumnView, len(cols))
	for i, c := range cols {
		if c.Size == 0 {
			c.Size = tcColSize(c.PropType)
		}
		draft[i] = ColumnView{PropType: c.PropType, PropID: c.PropID, Size: c.Size}
	}
	with, err := ensureSpecialPair(draft)
	if err != nil {
		return nil, err
	}
	if len(with) > MaxTCColumns {
		return nil, invalidArg("cCols", "%d columns exceeds byte cCols %d (MS-PST %s)", len(with), MaxTCColumns, SectionTCINFO)
	}
	layout, rgib, err := assignColumnLayout(with)
	if err != nil {
		return nil, err
	}
	byID := make(map[uint16]ColumnView, len(layout))
	for _, c := range layout {
		if _, ok := byID[c.PropID]; ok {
			return nil, invalidArg("tag", "duplicate PropID 0x%04x", c.PropID)
		}
		byID[c.PropID] = c
	}
	return &TC{
		n:      n,
		heap:   NewHeap(n, HeapSigTC),
		draft:  draft,
		cols:   layout,
		byID:   byID,
		rgib:   rgib,
		byRow:  make(map[uint32]int),
		nextID: 1,
	}, nil
}

func (t *TC) Columns() []ColumnView {
	if t == nil {
		return nil
	}
	return append([]ColumnView(nil), t.cols...)
}

func (t *TC) Add() (uint32, error) {
	if t == nil {
		return 0, invalidArg("tc", "nil TC")
	}
	id := t.nextID
	if err := t.AddID(id); err != nil {
		return 0, err
	}
	return id, nil
}

// AddID inserts a row whose dwRowID is id. Hierarchy tables use the
// child folder NID as dwRowID (MS-PST 2.3.4.3 / 2.4.4.4).
func (t *TC) AddID(id uint32) error {
	if t == nil {
		return invalidArg("tc", "nil TC")
	}
	if id == 0 {
		return invalidArg("dwRowID", "row id 0")
	}
	if _, ok := t.byRow[id]; ok {
		return invalidArg("dwRowID", "duplicate row 0x%x", id)
	}
	t.byRow[id] = len(t.rows)
	t.rows = append(t.rows, tcRow{id: id, ver: 1, cells: make(map[uint16][]byte)})
	if id >= t.nextID {
		t.nextID = id + 1
	}
	return nil
}

func (t *TC) row(rowID uint32) (*tcRow, error) {
	if t == nil {
		return nil, invalidArg("tc", "nil TC")
	}
	i, ok := t.byRow[rowID]
	if !ok {
		return nil, invalidArg("dwRowID", "row 0x%x not found", rowID)
	}
	return &t.rows[i], nil
}

func (t *TC) Set(rowID uint32, id uint16, data []byte) error {
	r, err := t.row(rowID)
	if err != nil {
		return err
	}
	col, ok := t.byID[id]
	if !ok {
		return invalidArg("propID", "column 0x%04x is not in this TC", id)
	}
	if isLtpRowID(col) || isLtpRowVer(col) {
		return invalidArg("propID", "PidTag 0x%04x is maintained by the TC (MS-PST %s)", id, SectionTCRowID)
	}
	r.cells[id] = append([]byte(nil), data...)
	r.ver++
	return nil
}

func (t *TC) Clear(rowID uint32, id uint16) error {
	r, err := t.row(rowID)
	if err != nil {
		return err
	}
	col, ok := t.byID[id]
	if !ok {
		return invalidArg("propID", "column 0x%04x is not in this TC", id)
	}
	if isLtpRowID(col) || isLtpRowVer(col) {
		return invalidArg("propID", "cannot clear PidTag 0x%04x (MS-PST %s)", id, SectionTCRowID)
	}
	delete(r.cells, id)
	r.ver++
	return nil
}

func (t *TC) Delete(rowID uint32) error {
	if t == nil {
		return invalidArg("tc", "nil TC")
	}
	i, ok := t.byRow[rowID]
	if !ok {
		return invalidArg("dwRowID", "row 0x%x not found", rowID)
	}
	t.rows = append(t.rows[:i], t.rows[i+1:]...)
	delete(t.byRow, rowID)
	for j := i; j < len(t.rows); j++ {
		t.byRow[t.rows[j].id] = j
	}
	return nil
}

func (t *TC) SetString(rowID uint32, id uint16, s string) error {
	return t.Set(rowID, id, encodeUnicodePC(s))
}

func (t *TC) SetInt32(rowID uint32, id uint16, v int32) error {
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, uint32(v))
	return t.Set(rowID, id, buf)
}

func (t *TC) SetInt64(rowID uint32, id uint16, v int64) error {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, uint64(v))
	return t.Set(rowID, id, buf)
}

func (t *TC) SetBool(rowID uint32, id uint16, v bool) error {
	buf := []byte{0}
	if v {
		buf[0] = 1
	}
	return t.Set(rowID, id, buf)
}

func (t *TC) SetObject(rowID uint32, id uint16, data []byte) error {
	return t.Set(rowID, id, data)
}

func (t *TC) SetBinary(rowID uint32, id uint16, b []byte) error {
	return t.Set(rowID, id, b)
}

func (t *TC) SetTime(rowID uint32, id uint16, filetime uint64) error {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, filetime)
	return t.Set(rowID, id, buf)
}

func (t *TC) place(typ uint16, data []byte) (uint32, error) {
	if typ == PtypObject {
		return t.heap.newSub(data), nil
	}
	return t.heap.Allocate(data)
}

func (t *TC) encodeRow(r tcRow) ([]byte, error) {
	rowSize := tableRowSize(t.rgib)
	row := make([]byte, rowSize)
	ceb := row[t.rgib[2]:t.rgib[3]]
	for _, col := range t.cols {
		var data []byte
		switch {
		case isLtpRowID(col):
			buf := make([]byte, 4)
			binary.LittleEndian.PutUint32(buf, r.id)
			data = buf
		case isLtpRowVer(col):
			buf := make([]byte, 4)
			binary.LittleEndian.PutUint32(buf, r.ver)
			data = buf
		default:
			var ok bool
			data, ok = r.cells[col.PropID]
			if !ok {
				continue
			}
		}
		cebSet(ceb, col.Bit)
		if !tcFixedCell(col.PropType) {
			hnid, err := t.place(col.PropType, data)
			if err != nil {
				return nil, err
			}
			binary.LittleEndian.PutUint32(row[col.Offset:], hnid)
			continue
		}
		n := int(col.Size)
		if len(data) > n {
			data = data[:n]
		}
		copy(row[col.Offset:], data)
	}
	return row, nil
}

func packRowMatrix(rows [][]byte, rowSize int) []byte {
	if len(rows) == 0 {
		return nil
	}
	buf := make([]byte, len(rows)*rowSize)
	for i, r := range rows {
		copy(buf[i*rowSize:], r)
	}
	return buf
}

func (t *TC) Commit(nid uint32) error {
	if t == nil || t.n == nil {
		return invalidArg("tc", "nil TC")
	}
	if nid == 0 || NIDTypeOf(nid) == NIDTypeHID {
		return invalidArg("nid", "TC node 0x%x must not be a HID", nid)
	}
	if err := t.build(); err != nil {
		return err
	}
	return t.heap.Commit(nid)
}

// AttachTo writes this table as a subnode of parent (recipient tables).
func (t *TC) AttachTo(parent *Heap, nid uint32) error {
	if t == nil || t.n == nil {
		return invalidArg("tc", "nil TC")
	}
	if parent == nil {
		return invalidArg("heap", "nil parent heap")
	}
	if err := t.build(); err != nil {
		return err
	}
	return parent.AttachHeap(nid, t.heap)
}

func (t *TC) build() error {
	rowSize := tableRowSize(t.rgib)
	raws := make([][]byte, len(t.rows))
	for i, r := range t.rows {
		raw, err := t.encodeRow(r)
		if err != nil {
			return err
		}
		raws[i] = raw
	}
	tight := len(t.rows) * rowSize
	blocked := tight > HeapMaxAlloc
	var matrixHNID uint32
	if len(t.rows) > 0 {
		packed := packRowMatrix(raws, rowSize)
		if blocked {
			leafCB := tableRowsPerBlock(rowSize) * rowSize
			if leafCB < 1 {
				return invariant(SectionRowMatrix, "cbRow", "row size %d exceeds data block %d (MS-PST %s)", rowSize, MaxDataBlockCB, SectionRowMatrix)
			}
			matrixHNID = t.heap.newSubChunked(packed, leafCB)
		} else {
			var err error
			matrixHNID, err = t.heap.Allocate(packed)
			if err != nil {
				return err
			}
		}
		if blocked && IsHID(matrixHNID) {
			return invariant(SectionRowMatrix, "hnidRows", "blocked matrix stayed a HID 0x%x", matrixHNID)
		}
	}
	bth, err := NewBTH(t.heap, 4, 4)
	if err != nil {
		return err
	}
	key := make([]byte, 4)
	val := make([]byte, 4)
	for i, r := range t.rows {
		binary.LittleEndian.PutUint32(key, r.id)
		binary.LittleEndian.PutUint32(val, uint32(i))
		if err := bth.Insert(append([]byte(nil), key...), append([]byte(nil), val...)); err != nil {
			return err
		}
	}
	bthHID, err := bth.Build()
	if err != nil {
		return err
	}
	info, err := writeTCINFO(t.cols, t.rgib, bthHID, matrixHNID)
	if err != nil {
		return err
	}
	hid, err := t.heap.Allocate(info)
	if err != nil {
		return err
	}
	t.heap.SetRoot(hid)
	return nil
}

// TCView is a committed Table Context.
type TCView struct {
	heap    *HeapView
	info    *TableView
	bth     *BTHView
	matrix  []byte
	blocked bool
	nid     uint32
}

func OpenTC(n *NDB, nid uint32) (*TCView, error) {
	h, err := OpenHeap(n, nid)
	if err != nil {
		return nil, err
	}
	if h.ClientSig() != HeapSigTC {
		return nil, invariant(SectionTCINFO, "bClientSig", "got 0x%02x want 0x%02x", h.ClientSig(), HeapSigTC)
	}
	raw, err := h.Read(h.Root())
	if err != nil {
		return nil, err
	}
	tv, err := InspectTable(raw)
	if err != nil {
		return nil, err
	}
	bth, err := OpenBTH(h, tv.RowIndex)
	if err != nil {
		return nil, err
	}
	if bth.KeySize() != 4 || bth.ValSize() != 4 {
		return nil, invariant(SectionTCRowID, "BTHHEADER", "Row Index cbKey %d cbEnt %d want 4/4", bth.KeySize(), bth.ValSize())
	}
	var matrix []byte
	if tv.RowMatrix != 0 {
		matrix, err = h.Read(tv.RowMatrix)
		if err != nil {
			return nil, err
		}
	}
	blocked := tv.RowMatrix != 0 && !IsHID(tv.RowMatrix)
	ents, err := bth.Entries()
	if err != nil {
		return nil, err
	}
	if err := InspectTableRows(tv, ents, matrix, blocked); err != nil {
		return nil, err
	}
	v := &TCView{heap: h, info: tv, bth: bth, matrix: matrix, blocked: blocked, nid: nid}
	if blocked {
		if err := v.inspectMatrixLeaves(); err != nil {
			return nil, err
		}
	}
	return v, nil
}

func (v *TCView) inspectMatrixLeaves() error {
	rowSize := tableRowSize(v.info.RgIB)
	rpb := tableRowsPerBlock(rowSize)
	var found bool
	err := v.heap.n.WalkSubnodes(v.heap.subBID, func(e SLEntry) error {
		if uint32(e.NID) != v.info.RowMatrix {
			return nil
		}
		found = true
		var i int
		_, err := v.heap.n.walkDataTree(e.DataBID, func(_ uint64, cb uint16) error {
			if rowSize > 0 && int(cb)%rowSize != 0 {
				return invariant(SectionRowMatrix, "row", "leaf %d size %d splits a row of %d (MS-PST %s)", i, cb, rowSize, SectionRowMatrix)
			}
			n := int(cb) / rowSize
			if n > rpb {
				return invariant(SectionRowMatrix, "cb", "leaf %d holds %d rows, max %d (MS-PST %s)", i, n, rpb, SectionRowMatrix)
			}
			i++
			return nil
		})
		return err
	})
	if err != nil {
		return err
	}
	if !found {
		return invariant(SectionRowMatrix, "hnidRows", "missing Row Matrix subnode 0x%x", v.info.RowMatrix)
	}
	return nil
}

func (v *TCView) Info() *TableView { return v.info }
func (v *TCView) RowCount() int {
	if v == nil || v.bth == nil {
		return 0
	}
	ents, err := v.bth.Entries()
	if err != nil {
		return 0
	}
	return len(ents)
}

func (v *TCView) RowIDs() ([]uint32, error) {
	ents, err := v.bth.Entries()
	if err != nil {
		return nil, err
	}
	ids := make([]uint32, len(ents))
	for i, e := range ents {
		ids[i] = binary.LittleEndian.Uint32(e.key)
	}
	return ids, nil
}

func (v *TCView) rowBytes(rowID uint32) ([]byte, error) {
	key := make([]byte, 4)
	binary.LittleEndian.PutUint32(key, rowID)
	rec, err := v.bth.Lookup(key)
	if err != nil {
		return nil, err
	}
	idx := binary.LittleEndian.Uint32(rec)
	rowSize := tableRowSize(v.info.RgIB)
	off := tableRowOffset(int(idx), rowSize, v.blocked)
	if off+rowSize > len(v.matrix) {
		return nil, invariant(SectionRowMatrix, "row", "row 0x%x index %d out of matrix", rowID, idx)
	}
	return v.matrix[off : off+rowSize], nil
}

func (v *TCView) Exists(rowID uint32, id uint16) (bool, error) {
	row, err := v.rowBytes(rowID)
	if err != nil {
		return false, err
	}
	col, ok := v.column(id)
	if !ok {
		return false, invalidArg("propID", "column 0x%04x is not in this TC", id)
	}
	ceb := row[v.info.RgIB[2]:v.info.RgIB[3]]
	return cebHas(ceb, col.Bit), nil
}

func (v *TCView) column(id uint16) (ColumnView, bool) {
	for _, c := range v.info.Columns {
		if c.PropID == id {
			return c, true
		}
	}
	return ColumnView{}, false
}

func (v *TCView) Get(rowID uint32, id uint16) (typ uint16, data []byte, err error) {
	row, err := v.rowBytes(rowID)
	if err != nil {
		return 0, nil, err
	}
	col, ok := v.column(id)
	if !ok {
		return 0, nil, invalidArg("propID", "column 0x%04x is not in this TC", id)
	}
	ceb := row[v.info.RgIB[2]:v.info.RgIB[3]]
	if !cebHas(ceb, col.Bit) {
		return 0, nil, invalidArg("rgbCEB", "cell 0x%04x does not exist in row 0x%x", id, rowID)
	}
	cell := append([]byte(nil), row[col.Offset:int(col.Offset)+int(col.Size)]...)
	if tcFixedCell(col.PropType) {
		return col.PropType, cell, nil
	}
	hnid := binary.LittleEndian.Uint32(cell)
	if col.PropType == PtypObject {
		if IsHID(hnid) || NIDTypeOf(hnid) != NIDTypeLTP {
			return 0, nil, invariant(SectionTCOLDESC, "dwValueHnid", "PtypObject HNID 0x%x must be NID_TYPE_LTP (MS-PST %s)", hnid, SectionPC)
		}
	}
	data, err = v.heap.Read(hnid)
	return col.PropType, data, err
}

func (v *TCView) GetString(rowID uint32, id uint16) (string, error) {
	typ, data, err := v.Get(rowID, id)
	if err != nil {
		return "", err
	}
	if typ != PtypString {
		return "", invalidArg("wPropType", "0x%04x is not PtypString", typ)
	}
	return decodeUnicodePC(data), nil
}

func (v *TCView) GetInt32(rowID uint32, id uint16) (int32, error) {
	typ, data, err := v.Get(rowID, id)
	if err != nil {
		return 0, err
	}
	if typ != PtypInteger32 || len(data) < 4 {
		return 0, invalidArg("wPropType", "0x%04x is not PtypInteger32", typ)
	}
	return int32(binary.LittleEndian.Uint32(data)), nil
}

func (v *TCView) GetBool(rowID uint32, id uint16) (bool, error) {
	typ, data, err := v.Get(rowID, id)
	if err != nil {
		return false, err
	}
	if typ != PtypBoolean {
		return false, invalidArg("wPropType", "0x%04x is not PtypBoolean", typ)
	}
	for _, b := range data {
		if b != 0 {
			return true, nil
		}
	}
	return false, nil
}

func (v *TCView) GetBinary(rowID uint32, id uint16) ([]byte, error) {
	typ, data, err := v.Get(rowID, id)
	if err != nil {
		return nil, err
	}
	if typ != PtypBinary {
		return nil, invalidArg("wPropType", "0x%04x is not PtypBinary", typ)
	}
	return append([]byte(nil), data...), nil
}

func (v *TCView) GetTime(rowID uint32, id uint16) (uint64, error) {
	typ, data, err := v.Get(rowID, id)
	if err != nil {
		return 0, err
	}
	if typ != PtypTime || len(data) < 8 {
		return 0, invalidArg("wPropType", "0x%04x is not PtypTime", typ)
	}
	return binary.LittleEndian.Uint64(data), nil
}

func (v *TCView) Load() (*TC, error) {
	t := &TC{
		n:      v.heap.n,
		heap:   NewHeap(v.heap.n, HeapSigTC),
		draft:  append([]ColumnView(nil), v.info.Columns...),
		cols:   append([]ColumnView(nil), v.info.Columns...),
		byID:   make(map[uint16]ColumnView, len(v.info.Columns)),
		rgib:   v.info.RgIB,
		byRow:  make(map[uint32]int),
		nextID: 1,
	}
	for _, c := range t.cols {
		t.byID[c.PropID] = c
	}
	ids, err := v.RowIDs()
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		row, err := v.rowBytes(id)
		if err != nil {
			return nil, err
		}
		ver := binary.LittleEndian.Uint32(row[4:8])
		r := tcRow{id: id, ver: ver, cells: make(map[uint16][]byte)}
		ceb := row[v.info.RgIB[2]:v.info.RgIB[3]]
		for _, col := range v.info.Columns {
			if isLtpRowID(col) || isLtpRowVer(col) || !cebHas(ceb, col.Bit) {
				continue
			}
			_, data, err := v.Get(id, col.PropID)
			if err != nil {
				return nil, err
			}
			r.cells[col.PropID] = data
		}
		t.byRow[id] = len(t.rows)
		t.rows = append(t.rows, r)
		if id >= t.nextID {
			t.nextID = id + 1
		}
	}
	return t, nil
}
