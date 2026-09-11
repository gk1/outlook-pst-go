package writer

import (
	"bytes"
	"encoding/binary"
	"io"
	"unicode/utf16"
)

// PC is a Property Context builder over HN/BTH. See MS-PST 2.3.3.
// Keys are PropIDs. Duplicate IDs are rejected by Add. Set replaces.
type PC struct {
	n     *NDB
	heap  *Heap
	props map[uint16]pcProp
	order []uint16
}

type pcProp struct {
	typ  uint16
	data []byte
	hnid uint32 // if set, used as dwValueHnid (PtypObject / pre-placed HNID)
}

func NewPC(n *NDB) *PC {
	return &PC{n: n, heap: NewHeap(n, HeapSigPC), props: make(map[uint16]pcProp)}
}

func (p *PC) Add(id, typ uint16, data []byte) error {
	if p == nil {
		return invalidArg("pc", "nil PC")
	}
	if _, ok := p.props[id]; ok {
		return invalidArg("propID", "duplicate property 0x%04x", id)
	}
	p.props[id] = pcProp{typ: typ, data: append([]byte(nil), data...)}
	p.order = append(p.order, id)
	return nil
}

func (p *PC) Set(id, typ uint16, data []byte) error {
	if p == nil {
		return invalidArg("pc", "nil PC")
	}
	if _, ok := p.props[id]; !ok {
		p.order = append(p.order, id)
	}
	p.props[id] = pcProp{typ: typ, data: append([]byte(nil), data...)}
	return nil
}

func (p *PC) Delete(id uint16) {
	if p == nil {
		return
	}
	if _, ok := p.props[id]; !ok {
		return
	}
	delete(p.props, id)
	out := p.order[:0]
	for _, x := range p.order {
		if x != id {
			out = append(out, x)
		}
	}
	p.order = out
}

func ptypFixedSize(t uint16) int {
	switch t {
	case PtypInteger16, PtypBoolean, PtypError:
		return 2
	case PtypInteger32, PtypFloating32:
		return 4
	case PtypInteger64, PtypFloating64, PtypCurrency, PtypTime:
		return 8
	case PtypGUID:
		return 16
	default:
		return 0
	}
}

func ptypInline(t uint16) bool {
	n := ptypFixedSize(t)
	return n > 0 && n <= 4
}

func encodeUnicodePC(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, (len(u)+1)*2)
	for i, r := range u {
		binary.LittleEndian.PutUint16(b[i*2:], r)
	}
	return b
}

func decodeUnicodePC(b []byte) string {
	if len(b) >= 2 && b[len(b)-1] == 0 && b[len(b)-2] == 0 {
		b = b[:len(b)-2]
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[i*2:])
	}
	return string(utf16.Decode(u))
}

func (p *PC) SetString(id uint16, s string) error {
	return p.Set(id, PtypString, encodeUnicodePC(s))
}

func (p *PC) SetBinary(id uint16, b []byte) error {
	if len(b) > HeapMaxAlloc {
		return p.SetBinaryStream(id, bytes.NewReader(b), int64(len(b)))
	}
	return p.Set(id, PtypBinary, b)
}

// SetBinaryStream stores a PtypBinary by streaming r through an HNID data
// tree (MS-PST 2.3.1 / 2.4.6.2.2). The payload is not retained on the PC.
func (p *PC) SetBinaryStream(id uint16, r io.Reader, size int64) error {
	if p == nil {
		return invalidArg("pc", "nil PC")
	}
	hnid, err := p.heap.AllocateStream(r, size)
	if err != nil {
		return err
	}
	if _, ok := p.props[id]; !ok {
		p.order = append(p.order, id)
	}
	p.props[id] = pcProp{typ: PtypBinary, hnid: hnid}
	return nil
}

func (p *PC) SetInt32(id uint16, v int32) error {
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, uint32(v))
	return p.Set(id, PtypInteger32, buf)
}

