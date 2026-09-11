package writer

import (
	"bytes"
	"encoding/binary"
	"errors"
	"path/filepath"
	"testing"
)

func TestTCEmptyRoundTrip(t *testing.T) {
	n := NewNDB(nil)
	tc, err := NewTC(n, nil)
	if err != nil {
		t.Fatal(err)
	}
	nid := MakeNID(NIDTypeInternal, 0x30)
	if err := tc.Commit(nid); err != nil {
		t.Fatal(err)
	}
	v, err := OpenTC(n, nid)
	if err != nil {
		t.Fatal(err)
	}
	if v.RowCount() != 0 {
		t.Fatalf("rows %d", v.RowCount())
	}
	if v.Info().RowMatrix != 0 {
		t.Fatalf("hnidRows 0x%x want 0", v.Info().RowMatrix)
	}
	if _, err := InspectTable(v.Info().Raw); err != nil {
		t.Fatal(err)
	}
	if v.Info().HidIndex != 0 {
		t.Fatalf("hidIndex %d", v.Info().HidIndex)
	}
}

func TestTCOneRowSparseVariableObject(t *testing.T) {
	n := NewNDB(nil)
	tc, err := NewTC(n, []ColumnView{
		{PropType: PtypInteger32, PropID: 0x0E07},
		{PropType: PtypString, PropID: 0x0037},
		{PropType: PtypBinary, PropID: 0x0102},
		{PropType: PtypTime, PropID: 0x0039},
		{PropType: PtypBoolean, PropID: 0x0E1B},
		{PropType: PtypObject, PropID: 0x0EA3},
	})
	if err != nil {
		t.Fatal(err)
	}
	id, err := tc.Add()
	if err != nil {
		t.Fatal(err)
	}
	if err := tc.SetInt32(id, 0x0E07, 7); err != nil {
		t.Fatal(err)
	}
	if err := tc.SetString(id, 0x0037, "café"); err != nil {
		t.Fatal(err)
	}
	if err := tc.SetString(id, 0x0037, ""); err != nil {
		t.Fatal(err)
	}
	if err := tc.SetBinary(id, 0x0102, nil); err != nil {
		t.Fatal(err)
	}
	if err := tc.SetInt64(id, 0x0039, int64(0x01D00000DEADBEEF)); err != nil {
		t.Fatal(err)
	}
	if err := tc.SetBool(id, 0x0E1B, true); err != nil {
		t.Fatal(err)
	}
	obj := bytes.Repeat([]byte("OBJ"), 40)
	if err := tc.SetObject(id, 0x0EA3, obj); err != nil {
		t.Fatal(err)
	}
	nid := MakeNID(NIDTypeInternal, 0x31)
	if err := tc.Commit(nid); err != nil {
		t.Fatal(err)
	}
	v, err := OpenTC(n, nid)
	if err != nil {
		t.Fatal(err)
	}
	if v.RowCount() != 1 {
		t.Fatalf("rows %d", v.RowCount())
	}
	if v.blocked {
		t.Fatal("one-row matrix should be a HID")
	}
	ok, err := v.Exists(id, 0x0E07)
	if err != nil || !ok {
		t.Fatalf("int32 exists %v %v", ok, err)
	}
	typ, data, err := v.Get(id, 0x0E07)
	if err != nil || typ != PtypInteger32 || binary.LittleEndian.Uint32(data) != 7 {
		t.Fatalf("int32 %x %x %v", typ, data, err)
	}
	s, err := v.GetString(id, 0x0037)
	if err != nil || s != "" {
		t.Fatalf("empty string %q %v", s, err)
	}
	typ, data, err = v.Get(id, 0x0102)
	if err != nil || typ != PtypBinary || len(data) != 0 {
		t.Fatalf("empty binary %x %d %v", typ, len(data), err)
	}
	typ, data, err = v.Get(id, 0x0EA3)
	if err != nil || typ != PtypObject || !bytes.Equal(data, obj) {
		t.Fatalf("object %x %d %v", typ, len(data), err)
	}
	row, err := v.rowBytes(id)
	if err != nil {
		t.Fatal(err)
	}
	col, _ := v.column(0x0EA3)
	hnid := binary.LittleEndian.Uint32(row[col.Offset : col.Offset+4])
	if IsHID(hnid) || NIDTypeOf(hnid) != NIDTypeLTP {
		t.Fatalf("PtypObject cell 0x%x want NID_TYPE_LTP", hnid)
	}
	ceb := row[v.Info().RgIB[2]:v.Info().RgIB[3]]
	if ceb[0]&1 == 0 || ceb[0]&2 == 0 {
		t.Fatalf("CEB LSB order: byte0=0x%02x want bits 0 and 1 set", ceb[0])
	}
	missing := uint16(0x0E08)
	if _, ok := v.column(missing); ok {
		t.Fatal("unexpected extra column")
	}
}

