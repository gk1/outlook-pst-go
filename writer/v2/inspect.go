package writer

import "github.com/grokify/outlook-pst-go/pkg/disk"

func crc32PST(data []byte) uint32 { return disk.ComputeCRC(data) }

func signature(id, address uint64) uint16 { return disk.ComputeSignature(id, address) }

func knownPageType(t byte) bool {
	switch t {
	case PageBBT, PageNBT, PageFMap, PagePMap, PageAMap, PageFPMap, PageDList:
		return true
	default:
		return false
	}
}
