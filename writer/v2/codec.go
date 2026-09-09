package writer

import "github.com/grokify/outlook-pst-go/pkg/disk"

// PagePayloadMax is the Unicode page data area (512 - 16 trailer).
func PagePayloadMax() int { return PageSize - UnicodePageTrailer }

// ValidateBlockCB returns the on-disk size of a Unicode block with payload cb.
// Total (cb + BLOCKTRAILER) is 64-byte aligned and at most 8192.
func ValidateBlockCB(cb uint64) (uint64, error) {
	if cb > MaxDataBlockCB {
		return 0, invalidArg("cb", "block data %d exceeds Unicode max %d (MS-PST %s)", cb, MaxDataBlockCB, SectionBlockTrailer)
	}
	disk := BlockDiskSize(cb)
	if disk > MaxAllocBytes {
		return 0, limitErr("size", "on-disk block %d exceeds MS-PST max %d", disk, MaxAllocBytes)
	}
	if disk%BytesPerSlot != 0 {
		return 0, invariant(SectionBlockAlign, "alignment", "on-disk block size %d is not a multiple of %d", disk, BytesPerSlot)
	}
	if disk < UnicodeBlockTrailer {
		return 0, invariant(SectionBlockTrailer, "size", "on-disk block %d shorter than trailer", disk)
	}
	return disk, nil
}

func knownCrypt(method byte) bool {
	switch method {
	case CryptNone, CryptPermute, CryptCyclic:
		return true
	default:
		return false
	}
}

func cryptData(data []byte, method byte, bid uint64) ([]byte, error) {
	switch method {
	case CryptNone:
		out := make([]byte, len(data))
		copy(out, data)
		return out, nil
	case CryptPermute:
		return disk.PermuteEncode(data), nil
	case CryptCyclic:
		return disk.CyclicEncode(data, uint32(bid)), nil
	case CryptWIP:
		return nil, unsupported(FeatureWIPCrypt, "WIP crypt is not implemented")
	default:
		return nil, invalidArg("crypt", "unknown method 0x%02x (MS-PST %s)", method, SectionCrypt)
	}
}

func decryptData(data []byte, method byte, bid uint64) ([]byte, error) {
	switch method {
	case CryptNone:
		out := make([]byte, len(data))
		copy(out, data)
		return out, nil
	case CryptPermute:
		return disk.PermuteDecode(data), nil
	case CryptCyclic:
		return disk.CyclicDecode(data, uint32(bid)), nil
	case CryptWIP:
		return nil, unsupported(FeatureWIPCrypt, "WIP crypt is not implemented")
	default:
		return nil, invalidArg("crypt", "unknown method 0x%02x (MS-PST %s)", method, SectionCrypt)
	}
}

func allocatedPageBID(pageType byte) bool {
	switch pageType {
	case PageBBT, PageNBT, PageDList:
		return true
	default:
		return false
	}
}