func TestTCAddUpdateDelete(t *testing.T) {
	n := NewNDB(nil)
	tc, err := NewTC(n, []ColumnView{
		{PropType: PtypInteger32, PropID: 0x0E07},
		{PropType: PtypString, PropID: 0x0037},
	})
	if err != nil {
		t.Fatal(err)
	}
	a, err := tc.Add()
	if err != nil {
		t.Fatal(err)
	}
	b, err := tc.Add()
	if err != nil {
		t.Fatal(err)
	}
	c, err := tc.Add()
	if err != nil {
		t.Fatal(err)
	}
	if err := tc.SetInt32(a, 0x0E07, 1); err != nil {
		t.Fatal(err)
	}
	if err := tc.SetInt32(b, 0x0E07, 2); err != nil {
		t.Fatal(err)
	}
	if err := tc.SetInt32(c, 0x0E07, 3); err != nil {
		t.Fatal(err)
	}
	if err := tc.SetString(b, 0x0037, "keep"); err != nil {
		t.Fatal(err)
	}
	if err := tc.Delete(a); err != nil {
		t.Fatal(err)
	}
	if err := tc.SetInt32(c, 0x0E07, 33); err != nil {
		t.Fatal(err)
	}
	if err := tc.Clear(b, 0x0E07); err != nil {
		t.Fatal(err)
	}
	nid := MakeNID(NIDTypeInternal, 0x32)
	if err := tc.Commit(nid); err != nil {
		t.Fatal(err)
	}
	v, err := OpenTC(n, nid)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := v.RowIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("ids %v", ids)
	}
	if _, _, err := v.Get(a, 0x0E07); err == nil {
		t.Fatal("deleted row still present")
	}
	ok, err := v.Exists(b, 0x0E07)
	if err != nil || ok {
		t.Fatalf("cleared cell exists %v %v", ok, err)
	}
	s, err := v.GetString(b, 0x0037)
	if err != nil || s != "keep" {
		t.Fatalf("string %q %v", s, err)
	}
	_, data, err := v.Get(c, 0x0E07)
	if err != nil || binary.LittleEndian.Uint32(data) != 33 {
		t.Fatalf("updated %x %v", data, err)
	}
	loaded, err := v.Load()
	if err != nil {
		t.Fatal(err)
	}
	d, err := loaded.Add()
	if err != nil {
		t.Fatal(err)
	}
	if err := loaded.SetInt32(d, 0x0E07, 4); err != nil {
		t.Fatal(err)
	}
	nid2 := MakeNID(NIDTypeInternal, 0x33)
	if err := loaded.Commit(nid2); err != nil {
		t.Fatal(err)
	}
	v2, err := OpenTC(n, nid2)
	if err != nil {
		t.Fatal(err)
	}
	if v2.RowCount() != 3 {
		t.Fatalf("after load+add rows %d", v2.RowCount())
	}
	s, err = v2.GetString(b, 0x0037)
	if err != nil || s != "keep" {
		t.Fatalf("unknown/variable dropped: %q %v", s, err)
	}
}

func TestTCThousandsMultiBlockAndMultiLevel(t *testing.T) {
	n := NewNDB(nil)
	tc, err := NewTC(n, []ColumnView{
		{PropType: PtypInteger32, PropID: 0x0E07},
		{PropType: PtypString, PropID: 0x0037},
		{PropType: PtypTime, PropID: 0x0039},
		{PropType: PtypBoolean, PropID: 0x0E1B},
	})
	if err != nil {
		t.Fatal(err)
	}
	const nRow = 2500
	for i := 0; i < nRow; i++ {
		id, err := tc.Add()
		if err != nil {
			t.Fatal(err)
		}
		if i%3 == 0 {
			if err := tc.SetInt32(id, 0x0E07, int32(i)); err != nil {
				t.Fatal(err)
			}
		}
		if i%5 == 0 {
			if err := tc.SetString(id, 0x0037, "r"); err != nil {
				t.Fatal(err)
			}
		}
		if err := tc.SetInt64(id, 0x0039, int64(i)); err != nil {
			t.Fatal(err)
		}
		if i%2 == 0 {
			if err := tc.SetBool(id, 0x0E1B, true); err != nil {
				t.Fatal(err)
			}
		}
	}
	nid := MakeNID(NIDTypeInternal, 0x34)
	if err := tc.Commit(nid); err != nil {
		t.Fatal(err)
	}
	v, err := OpenTC(n, nid)
	if err != nil {
		t.Fatal(err)
	}
	if v.RowCount() != nRow {
		t.Fatalf("rows %d", v.RowCount())
	}
	if !v.blocked {
		t.Fatal("want subnode/blocked Row Matrix")
	}
	if v.bth.Levels() < 1 {
		t.Fatalf("want multi-level Row Index, got %d", v.bth.Levels())
	}
	if _, err := InspectTable(v.Info().Raw); err != nil {
		t.Fatal(err)
	}
	id := uint32(1)
	_, data, err := v.Get(id, 0x0E07)
	if err != nil || binary.LittleEndian.Uint32(data) != 0 {
		t.Fatalf("row 1 int32 %x %v", data, err)
	}
	ok, err := v.Exists(2, 0x0E07)
	if err != nil || ok {
		t.Fatalf("sparse row 2 should lack 0x0E07")
	}
	id = 2500
	_, data, err = v.Get(id, 0x0039)
	if err != nil || binary.LittleEndian.Uint64(data) != 2499 {
		t.Fatalf("last time %x %v", data, err)
	}
}

