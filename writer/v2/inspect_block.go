package writer

import "encoding/binary"

// BlockView is a decoded Unicode data block. See MS-PST 2.2.2.8.1.
type BlockView struct {
	CB      uint16
	Sig     uint16
	CRC     uint32
	BID     uint64
	Offset  uint64
	Data    []byte
	DiskLen uint64
	Raw     []byte
}

// EncodeBlock builds a Unicode block: trailer at the end, disk size
// Align64(cb + trailer), CRC over exactly cb data bytes.
func EncodeBlock(data []byte, bid, offset uint64) ([]byte, error) {
	if len(data) > MaxDataBlockCB {
		return nil, invalidArg("cb", "block data %d exceeds Unicode max %d", len(data), MaxDataBlockCB)
	}
	cb := uint64(len(data))
	disk := BlockDiskSize(cb)
	buf := make([]byte, disk)
	copy(buf, data)
	crc := crc32PST(data) // CRC of cb bytes only, never padding
	sig := signature(bid, offset)
	trOff := disk - UnicodeBlockTrailer
	tr := buf[trOff:]
	binary.LittleEndian.PutUint16(tr[0:2], uint16(cb))
	binary.LittleEndian.PutUint16(tr[2:4], sig)
	binary.LittleEndian.PutUint32(tr[4:8], crc)
	binary.LittleEndian.PutUint64(tr[8:16], bid)
	return buf, nil
}

// InspectBlock validates a Unicode block at file offset.
func InspectBlock(raw []byte, offset uint64) (*BlockView, error) {
	if len(raw) < UnicodeBlockTrailer {
		return nil, invariant(SectionBlockTrailer, "size", "block is %d bytes, shorter than trailer", len(raw))
	}
	if uint64(len(raw))%BytesPerSlot != 0 {
		return nil, invariant(SectionBlockAlign, "alignment", "on-disk block size %d is not a multiple of 64", len(raw))
	}
	tr := raw[len(raw)-UnicodeBlockTrailer:]
	cb := binary.LittleEndian.Uint16(tr[0:2])
	wantDisk := BlockDiskSize(uint64(cb))
	if uint64(len(raw)) != wantDisk {
		return nil, invariant(SectionBlockTrailer, "size", "disk size %d != Align64(cb=%d + trailer %d)=%d", len(raw), cb, UnicodeBlockTrailer, wantDisk)
	}
	if int(cb) > len(raw)-UnicodeBlockTrailer {
		return nil, invariant(SectionBlockTrailer, "cb", "cb %d exceeds data region", cb)
	}
	data := raw[:cb]
	gotCRC := binary.LittleEndian.Uint32(tr[4:8])
	wantCRC := crc32PST(data)
	if gotCRC != wantCRC {
		return nil, invariant(SectionBlockTrailer, "dwCRC", "got 0x%08x want 0x%08x over cb=%d data bytes, not padding (MS-PST %s)", gotCRC, wantCRC, cb, SectionCRC)
	}
	bid := binary.LittleEndian.Uint64(tr[8:16])
	gotSig := binary.LittleEndian.Uint16(tr[2:4])
	wantSig := signature(bid, offset)
	if gotSig != wantSig {
		return nil, invariant(SectionSignature, "wSig", "got 0x%04x want 0x%04x for bid=0x%x ib=0x%x", gotSig, wantSig, bid, offset)
	}
	return &BlockView{
		CB:      cb,
		Sig:     gotSig,
		CRC:     gotCRC,
		BID:     bid,
		Offset:  offset,
		Data:    append([]byte(nil), data...),
		DiskLen: uint64(len(raw)),
		Raw:     append([]byte(nil), raw...),
	}, nil
}