func (p *PC) SetInt16(id uint16, v int16) error {
	buf := make([]byte, 2)
	binary.LittleEndian.PutUint16(buf, uint16(v))
	return p.Set(id, PtypInteger16, buf)
}

func (p *PC) SetBool(id uint16, v bool) error {
	buf := make([]byte, 2)
	if v {
		binary.LittleEndian.PutUint16(buf, 1)
	}
	return p.Set(id, PtypBoolean, buf)
}

func (p *PC) SetInt64(id uint16, v int64) error {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, uint64(v))
	return p.Set(id, PtypInteger64, buf)
}

func (p *PC) SetTime(id uint16, filetime uint64) error {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, filetime)
	return p.Set(id, PtypTime, buf)
}

func (p *PC) SetGUID(id uint16, g [16]byte) error {
	return p.Set(id, PtypGUID, g[:])
}

func (p *PC) SetObject(id uint16, data []byte) error {
	return p.Set(id, PtypObject, data)
}

// SetObjectNID records a PtypObject whose dwValueHnid is an existing subnode
// (embedded messages: NID_TYPE_NORMAL_MESSAGE sharing the attachment nidIndex).
func (p *PC) SetObjectNID(id uint16, nid uint32) error {
	if p == nil {
		return invalidArg("pc", "nil PC")
	}
	if nid == 0 {
		return invalidArg("nid", "PtypObject NID is 0")
	}
	if _, ok := p.props[id]; !ok {
		p.order = append(p.order, id)
	}
	p.props[id] = pcProp{typ: PtypObject, hnid: nid}
	return nil
}

func (p *PC) SetMVInt32(id uint16, vals []int32) error {
	buf := make([]byte, 4+4*len(vals))
	binary.LittleEndian.PutUint32(buf, uint32(len(vals)))
	for i, v := range vals {
		binary.LittleEndian.PutUint32(buf[4+4*i:], uint32(v))
	}
	return p.Set(id, PtypMVInteger32, buf)
}

func (p *PC) SetMVString(id uint16, vals []string) error {
	return p.Set(id, PtypMVString, encodeMVString(vals))
}

func (p *PC) SetMVBinary(id uint16, vals [][]byte) error {
	return p.Set(id, PtypMVBinary, encodeMVBinary(vals))
}

func encodeMVString(vals []string) []byte {
	var items [][]byte
	for _, s := range vals {
		items = append(items, encodeUnicodePC(s))
	}
	return encodeMVVar(items)
}

func encodeMVBinary(vals [][]byte) []byte {
	return encodeMVVar(vals)
}

func encodeMVVar(items [][]byte) []byte {
	n := 4
	for _, it := range items {
		n += 4 + len(it)
	}
	buf := make([]byte, n)
	binary.LittleEndian.PutUint32(buf, uint32(len(items)))
	off := 4
	for _, it := range items {
		binary.LittleEndian.PutUint32(buf[off:], uint32(len(it)))
		off += 4
		copy(buf[off:], it)
		off += len(it)
	}
	return buf
}

func decodeMVVar(b []byte) ([][]byte, error) {
	if len(b) < 4 {
		return nil, invariant(SectionPC, "mv", "truncated multi-value")
	}
	n := int(binary.LittleEndian.Uint32(b))
	off := 4
	out := make([][]byte, 0, n)
	for i := 0; i < n; i++ {
		if off+4 > len(b) {
			return nil, invariant(SectionPC, "mv", "truncated length")
		}
		ln := int(binary.LittleEndian.Uint32(b[off:]))
		off += 4
		if off+ln > len(b) {
			return nil, invariant(SectionPC, "mv", "truncated item")
		}
		out = append(out, append([]byte(nil), b[off:off+ln]...))
		off += ln
	}
	return out, nil
}