func TestTCObjectHNIDFileReopen(t *testing.T) {
	n := NewNDB(nil)
	tc, err := NewTC(n, []ColumnView{
		{PropType: PtypObject, PropID: 0x0EA3},
		{PropType: PtypString, PropID: 0x0037},
	})
	if err != nil {
		t.Fatal(err)
	}
	id, err := tc.Add()
	if err != nil {
		t.Fatal(err)
	}
	obj := bytes.Repeat([]byte("OBJ"), 80)
	if err := tc.SetObject(id, 0x0EA3, obj); err != nil {
		t.Fatal(err)
	}
	if err := tc.SetString(id, 0x0037, "hi"); err != nil {
		t.Fatal(err)
	}
	nid := MakeNID(NIDTypeInternal, 0x35)
	if err := tc.Commit(nid); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "tc.pst")
	if err := n.CommitFile(path); err != nil {
		t.Fatal(err)
	}
	_ = n.Close()
	n2, err := OpenNDBFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer n2.Close()
	v, err := OpenTC(n2, nid)
	if err != nil {
		t.Fatal(err)
	}
	row, err := v.rowBytes(id)
	if err != nil {
		t.Fatal(err)
	}
	col, _ := v.column(0x0EA3)
	hnid := binary.LittleEndian.Uint32(row[col.Offset : col.Offset+4])
	if IsHID(hnid) || NIDTypeOf(hnid) != NIDTypeLTP {
		t.Fatalf("PtypObject dwValueHnid 0x%x want NID_TYPE_LTP", hnid)
	}
	typ, data, err := v.Get(id, 0x0EA3)
	if err != nil || typ != PtypObject || !bytes.Equal(data, obj) {
		t.Fatalf("object after reopen typ=0x%x len=%d err=%v", typ, len(data), err)
	}
}

func TestInspectTableRowsRejectsSpan(t *testing.T) {
	n := NewNDB(nil)
	tc, err := NewTC(n, []ColumnView{{PropType: PtypInteger32, PropID: 0x0E07}})
	if err != nil {
		t.Fatal(err)
	}
	id, err := tc.Add()
	if err != nil {
		t.Fatal(err)
	}
	if err := tc.SetInt32(id, 0x0E07, 1); err != nil {
		t.Fatal(err)
	}
	nid := MakeNID(NIDTypeInternal, 0x36)
	if err := tc.Commit(nid); err != nil {
		t.Fatal(err)
	}
	v, err := OpenTC(n, nid)
	if err != nil {
		t.Fatal(err)
	}
	ents, err := v.bth.Entries()
	if err != nil {
		t.Fatal(err)
	}
	err = InspectTableRows(v.Info(), ents, v.matrix[:len(v.matrix)-1], false)
	if err == nil {
		t.Fatal("expected truncated matrix to fail")
	}
	if !errors.Is(err, ErrInvariant) {
		t.Fatalf("got %v", err)
	}
}

func TestTCRejectsSpecialMutation(t *testing.T) {
	n := NewNDB(nil)
	tc, err := NewTC(n, nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := tc.Add()
	if err != nil {
		t.Fatal(err)
	}
	if err := tc.SetInt32(id, PidTagLtpRowId, 9); !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("Set RowId: %v", err)
	}
	if err := tc.Clear(id, PidTagLtpRowVer); !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("Clear RowVer: %v", err)
	}
}
