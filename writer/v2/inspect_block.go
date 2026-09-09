package writer

import "encoding/binary"

// BlockView is a decoded Unicode data block. See MS-PST 2.2.2.8.1.
type BlockView struct {
	CB       uint16
	Sig      uint16
	CRC      uint32
	BID      uint64
	Offset   uint64
	Internal bool
	Crypt    byte
	Data     []byte // on-disk cb bytes (ciphertext when encrypted)
	Plain    []byte // decrypted data for external blocks
	DiskLen  uint64
	Raw      []byte
}

// EncodeBlock builds an unencrypted external Unicode block.
// Trailer is at the end; disk size is Align64(cb + trailer); CRC covers
// exactly cb data bytes, never padding. See MS-PST 2.2.2.8.1.
func EncodeBlock(data []byte, bid, offset uint64) ([]byte, error) {
	return EncodeExternalBlock(data, bid, offset, CryptNone)
}

// EncodeExternalBlock encodes a data block. Encryption applies to the cb
// data bytes only; the trailer is never encrypted. bidInternal must be 0.
func EncodeExternalBlock(data []byte, bid, offset uint64, crypt byte) ([]byte, error) {
	if BIDIsInternal(bid) {
		return nil, invalidArg("bid", "external block has bidInternal set (MS-PST %s)", SectionBID)
	}
	return encodeBlock(data, bid, offset, crypt, false)
}

// EncodeInternalBlock encodes an XBLOCK/SLBLOCK-style block. bidInternal is
// set; encryption is not applied. See MS-PST 2.2.2.8.3.
func EncodeInternalBlock(data []byte, bid, offset uint64) ([]byte, error) {
	return encodeBlock(data, bid, offset, CryptNone, true)
}

func encodeBlock(data []byte, bid, offset uint64, crypt byte, internal bool) ([]byte, error) {
	if bid == 0 {
		return nil, invalidArg("bid", "null BID (MS-PST %s)", SectionBID)
	}
	if BIDHasReserved(bid) {
		return nil, invalidArg("bid", "reserved bit must be 0 (MS-PST %s)", SectionBID)
	}
	if internal {
		bid = MakeInternalBID(bid)
		crypt = CryptNone
	}
	onDisk, err := cryptData(data, crypt, bid)
	if err != nil {
		return nil, err
	}
	cb := uint64(len(onDisk))
	disk, err := ValidateBlockCB(cb)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, disk)
	copy(buf, onDisk)
	crc := crc32PST(onDisk) // CRC of cb bytes only, never padding
	sig := signature(bid, offset)
	tr := buf[disk-UnicodeBlockTrailer:]
	binary.LittleEndian.PutUint16(tr[0:2], uint16(cb))
	binary.LittleEndian.PutUint16(tr[2:4], sig)
	binary.LittleEndian.PutUint32(tr[4:8], crc)
	binary.LittleEndian.PutUint64(tr[8:16], bid)
	return buf, nil
}

// InspectBlock validates a Unicode block at file offset (no decryption).
func InspectBlock(raw []byte, offset uint64) (*BlockView, error) {
	return inspectBlock(raw, offset, CryptNone, false)
}

// InspectBlockCrypt validates a Unicode block and decrypts an external
// payload with the given crypt method. Internal blocks are never decrypted.
func InspectBlockCrypt(raw []byte, offset uint64, crypt byte) (*BlockView, error) {
	return inspectBlock(raw, offset, crypt, true)
}

func inspectBlock(raw []byte, offset uint64, crypt byte, decrypt bool) (*BlockView, error) {
	if len(raw) < UnicodeBlockTrailer {
		return nil, invariant(SectionBlockTrailer, "size", "block is %d bytes, shorter than trailer", len(raw))
	}
	if uint64(len(raw))%BytesPerSlot != 0 {
		return nil, invariant(SectionBlockAlign, "alignment", "on-disk block size %d is not a multiple of 64", len(raw))
	}
	tr := raw[len(raw)-UnicodeBlockTrailer:]
	cb := binary.LittleEndian.Uint16(tr[0:2])
	wantDisk, err := ValidateBlockCB(uint64(cb))
	if err != nil {
		// Size/limit failures from a disk image are invariants.
		if e, ok := err.(*Error); ok && e.Code != CodeInvariant {
			return nil, invariant(SectionBlockTrailer, e.Field, "%s", e.Detail)
		}
		return nil, err
	}
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
	if bid == 0 {
		return nil, invariant(SectionBID, "bid", "null BID")
	}
	if BIDHasReserved(bid) {
		return nil, invariant(SectionBID, "bid", "reserved bit must be 0")
	}
	gotSig := binary.LittleEndian.Uint16(tr[2:4])
	wantSig := signature(bid, offset)
	if gotSig != wantSig {
		return nil, invariant(SectionSignature, "wSig", "got 0x%04x want 0x%04x for bid=0x%x ib=0x%x", gotSig, wantSig, bid, offset)
	}
	internal := BIDIsInternal(bid)
	plain := append([]byte(nil), data...)
	usedCrypt := CryptNone
	if decrypt && !internal {
		if !knownCrypt(crypt) {
			if crypt == CryptWIP {
				return nil, unsupported(FeatureWIPCrypt, "WIP crypt is not implemented")
			}
			return nil, invalidArg("crypt", "unknown method 0x%02x (MS-PST %s)", crypt, SectionCrypt)
		}
		dec, err := decryptData(data, crypt, bid)
		if err != nil {
			return nil, err
		}
		plain = dec
		usedCrypt = crypt
	}
	return &BlockView{
		CB:       cb,
		Sig:      gotSig,
		CRC:      gotCRC,
		BID:      bid,
		Offset:   offset,
		Internal: internal,
		Crypt:    usedCrypt,
		Data:     append([]byte(nil), data...),
		Plain:    plain,
		DiskLen:  uint64(len(raw)),
		Raw:      append([]byte(nil), raw...),
	}, nil
}