func (p *PC) place(pr pcProp) (uint32, error) {
	if pr.hnid != 0 {
		return pr.hnid, nil
	}
	typ, data := pr.typ, pr.data
	if typ == PtypObject {
		// PtypObject dwValueHnid is the object subnode NID (NID_TYPE_LTP),
		// not a HID wrapping a private {NID,size} record. See MS-PST 2.3.3.3.
		return p.heap.newSub(data), nil
	}
	if ptypInline(typ) {
		var v uint32
		switch len(data) {
		case 0:
		case 1:
			v = uint32(data[0])
		case 2:
			v = uint32(binary.LittleEndian.Uint16(data))
		case 4:
			v = binary.LittleEndian.Uint32(data)
		default:
			return 0, invalidArg("cb", "inline property %d bytes", len(data))
		}
		return v, nil
	}
	return p.heap.Allocate(data)
}

func (p *PC) Commit(nid uint32) error {
	if p == nil || p.n == nil {
		return invalidArg("pc", "nil PC")
	}
	if err := p.build(); err != nil {
		return err
	}
	return p.heap.Commit(nid)
}

// AttachTo writes this PC as a subnode of parent (attachment objects, MS-PST 2.4.6.1).
func (p *PC) AttachTo(parent *Heap, nid uint32) error {
	if p == nil || p.n == nil {
		return invalidArg("pc", "nil PC")
	}
	if parent == nil {
		return invalidArg("heap", "nil parent heap")
	}
	if err := p.build(); err != nil {
		return err
	}
	return parent.AttachHeap(nid, p.heap)
}

func (p *PC) build() error {
	ids := append([]uint16(nil), p.order...)
	for i := 1; i < len(ids); i++ {
		j := i
		for j > 0 && ids[j] < ids[j-1] {
			ids[j], ids[j-1] = ids[j-1], ids[j]
			j--
		}
	}
	bth, err := NewBTH(p.heap, 2, PCLeafValueSize)
	if err != nil {
		return err
	}
	key := make([]byte, 2)
	for _, id := range ids {
		pr := p.props[id]
		hnid, err := p.place(pr)
		if err != nil {
			return err
		}
		rec := make([]byte, PCLeafValueSize)
		binary.LittleEndian.PutUint16(rec[0:2], pr.typ)
		binary.LittleEndian.PutUint32(rec[2:6], hnid)
		binary.LittleEndian.PutUint16(key, id)
		if err := bth.Insert(key, rec); err != nil {
			return err
		}
	}
	_, err = bth.Build()
	return err
}

// PCView is a committed Property Context.
type PCView struct {
	heap *HeapView
	bth  *BTHView
	nid  uint32
}

func OpenPC(n *NDB, nid uint32) (*PCView, error) {
	h, err := OpenHeap(n, nid)
	if err != nil {
		return nil, err
	}
	if h.ClientSig() != HeapSigPC {
		return nil, invariant(SectionPC, "bClientSig", "got 0x%02x want 0x%02x", h.ClientSig(), HeapSigPC)
	}
	bth, err := OpenBTH(h, h.Root())
	if err != nil {
		return nil, err
	}
	return &PCView{heap: h, bth: bth, nid: nid}, nil
}

func (v *PCView) IDs() ([]uint16, error) {
	ents, err := v.bth.Entries()
	if err != nil {
		return nil, err
	}
	ids := make([]uint16, 0, len(ents))
	for _, e := range ents {
		ids = append(ids, binary.LittleEndian.Uint16(e.key))
	}
	return ids, nil
}

func (v *PCView) Get(id uint16) (typ uint16, data []byte, err error) {
	key := make([]byte, 2)
	binary.LittleEndian.PutUint16(key, id)
	rec, err := v.bth.Lookup(key)
	if err != nil {
		return 0, nil, err
	}
	if len(rec) < PCLeafValueSize {
		return 0, nil, invariant(SectionPC, "PCBTH", "record %d bytes", len(rec))
	}
	typ = binary.LittleEndian.Uint16(rec[0:2])
	hnid := binary.LittleEndian.Uint32(rec[2:6])
	data, err = v.resolve(typ, hnid)
	return typ, data, err
}

func (v *PCView) resolve(typ uint16, hnid uint32) ([]byte, error) {
	if typ == PtypObject {
		if IsHID(hnid) || NIDTypeOf(hnid) != NIDTypeLTP {
			return nil, invariant(SectionPC, "dwValueHnid", "PtypObject HNID 0x%x must be NID_TYPE_LTP (MS-PST %s)", hnid, SectionPC)
		}
		return v.heap.Read(hnid)
	}
	if ptypInline(typ) {
		n := ptypFixedSize(typ)
		buf := make([]byte, 4)
		binary.LittleEndian.PutUint32(buf, hnid)
		return buf[:n], nil
	}
	return v.heap.Read(hnid)
}

func (v *PCView) GetString(id uint16) (string, error) {
	typ, data, err := v.Get(id)
	if err != nil {
		return "", err
	}
	if typ != PtypString {
		return "", invalidArg("wPropType", "0x%04x is not PtypString", typ)
	}
	return decodeUnicodePC(data), nil
}

func (v *PCView) GetBinary(id uint16) ([]byte, error) {
	typ, data, err := v.Get(id)
	if err != nil {
		return nil, err
	}
	if typ != PtypBinary {
		return nil, invalidArg("wPropType", "0x%04x is not PtypBinary", typ)
	}
	return append([]byte(nil), data...), nil
}

func (v *PCView) GetInt32(id uint16) (int32, error) {
	typ, data, err := v.Get(id)
	if err != nil {
		return 0, err
	}
	if typ != PtypInteger32 || len(data) < 4 {
		return 0, invalidArg("wPropType", "0x%04x is not PtypInteger32", typ)
	}
	return int32(binary.LittleEndian.Uint32(data)), nil
}

func (v *PCView) GetBool(id uint16) (bool, error) {
	typ, data, err := v.Get(id)
	if err != nil {
		return false, err
	}
	if typ != PtypBoolean {
		return false, invalidArg("wPropType", "0x%04x is not PtypBoolean", typ)
	}
	switch len(data) {
	case 0:
		return false, nil
	case 1:
		return data[0] != 0, nil
	default:
		return binary.LittleEndian.Uint16(data) != 0, nil
	}
}

func (v *PCView) GetTime(id uint16) (uint64, error) {
	typ, data, err := v.Get(id)
	if err != nil {
		return 0, err
	}
	if typ != PtypTime || len(data) < 8 {
		return 0, invalidArg("wPropType", "0x%04x is not PtypTime", typ)
	}
	return binary.LittleEndian.Uint64(data), nil
}

func (v *PCView) GetMVString(id uint16) ([]string, error) {
	typ, data, err := v.Get(id)
	if err != nil {
		return nil, err
	}
	if typ != PtypMVString {
		return nil, invalidArg("wPropType", "0x%04x is not PtypMVString", typ)
	}
	items, err := decodeMVVar(data)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = decodeUnicodePC(it)
	}
	return out, nil
}

func (v *PCView) GetMVBinary(id uint16) ([][]byte, error) {
	typ, data, err := v.Get(id)
	if err != nil {
		return nil, err
	}
	if typ != PtypMVBinary {
		return nil, invalidArg("wPropType", "0x%04x is not PtypMVBinary", typ)
	}
	return decodeMVVar(data)
}

func (v *PCView) Load() (*PC, error) {
	ids, err := v.IDs()
	if err != nil {
		return nil, err
	}
	p := NewPC(v.heap.n)
	for _, id := range ids {
		typ, data, err := v.Get(id)
		if err != nil {
			return nil, err
		}
		if err := p.Add(id, typ, data); err != nil {
			return nil, err
		}
	}
	return p, nil
}
